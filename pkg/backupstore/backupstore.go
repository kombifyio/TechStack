// Package backupstore provisions and destroys per-stack kombify-managed R2
// backup stores: one bucket `skbk-<stack_id>` plus one bucket-scoped
// Cloudflare API token whose S3 credentials are handed to the node by value
// inside the backup runtime action (see addons/backup kombify-r2 posture in
// kombify-StackKits — credentials never persist in specs or artifacts).
//
// S3 credential derivation follows the R2 authentication contract
// (developers.cloudflare.com/r2/api/tokens): Access Key ID = API token id,
// Secret Access Key = SHA-256 hex of the API token value.
package backupstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
)

const (
	defaultBaseURL = "https://api.cloudflare.com/client/v4"

	// bucketPrefix is the release-defined naming scheme for managed backup
	// buckets (photo-vault plan decision 2026-07-08).
	bucketPrefix = "skbk-"

	permissionGroupItemRead  = "Workers R2 Storage Bucket Item Read"
	permissionGroupItemWrite = "Workers R2 Storage Bucket Item Write"
)

// Config carries the Cloudflare control credentials used to provision
// per-stack stores. These are platform credentials (provider-sourced env),
// distinct from the per-stack tokens this package creates.
type Config struct {
	AccountID string
	// APIToken must be allowed to manage R2 buckets and account API tokens.
	APIToken string
	// BaseURL overrides the Cloudflare API endpoint in tests.
	BaseURL string
	// HTTPClient overrides the default client in tests.
	HTTPClient *http.Client
}

// FromEnv resolves the provisioning configuration. Callers treat an error as
// "managed backup store unavailable" and fail closed at the entitlement gate.
func FromEnv() (Config, error) {
	accountID := strings.TrimSpace(os.Getenv("CLOUDFLARE_ACCOUNT_ID"))
	token := strings.TrimSpace(os.Getenv("TECHSTACK_BACKUPSTORE_CF_API_TOKEN"))
	if accountID == "" || token == "" {
		return Config{}, fmt.Errorf("backupstore requires CLOUDFLARE_ACCOUNT_ID and TECHSTACK_BACKUPSTORE_CF_API_TOKEN")
	}
	return Config{AccountID: accountID, APIToken: token}, nil
}

// Store is the provisioning result. SecretAccessKey travels by value only to
// the encrypted custody repository and, later, the authenticated operations
// boundary. It must never be logged or projected into StackKits documents.
type Store struct {
	Bucket          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	TokenID         string
}

// Client talks to the Cloudflare REST API.
type Client struct {
	cfg  Config
	http *http.Client
	log  *logger.Logger
}

func NewClient(cfg Config) (*Client, error) {
	if cfg.AccountID == "" || cfg.APIToken == "" {
		return nil, fmt.Errorf("backupstore client requires account id and api token")
	}
	if cfg.BaseURL == "" {
		cfg.BaseURL = defaultBaseURL
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: httpClient, log: logger.Get().WithComponent("backupstore")}, nil
}

var bucketNameSanitizer = regexp.MustCompile(`[^a-z0-9-]`)

// BucketName derives the managed bucket name for a stack. R2 bucket names
// are lowercase alphanumerics and dashes, max 63 characters.
func BucketName(stackID string) string {
	name := bucketPrefix + bucketNameSanitizer.ReplaceAllString(strings.ToLower(strings.TrimSpace(stackID)), "-")
	if len(name) > 63 {
		name = name[:63]
	}
	return strings.TrimRight(name, "-")
}

// S3Endpoint returns the account's S3-compatible R2 endpoint.
func (c *Client) S3Endpoint() string {
	return fmt.Sprintf("https://%s.r2.cloudflarestorage.com", c.cfg.AccountID)
}

// Provision ensures the stack's bucket exists and mints a fresh bucket-scoped
// token. Re-provisioning issues a new token; callers revoke the previous one
// via its persisted TokenID when rotating.
func (c *Client) Provision(ctx context.Context, stackID string) (*Store, error) {
	bucket := BucketName(stackID)
	if err := c.ensureBucket(ctx, bucket); err != nil {
		return nil, err
	}
	readID, writeID, err := c.resolvePermissionGroups(ctx)
	if err != nil {
		return nil, err
	}
	tokenID, tokenValue, err := c.createBucketToken(ctx, bucket, readID, writeID)
	if err != nil {
		return nil, err
	}
	secret := sha256.Sum256([]byte(tokenValue))
	c.log.Info("Provisioned managed backup store", "bucket", bucket, "token_id", tokenID)
	return &Store{
		Bucket:          bucket,
		Endpoint:        c.S3Endpoint(),
		AccessKeyID:     tokenID,
		SecretAccessKey: hex.EncodeToString(secret[:]),
		TokenID:         tokenID,
	}, nil
}

// RevokeToken revokes a freshly minted per-stack token. It is used to
// compensate when durable encrypted custody fails after provisioning. It does
// not delete the bucket, which may predate the failed attempt and contain data.
func (c *Client) RevokeToken(ctx context.Context, tokenID string) error {
	tokenID = strings.TrimSpace(tokenID)
	if tokenID == "" {
		return nil
	}
	return c.deleteToken(ctx, tokenID)
}

// Wipe destroys the control-plane side of a stack's backup store: all
// objects (via the purger), the bucket, and the scoped token. It runs LAST
// in the deletion sequence so it also covers the VM-already-gone case.
func (c *Client) Wipe(ctx context.Context, stackID, tokenID string, purger ObjectPurger) error {
	bucket := BucketName(stackID)
	if purger != nil {
		if err := purger.PurgeBucket(ctx, bucket); err != nil {
			return fmt.Errorf("purge objects in %s: %w", bucket, err)
		}
	}
	if err := c.deleteBucket(ctx, bucket); err != nil {
		return err
	}
	if tokenID != "" {
		if err := c.deleteToken(ctx, tokenID); err != nil {
			return err
		}
	}
	c.log.Info("Wiped managed backup store", "bucket", bucket, "token_id", tokenID)
	return nil
}

