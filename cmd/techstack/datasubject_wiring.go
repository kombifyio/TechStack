package main

import (
	"crypto/ed25519"
	"encoding/base64"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/datasubject"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// registerDataSubjectRoute mounts the Cloud data-subject participant route. It
// answers 503 unless TECHSTACK_DATASUBJECT_ENABLED, SERVICE_AUTH_SECRET and a
// valid TECHSTACK_DATASUBJECT_SIGNING_KEY are all set; an enabled endpoint with
// a malformed key stays closed and logs why. The subject directory (migration
// 156) resolves opaque refs across tenants; while it is absent an enabled
// endpoint answers blocked receipts (techstack_subject_directory_unwired)
// instead of claiming that nothing is held.
func registerDataSubjectRoute(router *httpx.Router, deps routeDeps) {
	handler := datasubject.Unavailable()
	if envBoolDefault("TECHSTACK_DATASUBJECT_ENABLED", false) {
		seed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(os.Getenv("TECHSTACK_DATASUBJECT_SIGNING_KEY")))
		switch {
		case err != nil || len(seed) != ed25519.SeedSize:
			deps.log.Error("datasubject_disabled", "reason", "TECHSTACK_DATASUBJECT_SIGNING_KEY must be a base64 32-byte Ed25519 seed")
		default:
			var retention time.Duration
			if raw := strings.TrimSpace(os.Getenv("TECHSTACK_DATASUBJECT_EVIDENCE_RETENTION")); raw != "" {
				if d, perr := time.ParseDuration(raw); perr == nil {
					retention = d
				} else {
					deps.log.Error("datasubject_evidence_retention_invalid", "error", perr)
				}
			}
			handler = datasubject.NewHandler(datasubject.Config{
				Enabled:    true,
				Secret:     strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET")),
				SecretNext: strings.TrimSpace(os.Getenv("SERVICE_AUTH_SECRET_NEXT")),
				SigningKey: ed25519.NewKeyFromSeed(seed),
				Executor:   dataSubjectExecutor(deps, retention),
			})
		}
	}
	router.POST(datasubject.Path, func(e *httpx.Event) error {
		handler.ServeHTTP(e.Response, e.Request)
		return nil
	})
}

func dataSubjectExecutor(deps routeDeps, retention time.Duration) datasubject.Executor {
	exec := &datasubject.StoreExecutor{EvidenceRetention: retention}
	if deps.v2 != nil && deps.v2.db != nil && deps.v2.db.DB != nil {
		exec.Store = &datasubject.SQLStore{DB: deps.v2.db.DB}
		// Migration 156 owns the cross-tenant subject directory. Until it is
		// applied the lookup fails and requests answer blocked
		// (techstack_subject_directory_unwired), never "nothing held".
		exec.Directory = &datasubject.SQLDirectory{DB: deps.v2.db.DB}
	}
	return exec
}
