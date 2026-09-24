package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ObservedGuestGrant is a separately approved and durably retained source
// power grant. It never grants deletion, configuration or device transfer.
type ObservedGuestGrant struct {
	ID           int       `json:"id"`
	Name         string    `json:"name"`
	ConfigDigest string    `json:"config_digest"`
	ExpiresAt    time.Time `json:"expires_at"`
	AllowStart   bool      `json:"allow_start"`
	AllowStop    bool      `json:"allow_stop"`
}

func GuestConfigDigest(guest Guest) string {
	payload, _ := json.Marshal(guest.Config)
	digest := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (c *Client) SubmitObservedLifecycle(ctx context.Context, grant ObservedGuestGrant, action string) (Submission, error) {
	if !grant.ExpiresAt.After(time.Now()) || (action != "start" && action != "stop") || (action == "start" && !grant.AllowStart) || (action == "stop" && !grant.AllowStop) {
		return Submission{}, ErrOwnership
	}
	guest, err := c.Guest(ctx, grant.ID)
	if err != nil {
		return Submission{}, err
	}
	if guest.Name != grant.Name || GuestConfigDigest(guest) != grant.ConfigDigest || guest.Lock != "" {
		return Submission{}, ErrOwnership
	}
	if (action == "start" && guest.Status == "running") || (action == "stop" && guest.Status == "stopped") {
		return Submission{Recovered: true, GuestID: guest.ID}, nil
	}
	return c.submit(ctx, http.MethodPost, c.vmPath(guest.ID)+"/status/"+action, url.Values{})
}

// SetNetworkIsolation changes only the existing production NIC link state,
// while the exact owned VM is stopped. It does not prove management isolation.
func (c *Client) SetNetworkIsolation(ctx context.Context, identity GuestIdentity, isolated bool) error {
	guest, err := c.ownedGuest(ctx, identity)
	if err != nil {
		return err
	}
	if guest.Status != "stopped" || guest.Lock != "" {
		return ErrConflict
	}
	network, ok := guest.Config["net0"].(string)
	if !ok || network == "" {
		return ErrCapability
	}
	parts := strings.Split(network, ",")
	filtered := make([]string, 0, len(parts)+1)
	for _, part := range parts {
		if !strings.HasPrefix(part, "link_down=") {
			filtered = append(filtered, part)
		}
	}
	if isolated {
		filtered = append(filtered, "link_down=1")
	}
	return c.request(ctx, http.MethodPut, c.vmPath(identity.ID)+"/config", url.Values{"net0": {strings.Join(filtered, ",")}, "digest": {stringConfig(guest.Config, "digest")}}, nil)
}

type ManagementIsolation struct {
	Bridge    string `json:"bridge"`
	ManagerIP string `json:"manager_ip"`
	Port      string `json:"port"`
}

// ConfigureManagementIsolation configures only this stopped owned guest. It
// uses an existing bridge; host/datacenter enforcement must already be enabled
// so configuring a new VM cannot alter other tenants' firewall behavior.
func (c *Client) ConfigureManagementIsolation(ctx context.Context, identity GuestIdentity, management ManagementIsolation) error {
	if !identifier.MatchString(management.Bridge) || net.ParseIP(management.ManagerIP) == nil || (management.Port != "8123" && management.Port != "80" && management.Port != "443") {
		return ErrCapability
	}
	guest, err := c.ownedGuest(ctx, identity)
	if err != nil {
		return err
	}
	if guest.Status != "stopped" || guest.Lock != "" {
		return ErrConflict
	}
	inventory, err := c.Inventory(ctx)
	if err != nil {
		return err
	}
	bridge := false
	for _, network := range inventory.Networks {
		if network.Name == management.Bridge && network.Type == "bridge" && network.Active == 1 {
			bridge = true
		}
	}
	if !bridge {
		return ErrCapability
	}
	for _, path := range []string{"/cluster/firewall/options", c.nodePath() + "/firewall/options"} {
		var options map[string]any
		if err := c.request(ctx, http.MethodGet, path, nil, &options); err != nil {
			return err
		}
		if options["enable"] != float64(1) {
			return errors.New("substrate: existing host firewall enforcement required")
		}
	}
	if existing := stringConfig(guest.Config, "net1"); existing != "" && (!optionEquals(existing, "bridge", management.Bridge) || !optionEquals(existing, "firewall", "1")) {
		return ErrConflict
	}
	if err := c.SetNetworkIsolation(ctx, identity, true); err != nil {
		return err
	}
	if stringConfig(guest.Config, "net1") == "" {
		if err := c.request(ctx, http.MethodPut, c.vmPath(identity.ID)+"/config", url.Values{"net1": {"virtio,bridge=" + management.Bridge + ",firewall=1"}}, nil); err != nil {
			return err
		}
	}
	if err := c.request(ctx, http.MethodPut, c.vmPath(identity.ID)+"/firewall/options", url.Values{"enable": {"1"}, "policy_in": {"DROP"}, "policy_out": {"DROP"}, "dhcp": {"1"}, "ndp": {"0"}, "radv": {"0"}}, nil); err != nil {
		return err
	}
	var rules []map[string]any
	if err := c.request(ctx, http.MethodGet, c.vmPath(identity.ID)+"/firewall/rules", nil, &rules); err != nil {
		return err
	}
	for _, rule := range rules {
		if rule["action"] == "ACCEPT" && rule["type"] == "in" && rule["source"] == management.ManagerIP && rule["dport"] == management.Port {
			return nil
		}
	}
	if len(rules) != 0 {
		return errors.New("substrate: existing guest firewall rules require explicit reconciliation")
	}
	return c.request(ctx, http.MethodPost, c.vmPath(identity.ID)+"/firewall/rules", url.Values{"type": {"in"}, "action": {"ACCEPT"}, "enable": {"1"}, "iface": {"net1"}, "source": {management.ManagerIP}, "proto": {"tcp"}, "dport": {management.Port}, "comment": {"kombify management ingress"}}, nil)
}

type ProductionNetworkPolicy struct {
	AllowedDestinations []string `json:"allowed_destinations"`
}

// ActivateProductionNetwork keeps firewall enforcement and permits only the
// destinations explicitly retained in the approved production policy.
func (c *Client) ActivateProductionNetwork(ctx context.Context, identity GuestIdentity, policy ProductionNetworkPolicy) error {
	guest, err := c.ownedGuest(ctx, identity)
	if err != nil {
		return err
	}
	if guest.Status != "stopped" || guest.Lock != "" || len(policy.AllowedDestinations) == 0 || len(policy.AllowedDestinations) > 16 {
		return ErrConflict
	}
	for _, destination := range policy.AllowedDestinations {
		_, network, err := net.ParseCIDR(destination)
		if err != nil || network.String() != destination {
			return ErrCapability
		}
	}
	for index, destination := range policy.AllowedDestinations {
		var rules []map[string]any
		if err := c.request(ctx, http.MethodGet, c.vmPath(identity.ID)+"/firewall/rules", nil, &rules); err != nil {
			return err
		}
		found := false
		for _, rule := range rules {
			if rule["type"] == "out" && rule["action"] == "ACCEPT" && rule["dest"] == destination && rule["iface"] == "net0" && rule["enable"] == float64(1) {
				found = true
			}
		}
		if !found {
			if err := c.request(ctx, http.MethodPost, c.vmPath(identity.ID)+"/firewall/rules", url.Values{"type": {"out"}, "action": {"ACCEPT"}, "enable": {"1"}, "iface": {"net0"}, "dest": {destination}, "comment": {"kombify approved production " + strconv.Itoa(index)}}, nil); err != nil {
				return err
			}
		}
	}
	return c.SetNetworkIsolation(ctx, identity, false)
}

// VerifyManagementIsolation checks the actual existing Proxmox firewall and
// NIC configuration. The requested management path is never provisioned or
// assumed from a caller boolean. Complex inherited rules are refused until a
// dedicated verified policy supports them.
func (c *Client) VerifyManagementIsolation(ctx context.Context, identity GuestIdentity, management ManagementIsolation) (string, error) {
	if !identifier.MatchString(management.Bridge) || net.ParseIP(management.ManagerIP) == nil || (management.Port != "8123" && management.Port != "80" && management.Port != "443") {
		return "", ErrCapability
	}
	guest, err := c.ownedGuest(ctx, identity)
	if err != nil {
		return "", err
	}
	net0 := stringConfig(guest.Config, "net0")
	net1 := stringConfig(guest.Config, "net1")
	if !optionEquals(net0, "link_down", "1") || !optionEquals(net1, "bridge", management.Bridge) || !optionEquals(net1, "firewall", "1") || optionEquals(net1, "link_down", "1") {
		return "", errors.New("substrate: disconnected production NIC and filtered management NIC required")
	}
	observed := map[string]any{"guest": guest.Config}
	for _, path := range []string{"/cluster/firewall/options", c.nodePath() + "/firewall/options", c.vmPath(identity.ID) + "/firewall/options"} {
		var options map[string]any
		if err := c.request(ctx, http.MethodGet, path, nil, &options); err != nil {
			return "", err
		}
		if options["enable"] != float64(1) {
			return "", errors.New("substrate: firewall enforcement is not enabled at every level")
		}
		if strings.HasPrefix(path, c.vmPath(identity.ID)+"/") && (options["policy_in"] != "DROP" || options["policy_out"] != "DROP") {
			return "", errors.New("substrate: default deny in both directions required")
		}
		observed[path] = options
	}
	allowed := false
	for _, path := range []string{"/cluster/firewall/rules", c.nodePath() + "/firewall/rules", c.vmPath(identity.ID) + "/firewall/rules"} {
		var rules []map[string]any
		if err := c.request(ctx, http.MethodGet, path, nil, &rules); err != nil {
			return "", err
		}
		for _, rule := range rules {
			if rule["enable"] == float64(0) {
				continue
			}
			if rule["action"] == "DROP" || rule["action"] == "REJECT" {
				continue
			}
			if path != c.vmPath(identity.ID)+"/firewall/rules" || rule["type"] != "in" || rule["action"] != "ACCEPT" || rule["source"] != management.ManagerIP || rule["proto"] != "tcp" || rule["dport"] != management.Port || (rule["macro"] != nil && rule["macro"] != "") || (rule["iface"] != nil && rule["iface"] != "net1") {
				return "", errors.New("substrate: firewall contains an unverified traffic allowance")
			}
			allowed = true
		}
		observed[path] = rules
	}
	if !allowed {
		return "", errors.New("substrate: no exact management ingress rule")
	}
	payload, _ := json.Marshal(observed)
	hash := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(hash[:]), nil
}

func stringConfig(config map[string]any, key string) string {
	value, _ := config[key].(string)
	return value
}
func optionEquals(value, key, want string) bool {
	for _, option := range strings.Split(value, ",") {
		if option == key+"="+want {
			return true
		}
	}
	return false
}
