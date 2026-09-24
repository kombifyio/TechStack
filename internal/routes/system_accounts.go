package routes

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"net/http"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	"github.com/kombifyio/techstack/internal/systemwallet"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

const (
	systemAccountRoleSuperuser = "superuser"
	systemAccountRoleAdmin     = "admin"
	systemAccountRoleDeveloper = "developer"
)

// RegisterSystemAccountRoutes exposes system credential reset endpoints.
func RegisterSystemAccountRoutes(r *httpx.Router, app core.App, walletStore controlplane.WalletStore) {
	h := systemAccountRouteHandlers{app: app, walletStore: walletStore}
	r.POST("/api/v1/system-accounts/{role}/reset", h.reset)
}

type systemAccountRouteHandlers struct {
	app         core.App
	walletStore controlplane.WalletStore
}

func (h systemAccountRouteHandlers) reset(e *httpx.Event) error {
	ownerID, err := requireAuth(e)
	if err != nil {
		return err
	}
	if e.Auth == nil {
		return httpx.Forbidden(e, "System account reset requires a local owner session")
	}
	tenantID, tenantErr := tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, "techstack.system-accounts.reset")
	if tenantErr != nil {
		return tenantErr
	}
	if h.walletStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Canonical wallet custody is not configured", nil)
	}
	if _, err := h.walletStore.ListWalletItems(e.Request.Context(), tenantID, ""); err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Canonical wallet custody is unavailable", nil)
	}

	cfg, ok := systemAccountConfigForRole(e.Request.PathValue("role"))
	if !ok {
		return httpx.BadRequest(e, "invalid role", map[string]any{"role": strings.TrimSpace(e.Request.PathValue("role"))})
	}

	var req struct {
		Confirm string `json:"confirm"`
	}
	if bindErr := e.BindBody(&req); bindErr != nil {
		return httpx.BadRequest(e, "invalid request body", nil)
	}
	if confirmErr := confirmSystemAccountReset(cfg.role, req.Confirm); confirmErr != nil {
		return httpx.BadRequest(e, confirmErr.Error(), nil)
	}
	if !cfg.allowsReset(e.Auth.GetString("email")) {
		return httpx.Forbidden(e, "Not allowed")
	}

	newPassword, err := randomPasswordURLSafe(24)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "failed to generate password", nil)
	}
	if err := setAuthPasswordByEmail(h.app, cfg.collection, cfg.email, newPassword); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, err.Error(), nil)
	}
	if err := systemwallet.UpsertCredential(e.Request.Context(), h.walletStore, tenantID, ownerID, systemwallet.Credential{
		Role: cfg.role, Name: cfg.walletName, Username: cfg.email, Secret: newPassword,
		Notes: "System account recovery credential (issued by kombifyTechstack)",
	}); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, fmt.Sprintf("store in wallet: %v", err), nil)
	}

	h.app.Logger().Info("system account password reset",
		"role", cfg.role,
		"email", cfg.email,
		"owner_id", ownerID,
		"remote_addr", e.Request.RemoteAddr,
	)

	return httpx.Success(e, http.StatusOK, map[string]any{
		"role":            cfg.role,
		"email":           cfg.email,
		"secretStored":    true,
		"walletServiceId": systemwallet.ServiceID(cfg.role),
	})
}

type systemAccountConfig struct {
	role            string
	collection      string
	email           string
	authorizedEmail string
	walletName      string
}

func systemAccountConfigForRole(role string) (systemAccountConfig, bool) {
	adminEmail := envWithDefault("TECHSTACK_ADMIN_EMAIL", "admin@techstack.local")
	devEmail := envWithDefault("TECHSTACK_DEVELOPER_EMAIL", "developer@techstack.local")
	switch strings.TrimSpace(role) {
	case systemAccountRoleSuperuser:
		return systemAccountConfig{
			role:            systemAccountRoleSuperuser,
			collection:      "_superusers",
			email:           envWithDefault("TECHSTACK_SUPERUSER_EMAIL", "superuser@techstack.local"),
			authorizedEmail: adminEmail,
			walletName:      "PocketBase Superuser",
		}, true
	case systemAccountRoleAdmin:
		return systemAccountConfig{
			role:            systemAccountRoleAdmin,
			collection:      "users",
			email:           adminEmail,
			authorizedEmail: adminEmail,
			walletName:      "kombifyTechstack Admin",
		}, true
	case systemAccountRoleDeveloper:
		return systemAccountConfig{
			role:            systemAccountRoleDeveloper,
			collection:      "users",
			email:           devEmail,
			authorizedEmail: devEmail,
			walletName:      "kombifyTechstack Developer",
		}, true
	default:
		return systemAccountConfig{}, false
	}
}

func (cfg systemAccountConfig) allowsReset(requesterEmail string) bool {
	return strings.EqualFold(strings.TrimSpace(requesterEmail), cfg.authorizedEmail)
}

func envWithDefault(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func confirmSystemAccountReset(role, confirmation string) error {
	if strings.TrimSpace(role) == "" {
		return fmt.Errorf("role is required")
	}
	if !strings.EqualFold(strings.TrimSpace(confirmation), role) {
		return fmt.Errorf("reset confirmation must match role %q", role)
	}
	return nil
}

func setAuthPasswordByEmail(app core.App, collectionName, email, password string) error {
	c, err := app.FindCollectionByNameOrId(collectionName)
	if err != nil {
		return fmt.Errorf("find collection %s: %w", collectionName, err)
	}
	rec, err := app.FindAuthRecordByEmail(c, email)
	if err != nil || rec == nil {
		return fmt.Errorf("account not found: %s (%s)", email, collectionName)
	}
	rec.SetPassword(password)
	if err := app.Save(rec); err != nil {
		return fmt.Errorf("save account password: %w", err)
	}
	return nil
}

func randomPasswordURLSafe(length int) (string, error) {
	if length < 16 {
		length = 16
	}
	b := make([]byte, length)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b)[:length], nil
}
