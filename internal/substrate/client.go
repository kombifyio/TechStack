// Package substrate implements the node-local Proxmox API boundary. It never
// installs StackKits on a hypervisor or resolves commercial provider credentials.
package substrate

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	ErrOwnership  = errors.New("substrate: guest ownership mismatch")
	ErrNotFound   = errors.New("substrate: resource not found")
	ErrConflict   = errors.New("substrate: resource or operation conflict")
	ErrCapability = errors.New("substrate: required capability unavailable")
	identifier    = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
	operationTag  = regexp.MustCompile(`^kombify-op-[a-f0-9]{32,64}$`)
)

// Config is local configuration, not a remotely supplied request. TokenFile
// contains the full PVEAPIToken value (user@realm!token=secret) at mode 0600.
type Config struct {
	Endpoint          string `json:"endpoint"`
	Node              string `json:"node"`
	TokenFile         string `json:"token_file"`
	CertificateSHA256 string `json:"certificate_sha256"`
	MinGuestID        int    `json:"min_guest_id"`
	MaxGuestID        int    `json:"max_guest_id"`
}

type Client struct {
	cfg        Config
	http       *http.Client
	base       string
	credential func() (string, error)
}

// SameNodeAuthority compares the actual enrolled API destination, node and
// certificate identity. A matching VM number alone never grants cross-node USB
// custody; different credentials may still represent the same node authority.
func (c *Client) SameNodeAuthority(other *Client) bool {
	if c == nil || other == nil || c.cfg.Node == "" || c.base == "" || c.cfg.CertificateSHA256 == "" {
		return false
	}
	return c.base == other.base && c.cfg.Node == other.cfg.Node &&
		strings.EqualFold(strings.ReplaceAll(c.cfg.CertificateSHA256, ":", ""), strings.ReplaceAll(other.cfg.CertificateSHA256, ":", ""))
}

func NewClient(cfg Config) (*Client, error) {
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return nil, errors.New("substrate: HTTPS loopback endpoint required")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() {
		return nil, errors.New("substrate: only node-local loopback API is permitted")
	}
	if !identifier.MatchString(cfg.Node) || cfg.MinGuestID < 100 || cfg.MaxGuestID < cfg.MinGuestID || cfg.MaxGuestID > 999999999 {
		return nil, errors.New("substrate: node and reserved guest range required")
	}
	pin, err := hex.DecodeString(strings.ReplaceAll(strings.ToLower(cfg.CertificateSHA256), ":", ""))
	if err != nil || len(pin) != sha256.Size {
		return nil, errors.New("substrate: SHA256 leaf certificate pin required")
	}
	client := &Client{cfg: cfg, base: strings.TrimRight(cfg.Endpoint, "/") + "/api2/json"}
	client.credential = client.token
	if _, err := client.token(); err != nil {
		return nil, err
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Proxmox commonly uses a private leaf certificate. Verification below
		// replaces CA verification with the locally enrolled exact leaf pin.
		InsecureSkipVerify: true, // #nosec G402 -- mandatory exact leaf verification
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) == 0 {
				return errors.New("substrate: missing API certificate")
			}
			leaf := state.PeerCertificates[0]
			now := time.Now()
			if now.Before(leaf.NotBefore) || now.After(leaf.NotAfter) {
				return errors.New("substrate: API certificate expired or not valid yet")
			}
			digest := sha256.Sum256(leaf.Raw)
			if subtle.ConstantTimeCompare(pin, digest[:]) != 1 {
				return errors.New("substrate: API certificate pin mismatch")
			}
			return nil
		},
	}}
	client.http = &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("substrate: API redirect refused") }}
	return client, nil
}

