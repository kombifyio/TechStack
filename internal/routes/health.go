// Package routes provides custom HTTP routes for kombifyTechstack API.
// These extend PocketBase's built-in collection routes.
package routes

import (
	"context"
	"net/http"
	"regexp"
	"runtime"
	"strings"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/pocketbase/pocketbase/core"
)

var fullHealthRevisionPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// HealthDependencies holds optional dependencies for health checks.
// This allows readiness probes to verify critical services.
type HealthDependencies struct {
	// Edition is the boot-time product lane used for release smoke checks.
	Edition config.Edition
	// DeploymentMode is the derived legacy deployment mode.
	DeploymentMode config.DeploymentMode
	// GRPCRunning is a function that returns true if gRPC server is running
	GRPCRunning func() bool
	// DB is the control-plane Postgres connection. When set, readiness and
	// startup probes report Postgres connectivity, schema version, and applied
	// migration count (PocketBase retirement: Postgres is the source of truth).
	DB *db.DB
}

// dbHealthStatus captures control-plane Postgres connectivity and schema state.
type dbHealthStatus struct {
	Connected         bool   `json:"connected"`
	Backend           string `json:"backend,omitempty"`
	SchemaVersion     string `json:"schema_version,omitempty"`
	MigrationsApplied int    `json:"migrations_applied"`
	Error             string `json:"error,omitempty"`
}

// controlPlaneDBHealth pings the control-plane Postgres database and reads the
// latest applied schema migration version + count from schema_migrations.
func controlPlaneDBHealth(database *db.DB) dbHealthStatus {
	status := dbHealthStatus{}
	if database == nil || database.DB == nil {
		status.Error = "control-plane database not configured"
		return status
	}
	status.Backend = string(database.Backend())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	if err := database.PingContext(ctx); err != nil {
		status.Error = err.Error()
		return status
	}
	status.Connected = true

	// schema_migrations is created/populated by pkg/db.(*DB).Migrate. version is a
	// TEXT primary key (the migration filename); the lexically greatest value is
	// the latest applied migration.
	var count int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
		status.Error = err.Error()
		return status
	}
	status.MigrationsApplied = count

	var latest string
	if err := database.QueryRowContext(ctx, "SELECT COALESCE(MAX(version), '') FROM schema_migrations").Scan(&latest); err != nil {
		status.Error = err.Error()
		return status
	}
	status.SchemaVersion = latest
	return status
}

// RegisterHealthRoutesWithDeps adds health endpoints with dependency checks.
// This is the preferred method for production deployments with Kubernetes.
func RegisterHealthRoutesWithDeps(r *httpx.Router, app core.App, version string, buildRevision string, startTime time.Time, deps *HealthDependencies) {
	// Main health endpoint (backwards compatible)
	r.GET("/api/v1/health", func(e *httpx.Event) error {
		now := time.Now().UTC().Format(time.RFC3339)
		edition, mode := runtimeIdentityFields(deps)
		payload := map[string]any{
			"status":          "healthy",
			"version":         version,
			"service":         "kombify-techstack",
			"timestamp":       now,
			"time":            now, // backwards compat
			"edition":         string(edition),
			"deployment_mode": string(mode),
		}
		if revision := normalizedBuildRevision(buildRevision); revision != "" {
			payload["revision"] = revision
		}
		return httpx.Success(e, http.StatusOK, payload)
	})

	// Kubernetes Liveness Probe: GET /api/v1/health/live
	// Returns 200 if the process is running. Fast, simple check.
	// Does NOT check database or other dependencies.
	r.GET("/api/v1/health/live", func(e *httpx.Event) error {
		return httpx.Success(e, http.StatusOK, map[string]string{"status": "alive"})
	})

	// Kubernetes Readiness Probe: GET /api/v1/health/ready
	// Returns 200 if the service can handle requests.
	// Checks database connectivity and critical dependencies.
	r.GET("/api/v1/health/ready", func(e *httpx.Event) error {
		checks := make(map[string]bool)

		// Control-plane Postgres connectivity is the authoritative readiness gate
		// (PocketBase retirement). Report schema version + migration status too.
		var dbStatus dbHealthStatus
		if deps != nil && deps.DB != nil {
			dbStatus = controlPlaneDBHealth(deps.DB)
			checks["database"] = dbStatus.Connected
		} else {
			// Coexistence fallback: verify the embedded PocketBase data layer.
			checks["database"] = checkDatabase(app)
		}

		// Check gRPC only when the deployment configured that listener as a
		// critical dependency. SaaS may deliberately use the HTTPS Guard path.
		if deps != nil && deps.GRPCRunning != nil {
			checks["grpc"] = deps.GRPCRunning()
		}

		// Determine overall readiness
		allReady := true
		for _, ready := range checks {
			if !ready {
				allReady = false
				break
			}
		}

		status := "ready"
		statusCode := http.StatusOK
		if !allReady {
			status = "not_ready"
			statusCode = http.StatusServiceUnavailable
		}

		payload := map[string]any{
			"status": status,
			"checks": checks,
		}
		if deps != nil && deps.DB != nil {
			payload["database_detail"] = dbStatus
		}
		return httpx.Success(e, statusCode, payload)
	})

	// Kubernetes Startup Probe: GET /api/v1/health/startup
	// Returns 200 if the service has finished initializing.
	// Use this for slow-starting containers to avoid premature restarts.
	r.GET("/api/v1/health/startup", func(e *httpx.Event) error {
		// Service is "started" once the control-plane Postgres database is
		// reachable and migrations have been applied. During the coexistence
		// window fall back to the embedded PocketBase data layer when no DB is
		// wired in.
		if deps != nil && deps.DB != nil {
			dbStatus := controlPlaneDBHealth(deps.DB)
			if !dbStatus.Connected {
				return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service still starting", map[string]any{"database": dbStatus})
			}
			return httpx.Success(e, http.StatusOK, map[string]any{"status": "started", "database": dbStatus})
		}
		if !checkDatabase(app) {
			return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Service still starting", nil)
		}
		return httpx.Success(e, http.StatusOK, map[string]string{"status": "started"})
	})
}

