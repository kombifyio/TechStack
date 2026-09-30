package main

import (
	"context"

	"github.com/kombifyio/techstack/internal/advancedissuer"
	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/logger"
)

// advancedCapabilityIssuer opens the installation's Advanced issuer. Without
// it every managed rollout fails closed at the trust-import step, because a
// Techstack rollout without Advanced Mode is a defect; the boot continues so
// the rest of the control plane stays available.
func advancedCapabilityIssuer(ctx context.Context, boot *v2Boot, log *logger.Logger) jobs.AdvancedIssuer {
	if boot == nil || boot.db == nil {
		return nil
	}
	issuer, err := advancedissuer.Open(ctx, boot.db, auth.GetEncryptor())
	if err != nil {
		if log != nil {
			log.Error("advanced_issuer_unavailable", "error", err)
		}
		return nil
	}
	if log != nil {
		log.Info("advanced_issuer_ready", "issuer_id", issuer.IssuerID(), "key_id", issuer.KeyID())
	}
	return issuer
}