func (c *Client) token() (string, error) {
	info, err := os.Lstat(c.cfg.TokenFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 4096 {
		return "", errors.New("substrate: private regular node-local token file required")
	}
	data, err := os.ReadFile(c.cfg.TokenFile)
	if err != nil {
		return "", errors.New("substrate: cannot read node-local token")
	}
	token := strings.TrimSpace(string(data))
	if !strings.Contains(token, "!") || !strings.Contains(token, "=") || strings.ContainsAny(token, "\r\n\t ") {
		return "", errors.New("substrate: malformed node-local token")
	}
	return token, nil
}

func (c *Client) request(ctx context.Context, method, path string, values url.Values, out any) error {
	var body io.Reader
	if values != nil {
		body = strings.NewReader(values.Encode())
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	token, err := c.credential()
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "PVEAPIToken="+token)
	if values != nil {
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	}
	response, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("substrate: API transport: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode == 404 {
		return ErrNotFound
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		// API errors may echo credential or caller-provided values. Do not
		// forward response bodies into receipts or application logs.
		return fmt.Errorf("substrate: API returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 4<<20)).Decode(&envelope); err != nil {
		return fmt.Errorf("substrate: invalid API response: %w", err)
	}
	if out != nil {
		return json.Unmarshal(envelope.Data, out)
	}
	return nil
}

func (c *Client) nodePath() string     { return "/nodes/" + c.cfg.Node }
func (c *Client) vmPath(id int) string { return c.nodePath() + "/qemu/" + strconv.Itoa(id) }

type GuestIdentity struct {
	ID           int    `json:"id"`
	Name         string `json:"name"`
	OperationTag string `json:"operation_tag"`
}

func (c *Client) validateIdentity(g GuestIdentity) error {
	if g.ID < c.cfg.MinGuestID || g.ID > c.cfg.MaxGuestID || !identifier.MatchString(g.Name) || !strings.HasPrefix(g.Name, "kombify-") || !operationTag.MatchString(g.OperationTag) {
		return ErrOwnership
	}
	return nil
}

type Guest struct {
	ID     int            `json:"vmid"`
	Name   string         `json:"name"`
	Status string         `json:"status"`
	Tags   string         `json:"tags"`
	Lock   string         `json:"lock,omitempty"`
	Config map[string]any `json:"config,omitempty"`
}

func (c *Client) Guest(ctx context.Context, id int) (Guest, error) {
	if id < 100 || id > 999999999 {
		return Guest{}, ErrOwnership
	}
	var permissions map[string]map[string]any
	permissionPath := "/vms/" + strconv.Itoa(id)
	if err := c.request(ctx, http.MethodGet, "/access/permissions?path="+url.QueryEscape(permissionPath), nil, &permissions); err != nil {
		return Guest{}, err
	}
	if permissions[permissionPath]["VM.Audit"] != float64(1) {
		return Guest{}, errors.New("substrate: exact guest audit permission required to prove presence or absence")
	}
	// List membership distinguishes absence from permission errors: Proxmox
	// can return HTTP 500 for a missing individual VM configuration.
	var guests []Guest
	if err := c.request(ctx, http.MethodGet, c.nodePath()+"/qemu", nil, &guests); err != nil {
		return Guest{}, err
	}
	for _, guest := range guests {
		if guest.ID != id {
			continue
		}
		if err := c.request(ctx, http.MethodGet, c.vmPath(id)+"/config", nil, &guest.Config); err != nil {
			return Guest{}, err
		}
		guest.Tags, _ = guest.Config["tags"].(string)
		guest.Name, _ = guest.Config["name"].(string)
		guest.Lock, _ = guest.Config["lock"].(string)
		return guest, nil
	}
	return Guest{}, ErrNotFound
}

func owns(identity GuestIdentity, guest Guest) bool {
	tags := strings.Split(guest.Tags, ";")
	managed, correlated := false, false
	for _, tag := range tags {
		managed = managed || tag == "kombify-managed"
		correlated = correlated || tag == identity.OperationTag
	}
	return identity.ID == guest.ID && identity.Name == guest.Name && managed && correlated
}

func (c *Client) ownedGuest(ctx context.Context, identity GuestIdentity) (Guest, error) {
	if err := c.validateIdentity(identity); err != nil {
		return Guest{}, err
	}
	guest, err := c.Guest(ctx, identity.ID)
	if err != nil {
		return Guest{}, err
	}
	if !owns(identity, guest) {
		return Guest{}, ErrOwnership
	}
	return guest, nil
}

type Submission struct {
	TaskRef   string `json:"task_ref,omitempty"`
	Recovered bool   `json:"recovered"`
	GuestID   int    `json:"guest_id,omitempty"`
}

type Task struct {
	UPID       string `json:"upid"`
	Status     string `json:"status"`
	ExitStatus string `json:"exitstatus,omitempty"`
}

func (t Task) Complete() bool  { return t.Status == "stopped" }
func (t Task) Succeeded() bool { return t.Complete() && t.ExitStatus == "OK" }

func (c *Client) ObserveTask(ctx context.Context, ref string) (Task, error) {
	if !strings.HasPrefix(ref, "UPID:"+c.cfg.Node+":") || len(ref) > 1024 || strings.ContainsAny(ref, "/\\\r\n") {
		return Task{}, errors.New("substrate: invalid local task reference")
	}
	var task Task
	err := c.request(ctx, http.MethodGet, c.nodePath()+"/tasks/"+url.PathEscape(ref)+"/status", nil, &task)
	return task, err
}

func (c *Client) submit(ctx context.Context, method, path string, values url.Values) (Submission, error) {
	var ref string
	err := c.request(ctx, method, path, values, &ref)
	if err != nil {
		return Submission{}, err
	}
	if !strings.HasPrefix(ref, "UPID:"+c.cfg.Node+":") {
		return Submission{}, errors.New("substrate: submission has no local task handle")
	}
	return Submission{TaskRef: ref}, nil
}

// SubmitLifecycle operates only on a guest created under the exact retained
// identity. Retiring an observed/imported guest never grants this permission.
func (c *Client) SubmitLifecycle(ctx context.Context, identity GuestIdentity, action string) (Submission, error) {
	if action != "start" && action != "shutdown" && action != "stop" && action != "delete" {
		return Submission{}, errors.New("substrate: unsupported lifecycle action")
	}
	guest, err := c.ownedGuest(ctx, identity)
	if errors.Is(err, ErrNotFound) && action == "delete" {
		return Submission{Recovered: true, GuestID: identity.ID}, nil
	}
	if err != nil {
		return Submission{}, err
	}
	if guest.Lock != "" {
		return Submission{}, ErrConflict
	}
	if action == "delete" {
		if guest.Status != "stopped" {
			return Submission{}, errors.New("substrate: guest must be observed stopped before deletion")
		}
		return c.submit(ctx, http.MethodDelete, c.vmPath(identity.ID), url.Values{"purge": {"0"}, "destroy-unreferenced-disks": {"0"}})
	}
	if (action == "start" && guest.Status == "running") || ((action == "shutdown" || action == "stop") && guest.Status == "stopped") {
		return Submission{Recovered: true, GuestID: identity.ID}, nil
	}
	return c.submit(ctx, http.MethodPost, c.vmPath(identity.ID)+"/status/"+action, url.Values{})
}
