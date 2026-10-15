package main

import (
	"context"
	"fmt"
	"os"
	"strings"

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
	mode, err := advancedIssuerCustodyMode()
	if err != nil {
		if log != nil {
			log.Error("advanced_issuer_unavailable", "error", err)
		}
		return nil
	}
	if mode == advancedIssuerCustodyApplication {
		// Bridge until the production purpose bindings exist (ix4h.1): the
		// issuer keeps its key in the application pool, as before #3230.
		issuer, err := advancedissuer.Open(ctx, boot.db, auth.GetEncryptor())
		if err != nil {
			if log != nil {
				log.Error("advanced_issuer_unavailable", "error", err)
			}
			return nil
		}
		if log != nil {
			log.Warn("advanced_issuer_application_custody", "issuer_id", issuer.IssuerID(), "key_id", issuer.KeyID(),
				"reason", "no signing custody binding; set "+envAdvancedIssuerCustody+"=bound after governed preparation")
		}
		return issuer
	}
	issuer, custody, err := advancedissuer.OpenBound(ctx, boot.db, auth.GetEncryptor())
	if err != nil {
		if log != nil {
			log.Error("advanced_issuer_unavailable", "error", err)
		}
		return nil
	}
	boot.advancedIssuerCustody = custody
	if log != nil {
		log.Info("advanced_issuer_ready", "issuer_id", issuer.IssuerID(), "key_id", issuer.KeyID())
	}
	return issuer
}

// envAdvancedIssuerCustody selects where the installation signing key lives:
// "bound" requires the separately prepared signing login, "application" keeps
// the key in the application pool. Unset selects bound only when the signing
// binding is configured, so environments without governed preparation keep
// the application custody they already run with.
const envAdvancedIssuerCustody = "TECHSTACK_ADVANCED_ISSUER_CUSTODY"

const (
	advancedIssuerCustodyBound       = "bound"
	advancedIssuerCustodyApplication = "application"
)

func advancedIssuerCustodyMode() (string, error) {
	switch mode := strings.TrimSpace(os.Getenv(envAdvancedIssuerCustody)); mode {
	case advancedIssuerCustodyBound, advancedIssuerCustodyApplication:
		return mode, nil
	case "":
		if strings.TrimSpace(os.Getenv(advancedissuer.EnvKeyCustodyDatabaseURL)) != "" {
			return advancedIssuerCustodyBound, nil
		}
		return advancedIssuerCustodyApplication, nil
	default:
		return "", fmt.Errorf("%s must be bound or application", envAdvancedIssuerCustody)
	}
}
