package main

import (
	"log/slog"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/agent/journal"
)

// decommissionRevokedGuard ends the local state of a server whose identity the
// control plane revoked (detach, retire or an owner's revoke), as the Guard data
// contract promises: the identity key, the worker credential in the enrollment
// files and a persisted pairing capability are destroyed, the telemetry spool is
// removed, and the journal drops everything the control plane acknowledged.
// Command evidence that was never acknowledged is the one thing kept, until it
// is acknowledged or handed off; the log line says how much remains. A Guard
// that holds no secrets idles; a new pairing capability starts over. It is safe
// to repeat and runs again on every start of a revoked Guard.
func decommissionRevokedGuard(manager *hostidentity.Manager, cfg *agentModeConfig, log *slog.Logger) {
	if manager == nil {
		return
	}
	paths := []string{cfg.enrollmentFile}
	if err := manager.Decommission(paths...); err != nil {
		log.Warn("guard_decommission_secrets_incomplete", "error", err.Error())
	}
	root, err := manager.OpenCustodyRoot()
	if err != nil {
		log.Warn("guard_decommission_custody_unavailable", "error", err.Error())
		return
	}
	defer root.Close()
	if err := root.RemoveAll("telemetry-spool"); err != nil {
		log.Warn("guard_decommission_spool_incomplete", "error", err.Error())
	}
	store, err := journal.OpenRoot(root, journal.Options{})
	if err != nil {
		log.Warn("guard_decommission_journal_unavailable", "error", err.Error())
		return
	}
	kept, err := store.Decommission()
	_ = store.Close()
	switch {
	case err != nil:
		log.Warn("guard_decommission_journal_incomplete", "error", err.Error(), "unacknowledged_entries", kept)
	case kept > 0:
		log.Warn("guard_decommission_journal_retained", "unacknowledged_entries", kept,
			"guidance", "these command receipts were never acknowledged by the control plane; keep or export the journal in the Guard state directory, then delete it")
	default:
		log.Info("guard_decommissioned", "journal", "removed")
	}
	if kept == 0 && err == nil {
		_ = root.Remove("journal.lock")
	}
}