// ObjectPurger empties a bucket before deletion (R2 refuses to delete
// non-empty buckets). Implemented by the S3 purger; faked in tests.
type ObjectPurger interface {
	PurgeBucket(ctx context.Context, bucket string) error
}

type cfEnvelope struct {
	Success bool `json:"success"`
	Errors  []struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"errors"`
	Result json.RawMessage `json:"result"`
}

func (c *Client) do(ctx context.Context, method, path string, body any) (*cfEnvelope, int, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.APIToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, resp.StatusCode, err
	}
	var envelope cfEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, resp.StatusCode, fmt.Errorf("cloudflare %s %s: HTTP %d with non-JSON body", method, path, resp.StatusCode)
	}
	return &envelope, resp.StatusCode, nil
}

func (envelope *cfEnvelope) errorText() string {
	parts := make([]string, 0, len(envelope.Errors))
	for _, cfErr := range envelope.Errors {
		parts = append(parts, fmt.Sprintf("%d: %s", cfErr.Code, cfErr.Message))
	}
	return strings.Join(parts, "; ")
}

func (c *Client) ensureBucket(ctx context.Context, bucket string) error {
	envelope, status, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/accounts/%s/r2/buckets", c.cfg.AccountID),
		map[string]string{"name": bucket})
	if err != nil {
		return fmt.Errorf("create bucket %s: %w", bucket, err)
	}
	if envelope.Success {
		return nil
	}
	if strings.Contains(strings.ToLower(envelope.errorText()), "already exists") {
		return nil
	}
	return fmt.Errorf("create bucket %s: HTTP %d: %s", bucket, status, envelope.errorText())
}

func (c *Client) deleteBucket(ctx context.Context, bucket string) error {
	envelope, status, err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/accounts/%s/r2/buckets/%s", c.cfg.AccountID, bucket), nil)
	if err != nil {
		return fmt.Errorf("delete bucket %s: %w", bucket, err)
	}
	if envelope.Success || status == http.StatusNotFound {
		return nil
	}
	if strings.Contains(strings.ToLower(envelope.errorText()), "not exist") ||
		strings.Contains(strings.ToLower(envelope.errorText()), "not found") {
		return nil
	}
	return fmt.Errorf("delete bucket %s: HTTP %d: %s", bucket, status, envelope.errorText())
}

func (c *Client) resolvePermissionGroups(ctx context.Context) (readID, writeID string, err error) {
	envelope, status, err := c.do(ctx, http.MethodGet,
		fmt.Sprintf("/accounts/%s/tokens/permission_groups", c.cfg.AccountID), nil)
	if err != nil {
		return "", "", fmt.Errorf("list permission groups: %w", err)
	}
	if !envelope.Success {
		return "", "", fmt.Errorf("list permission groups: HTTP %d: %s", status, envelope.errorText())
	}
	var groups []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(envelope.Result, &groups); err != nil {
		return "", "", fmt.Errorf("parse permission groups: %w", err)
	}
	for _, group := range groups {
		switch group.Name {
		case permissionGroupItemRead:
			readID = group.ID
		case permissionGroupItemWrite:
			writeID = group.ID
		}
	}
	if readID == "" || writeID == "" {
		return "", "", fmt.Errorf("R2 bucket item permission groups not found (read=%q write=%q)", readID, writeID)
	}
	return readID, writeID, nil
}

func (c *Client) createBucketToken(ctx context.Context, bucket, readID, writeID string) (tokenID, tokenValue string, err error) {
	// Bucket resource shape per developers.cloudflare.com/r2/api/tokens:
	// com.cloudflare.edge.r2.bucket.<ACCOUNT_ID>_<JURISDICTION>_<BUCKET>.
	resource := fmt.Sprintf("com.cloudflare.edge.r2.bucket.%s_default_%s", c.cfg.AccountID, bucket)
	payload := map[string]any{
		"name": "techstack-backup-" + bucket,
		"policies": []map[string]any{{
			"effect":    "allow",
			"resources": map[string]string{resource: "*"},
			"permission_groups": []map[string]string{
				{"id": readID},
				{"id": writeID},
			},
		}},
	}
	envelope, status, err := c.do(ctx, http.MethodPost,
		fmt.Sprintf("/accounts/%s/tokens", c.cfg.AccountID), payload)
	if err != nil {
		return "", "", fmt.Errorf("create bucket token: %w", err)
	}
	if !envelope.Success {
		return "", "", fmt.Errorf("create bucket token: HTTP %d: %s", status, envelope.errorText())
	}
	var result struct {
		ID    string `json:"id"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(envelope.Result, &result); err != nil {
		return "", "", fmt.Errorf("parse bucket token: %w", err)
	}
	if result.ID == "" || result.Value == "" {
		return "", "", fmt.Errorf("bucket token response missing id or value")
	}
	return result.ID, result.Value, nil
}

func (c *Client) deleteToken(ctx context.Context, tokenID string) error {
	envelope, status, err := c.do(ctx, http.MethodDelete,
		fmt.Sprintf("/accounts/%s/tokens/%s", c.cfg.AccountID, tokenID), nil)
	if err != nil {
		return fmt.Errorf("delete token %s: %w", tokenID, err)
	}
	if envelope.Success || status == http.StatusNotFound {
		return nil
	}
	return fmt.Errorf("delete token %s: HTTP %d: %s", tokenID, status, envelope.errorText())
}
