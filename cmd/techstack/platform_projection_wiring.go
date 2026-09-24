package main

import (
	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/serverregistry"
)

const (
	// The platform origin that owns kombify-db and performs the write. Unset
	// means no projection, which is the self-hosted and local default.
	envPlatformProjectionOrigin   = "TECHSTACK_PLATFORM_PROJECTION_URL"
	envPlatformProjectionInterval = "TECHSTACK_PLATFORM_PROJECTION_INTERVAL"
)

// bootPlatformServerProjector composes the worker that publishes this service's
// server inventory into the platform database, so every kombify product can
// read one persisted server list instead of calling Techstack and inheriting
// its inventory authorization (ADR-038; owner direction 2026-09-18).
//
// It is optional by construction: a deployment without the platform origin
// simply does not project, and a self-hosted install never does.
func bootPlatformServerProjector(boot *v2Boot, log *logger.Logger) *serverregistry.PlatformProjector {
	if boot == nil || boot.db == nil || boot.db.DB == nil {
		return nil
	}
	origin := v2FirstEnv(envPlatformProjectionOrigin)
	if origin == "" {
		return nil
	}
	secret, _, err := servicecall.LoadKeysFromEnv()
	if err != nil || secret == "" {
		log.Warn("platform_projection_disabled", "reason", "service_auth_secret_missing")
		return nil
	}
	publisher, err := routes.NewPlatformPublisher(routes.PlatformPublisherConfig{
		CloudOrigin: origin,
		Secret:      secret,
	})
	if err != nil || publisher == nil {
		log.Error("platform_projection_publisher_invalid", "error", err)
		return nil
	}
	projector, err := serverregistry.NewPlatformProjector(serverregistry.PlatformProjectorConfig{
		Source:    routes.PlatformProjectionSource{Store: controlPlanePostgresStore(boot)},
		Publisher: publisher,
		Interval:  durationFromEnv(envPlatformProjectionInterval, 0),
		Logger:    log.Logger,
	})
	if err != nil {
		log.Error("platform_projection_config_invalid", "error", err)
		return nil
	}
	log.Info("platform_projection_enabled", "origin", origin)
	return projector
}