// checkDatabase verifies database connectivity by executing a simple query.
func checkDatabase(app core.App) bool {
	// Use a short timeout context for the health check
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Try to query a simple system operation via PocketBase
	// We'll check if we can access any collection (the collections themselves)
	done := make(chan bool, 1)
	go func() {
		// PocketBase stores collections in memory, but the underlying SQLite
		// connection is what we want to verify. We can use FindCollectionByNameOrId
		// which will access the database.
		_, err := app.FindCollectionByNameOrId("_superusers")
		done <- (err == nil)
	}()

	select {
	case result := <-done:
		return result
	case <-ctx.Done():
		return false // Timeout means database is not responsive
	}
}

// RegisterInfoRoutes adds the /api/v1/info endpoint. environment is the
// runtime posture (development, staging, production, local).
func RegisterInfoRoutes(r *httpx.Router, app core.App, version string, buildRevision string, startTime time.Time, edition config.Edition, mode config.DeploymentMode, environment string) {
	r.GET("/api/v1/info", func(e *httpx.Event) error {
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		edition, mode := normalizeRuntimeIdentity(edition, mode)

		payload := map[string]any{
			"service":         "techstack",
			"version":         version,
			"go_version":      runtime.Version(),
			"uptime_secs":     time.Since(startTime).Seconds(),
			"memory_mb":       m.Alloc / 1024 / 1024,
			"goroutines":      runtime.NumGoroutine(),
			"pocketbase":      true,
			"edition":         string(edition),
			"deployment_mode": string(mode),
			"environment":     environment,
		}
		if revision := normalizedBuildRevision(buildRevision); revision != "" {
			payload["revision"] = revision
		}
		return httpx.Success(e, http.StatusOK, payload)
	})
}

func runtimeIdentityFields(deps *HealthDependencies) (config.Edition, config.DeploymentMode) {
	if deps == nil {
		return normalizeRuntimeIdentity("", "")
	}
	return normalizeRuntimeIdentity(deps.Edition, deps.DeploymentMode)
}

func normalizeRuntimeIdentity(edition config.Edition, mode config.DeploymentMode) (config.Edition, config.DeploymentMode) {
	if edition.IsValid() {
		return edition, edition.DeploymentMode()
	}
	if mode.IsSaaS() {
		return config.EditionSaaSStandalone, config.ModeSaaS
	}
	return config.EditionSelfHostOSS, config.ModeSelfHosted
}

func normalizedBuildRevision(buildRevision string) string {
	revision := strings.ToLower(strings.TrimSpace(buildRevision))
	if fullHealthRevisionPattern.MatchString(revision) {
		return revision
	}
	return ""
}
