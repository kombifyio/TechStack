package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/discovery"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type wizardRemoteSSHTester interface {
	TestSSH(ctx context.Context, host string, port int, username, password string) error
	TestSSHWithKey(ctx context.Context, host string, port int, username, privateKey string) error
}

type wizardRemoteSSHTestRequest struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	User        string `json:"user"`
	AuthMethod  string `json:"auth_method"`
	Password    string `json:"password,omitempty"`
	SSHKeyLabel string `json:"ssh_key_label,omitempty"`
	PrivateKey  string `json:"private_key,omitempty"`
}

func (h wizardRouteHandlers) testRemoteSSH(e *httpx.Event) error {
	ownerID, _, ok := authenticatedUser(e)
	if !ok {
		return httpx.Unauthorized(e, "Authentication required")
	}
	if !h.nativeV2WizardEnabled(e.Request.Context(), ownerID) {
		return httpx.Error(e, http.StatusForbidden, ksapi.ErrCodeForbidden, "Native v2 wizard is not enabled", map[string]any{
			inventoryReasonCodeField:          "feature_not_enabled",
			"required_features":               []string{nativeV2WizardFeatureKey},
			"missing_features":                []string{nativeV2WizardFeatureKey},
			managedRuntimeDetailsRetryableKey: false,
		})
	}
	if h.cfg.RemoteSSHTester == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Remote SSH testing is not configured", map[string]any{
			inventoryReasonCodeField:          "wizard_remote_ssh_unavailable",
			managedRuntimeDetailsRetryableKey: true,
		})
	}

	req, err := decodeWizardRemoteSSHTestRequest(e.Request.Body)
	if err != nil {
		return httpx.BadRequest(e, "Invalid request body")
	}
	if validationErr := validateWizardRemoteSSHTestRequest(req); validationErr != nil {
		return httpx.BadRequest(e, validationErr.Error())
	}

	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.wizard.remote.test-ssh")
	if tenantErr != nil {
		return tenantErr
	}

	password := strings.TrimSpace(req.Password)
	privateKey := strings.TrimSpace(req.PrivateKey)
	authMethod := normalizeWizardRemoteAuthMethod(req.AuthMethod)

	switch authMethod {
	case "password":
		if password == "" {
			return httpx.BadRequest(e, "Password is required for password authentication")
		}
	case "ssh-key":
		if privateKey == "" && strings.TrimSpace(req.SSHKeyLabel) != "" {
			resolvedKey, resolveErr := h.resolveWalletCredentialSecret(
				e.Request.Context(),
				tenantID,
				ownerID,
				strings.TrimSpace(req.SSHKeyLabel),
				"ssh_key",
			)
			if resolveErr != nil {
				return httpx.BadRequest(e, resolveErr.Error())
			}
			privateKey = resolvedKey
		}
		if privateKey == "" {
			return httpx.BadRequest(e, "Provide an SSH key label that matches a wallet SSH key, or supply a private key for this test")
		}
	default:
		return httpx.BadRequest(e, "Unsupported authentication method")
	}

	var testErr error
	if authMethod == "password" {
		testErr = h.cfg.RemoteSSHTester.TestSSH(
			e.Request.Context(),
			strings.TrimSpace(req.Host),
			req.Port,
			strings.TrimSpace(req.User),
			password,
		)
	} else {
		testErr = h.cfg.RemoteSSHTester.TestSSHWithKey(
			e.Request.Context(),
			strings.TrimSpace(req.Host),
			req.Port,
			strings.TrimSpace(req.User),
			privateKey,
		)
	}
	if testErr != nil {
		return httpx.Success(e, http.StatusOK, map[string]any{
			routeSuccessField: false,
			routeErrorField:   testErr.Error(),
		})
	}
	return httpx.Success(e, http.StatusOK, map[string]any{
		routeSuccessField:   true,
		routeMessageField:   "SSH connection successful",
	})
}

func decodeWizardRemoteSSHTestRequest(body io.Reader) (wizardRemoteSSHTestRequest, error) {
	var req wizardRemoteSSHTestRequest
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		return wizardRemoteSSHTestRequest{}, err
	}
	if req.Port == 0 {
		req.Port = 22
	}
	return req, nil
}

func validateWizardRemoteSSHTestRequest(req wizardRemoteSSHTestRequest) error {
	if strings.TrimSpace(req.Host) == "" {
		return errors.New("host is required")
	}
	if strings.TrimSpace(req.User) == "" {
		return errors.New("user is required")
	}
	if req.Port < 1 || req.Port > 65535 {
		return errors.New("port must be between 1 and 65535")
	}
	return nil
}

func normalizeWizardRemoteAuthMethod(value string) string {
	switch strings.TrimSpace(strings.ToLower(value)) {
	case "password":
		return "password"
	default:
		return "ssh-key"
	}
}

func (h wizardRouteHandlers) resolveWalletCredentialSecret(
	ctx context.Context,
	tenantID, ownerID, label, kind string,
) (string, error) {
	if h.cfg.Wallet == nil {
		return "", errors.New("Wallet is not configured; store the SSH key in your wallet before testing by label")
	}
	label = strings.TrimSpace(label)
	if label == "" {
		return "", errors.New("SSH key label is required")
	}
	items, err := h.cfg.Wallet.ListWalletItems(ctx, tenantID, "")
	if err != nil {
		return "", errors.New("Could not load wallet credentials for SSH key lookup")
	}
	for _, item := range items {
		if !walletItemOwnedBy(item.Metadata, ownerID) {
			continue
		}
		name := strings.TrimSpace(walletString(item.Metadata["name"]))
		if !strings.EqualFold(name, label) {
			continue
		}
		itemKind := strings.TrimSpace(firstNonEmptyWallet(
			walletString(item.Metadata["kind"]),
			item.ItemType,
		))
		if itemKind != kind {
			continue
		}
		secret, revealErr := walletRevealField(walletString(item.Metadata["secret"]))
		if revealErr != nil {
			return "", errors.New("Could not decrypt the matching wallet credential")
		}
		secret = strings.TrimSpace(secret)
		if secret == "" {
			return "", errors.New("The matching wallet credential has no secret material")
		}
		return secret, nil
	}
	return "", errors.New("No wallet credential matched the SSH key label")
}

// discoveryRemoteSSHAdapter exposes key-based and password-based probes from
// the shared discovery service without LAN ownership checks.
type discoveryRemoteSSHAdapter struct {
	service *discovery.Service
}

func NewDiscoveryRemoteSSHAdapter(service *discovery.Service) wizardRemoteSSHTester {
	if service == nil {
		return nil
	}
	return discoveryRemoteSSHAdapter{service: service}
}

func (a discoveryRemoteSSHAdapter) TestSSH(ctx context.Context, host string, port int, username, password string) error {
	return a.service.TestSSH(ctx, host, port, username, password)
}

func (a discoveryRemoteSSHAdapter) TestSSHWithKey(ctx context.Context, host string, port int, username, privateKey string) error {
	return a.service.TestSSHWithKey(ctx, host, port, username, privateKey)
}

var _ wizardRemoteSSHTester = discoveryRemoteSSHAdapter{}
