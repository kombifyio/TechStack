package main

import (
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/guardreach"
	"github.com/kombifyio/techstack/pkg/kombifyme"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/serviceauth"
)

const (
	// guardReachEnabledEnv switches kombify Guard reach on for a connected
	// (SaaS) deployment. It is off by default: reach publishes a server at a
	// kombify.me address, so a rollout is an explicit operator decision.
	guardReachEnabledEnv = "TECHSTACK_GUARD_REACH_ENABLED"
	guardReachGatewayEnv = "TECHSTACK_GUARD_REACH_GATEWAY_URL"
	guardReachSecretEnv  = "TECHSTACK_GATEWAY_CONNECT_SERVICE_SECRET"
	guardReachRelayEnv   = "TECHSTACK_GUARD_REACH_RELAY_URL"
	guardReachPollEnv    = "TECHSTACK_GUARD_REACH_REVOCATION_POLL"
)

// Maintenance authority is independent of optional tunnel publication and its
// kombify.me credential. Missing dedicated custody keeps delegated tools closed.
func bootMaintenanceOriginGateway(log *logger.Logger) *guardreach.Gateway {
	secret := strings.TrimSpace(os.Getenv(guardReachSecretEnv))
	if (secret == "" && !serviceauth.CanSign("")) || (secret != "" && secret == strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET"))) {
		log.Warn("maintenance_origin_disabled", "reason", "dedicated_service_custody_unavailable")
		return nil
	}
	gateway, err := guardreach.NewGateway(guardreach.GatewayConfig{BaseURL: os.Getenv(guardReachGatewayEnv), Secret: secret})
	if err != nil {
		log.Warn("maintenance_origin_disabled", "reason", "gateway_configuration_invalid")
		return nil
	}
	return gateway
}

// bootGuardReach composes the kombify Guard reach service for a connected
// edition. A self-managed installation, a deployment without the explicit
// enable flag and one missing either credential (the service-auth secret for
// Connect, the platform kombify.me key for the relay) run no reach at all;
// Guard enrollment and execution are unaffected either way.
func bootGuardReach(v2 *v2Boot, edition config.Edition, log *logger.Logger) *guardreach.Service {
	if !edition.IsSaaS() || !envBoolDefault(guardReachEnabledEnv, false) {
		return nil
	}
	store := controlPlanePostgresStore(v2)
	if store == nil {
		return nil
	}
	// The Connect routes accept only Techstack's own secret; the shared
	// SERVICE_AUTH_SECRET is never used here, not even as a fallback.
	secret := strings.TrimSpace(os.Getenv(guardReachSecretEnv))
	meConfig := kombifyme.DefaultConfigFromEnv()
	switch {
	case secret == "" && !serviceauth.CanSign(""):
		log.Warn("guard_reach_disabled", "reason", guardReachSecretEnv+" and the Ed25519 service key missing")
		return nil
	case secret != "" && secret == strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET")):
		log.Warn("guard_reach_disabled", "reason", guardReachSecretEnv+" must differ from SERVICE_AUTH_SECRET")
		return nil
	case !strings.HasPrefix(strings.TrimSpace(meConfig.APIKey), "kbi_"):
		log.Warn("guard_reach_disabled", "reason", "platform kombify.me key (KOMBIFY_ME_API_KEY) missing")
		return nil
	}
	reachStore, err := guardreach.NewPostgresStore(v2.db.DB)
	if err != nil {
		log.Warn("guard_reach_disabled", "reason", err.Error())
		return nil
	}
	gateway, err := guardreach.NewGateway(guardreach.GatewayConfig{BaseURL: os.Getenv(guardReachGatewayEnv), Secret: secret})
	if err != nil {
		log.Warn("guard_reach_disabled", "reason", err.Error())
		return nil
	}
	poll, _ := time.ParseDuration(strings.TrimSpace(os.Getenv(guardReachPollEnv)))
	meConfig.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	service, err := guardreach.New(guardreach.Config{
		Store: reachStore, Me: guardreach.NewMe(kombifyme.NewClient(meConfig)), Connect: gateway,
		Identities: guardreach.ControlPlane{Workers: store, Identities: store, Servers: store}, Access: store,
		RelayURL:       os.Getenv(guardReachRelayEnv),
		RevocationPoll: poll, Logger: log.Logger,
	})
	if err != nil {
		log.Warn("guard_reach_disabled", "reason", err.Error())
		return nil
	}
	log.Info("guard_reach_ready")
	return service
}
