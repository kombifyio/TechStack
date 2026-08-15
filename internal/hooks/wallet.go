// Package hooks contains PocketBase lifecycle hooks for business logic.
package hooks

import (
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterWalletHooks registers hooks that enforce wallet ownership and secret encryption.
// H4 Security: Secrets are encrypted at rest using AES-256-GCM when TECHSTACK_ENCRYPTION_KEY is set.
//
// In SaaS mode the encryption key MUST be configured — this returns an error so the process
// refuses to boot with plaintext credential persistence. Self-hosted mode keeps the previous
// opt-in behavior with a warning (operators can still choose to run without encryption).
//
// guards push cyclomatic complexity above the default. Splitting would add four near-identical
// helper functions for trivial branches; keep it inline for readability.
//
//nolint:gocyclo // Four PocketBase lifecycle hooks (create/update/view/list) plus mode + key
func RegisterWalletHooks(app core.App, log *logger.Logger, mode config.DeploymentMode) error {
	encryptor := auth.GetEncryptor()
	if encryptor == nil {
		if mode.IsSaaS() {
			return fmt.Errorf("wallet encryption is required in SaaS mode: set %s to a 32-byte key", auth.EncryptionKeyEnvVar)
		}
		if log != nil {
			log.Warn("wallet_encryption_disabled",
				"message", "Wallet secrets will be stored unencrypted (TECHSTACK_ENCRYPTION_KEY not set)",
				"recommendation", "Set TECHSTACK_ENCRYPTION_KEY to a 32-byte key for production use",
			)
		}
	}

	// Enforce owner_id = request.auth.id on creation and encrypt secrets.
	app.OnRecordCreateRequest("wallet").BindFunc(func(e *core.RecordRequestEvent) error {
		info, err := e.RequestInfo()
		if err == nil && info != nil && info.Auth != nil {
			e.Record.Set("owner_id", info.Auth.Id)
		}

		encryptWalletRecord(e.Record, encryptor)

		return e.Next()
	})

	// Prevent owner_id tampering on update and encrypt secrets.
	app.OnRecordUpdateRequest("wallet").BindFunc(func(e *core.RecordRequestEvent) error {
		info, err := e.RequestInfo()
		if err == nil && info != nil && info.Auth != nil {
			e.Record.Set("owner_id", info.Auth.Id)
		}

		encryptWalletRecord(e.Record, encryptor)

		return e.Next()
	})

	// Detail reads are inventory-only. Secrets are revealed only through the
	// API route that enforces re-authentication and records an audit event.
	app.OnRecordViewRequest("wallet").BindFunc(func(e *core.RecordRequestEvent) error {
		sanitizeWalletRecordForList(e.Record)
		return e.Next()
	})

	// List reads are inventory-only. Secrets stay hidden until the client fetches a
	// single item explicitly for reveal.
	app.OnRecordsListRequest("wallet").BindFunc(func(e *core.RecordsListRequestEvent) error {
		for _, record := range e.Records {
			sanitizeWalletRecordForList(record)
		}
		return e.Next()
	})

	return nil
}

func encryptWalletRecord(record *core.Record, encryptor *auth.SecretEncryptor) {
	encryptWalletField(record, "secret", encryptor)
	encryptWalletField(record, "totp", encryptor)
	applyWalletDefaults(record)
}

func encryptWalletField(record *core.Record, field string, encryptor *auth.SecretEncryptor) {
	value := record.GetString(field)
	if value == "" {
		return
	}
	encrypted, err := auth.EncryptIfNeeded(encryptor, value)
	if err == nil {
		record.Set(field, encrypted)
	}
}

func sanitizeWalletRecordForList(record *core.Record) {
	secret := record.GetString("secret")
	totp := record.GetString("totp")
	record.Set("has_secret", walletFieldPresent(secret))
	record.Set("has_totp", walletFieldPresent(totp))
	record.Set("secret", "")
	record.Set("totp", "")
	applyWalletDefaults(record)
}

func walletFieldPresent(value string) bool {
	return strings.TrimSpace(value) != ""
}

func applyWalletDefaults(record *core.Record) {
	revealable := walletFieldPresent(record.GetString("secret")) ||
		walletFieldPresent(record.GetString("totp")) ||
		record.GetBool("has_secret") ||
		record.GetBool("has_totp")

	if strings.TrimSpace(record.GetString("item_class")) == "" {
		record.Set("item_class", inferWalletItemClass(record, revealable))
	}

	if strings.TrimSpace(record.GetString("source_type")) == "" {
		sourceType := "manual"
		sourceRef := strings.TrimSpace(record.GetString("source_ref"))
		serviceID := strings.TrimSpace(record.GetString("service_id"))
		switch {
		case strings.HasPrefix(serviceID, "system:"):
			sourceType = "system_account"
			if sourceRef == "" {
				record.Set("source_ref", serviceID)
			}
		case serviceID != "":
			sourceType = "service"
			if sourceRef == "" {
				record.Set("source_ref", serviceID)
			}
		case strings.TrimSpace(record.GetString("stack_id")) != "":
			sourceType = "stack"
		}
		record.Set("source_type", sourceType)
	}

	if revealable || record.Get("revealable") == nil {
		record.Set("revealable", revealable)
	}

	if strings.TrimSpace(record.GetString("access_mode")) == "" {
		record.Set("access_mode", inferWalletAccessMode(record, revealable))
	}
}

func inferWalletItemClass(record *core.Record, revealable bool) string {
	accessMode := strings.TrimSpace(record.GetString("access_mode"))
	serviceID := strings.TrimSpace(record.GetString("service_id"))
	hasURL := strings.TrimSpace(record.GetString("url")) != ""
	hasUsername := strings.TrimSpace(record.GetString("username")) != ""

	switch {
	case accessMode == "open":
		return "launch"
	case accessMode == "manage":
		if hasUsername {
			return "user_account"
		}
		return "manual"
	case revealable:
		return "recovery"
	case strings.HasPrefix(serviceID, "system:"):
		return "recovery"
	case hasUsername:
		return "user_account"
	case hasURL:
		return "launch"
	default:
		return "manual"
	}
}

func inferWalletAccessMode(record *core.Record, revealable bool) string {
	itemClass := strings.TrimSpace(record.GetString("item_class"))
	hasURL := strings.TrimSpace(record.GetString("url")) != ""

	switch {
	case itemClass == "launch":
		return "open"
	case itemClass == "user_account":
		return "manage"
	case revealable:
		return "reveal"
	case hasURL:
		return "open"
	default:
		return "reveal"
	}
}
