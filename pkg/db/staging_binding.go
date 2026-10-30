package db

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
)

// stagingBindingJSON is the original governed Stage producer's non-secret
// binding. The producer and native attestation consume exactly the same facts.
//
//go:embed staging_binding.json
var stagingBindingJSON []byte

var ErrStagingRuntimeBinding = errors.New("verified Stage runtime database binding is unavailable")

// DeclaresStagingDatabaseBinding identifies only the existing exact physical
// Stage pin. This may tighten startup failure but conveys no live evidence or
// tenant authority; production pins and inherited test-tenant custody do not
// select Stage admission. The normal application-pool pin assertion remains
// mandatory before the separate live provider-role attestation.
func DeclaresStagingDatabaseBinding() bool {
	var binding struct {
		Identity string `json:"identity"`
	}
	if json.Unmarshal(stagingBindingJSON, &binding) != nil || binding.Identity == "" {
		return false
	}
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	return err == nil && pinned && pin.SystemIdentifier != "" &&
		pin.DatabaseName+"@"+pin.SystemIdentifier == binding.Identity
}

// StagingRuntimeBinding is opaque live database identity evidence. Only the
// verifier below can construct a nonzero value; it conveys no tenant authority.
type StagingRuntimeBinding struct{ verified bool }

func (binding *StagingRuntimeBinding) Verified() bool {
	return binding != nil && binding.verified
}

// VerifyStagingRuntimeBinding is used after the existing native provider-role
// posture and same-physical application-pool assertion. It reads the existing
// bounded authority helper through the same restricted pool and positively
// excludes every database outside the one governed Stage binding. No caller
// flag, expected-identity string or bootstrap receipt can manufacture evidence.
func VerifyStagingRuntimeBinding(ctx context.Context, database *sql.DB) (*StagingRuntimeBinding, error) {
	verified, err := verifyStagingDatabaseRole(ctx, database, "TECHSTACK_PROVIDER_CONTROL_RUNTIME_DATABASE_URL")
	if err != nil {
		return nil, err
	}
	if !verified {
		return nil, ErrStagingRuntimeBinding
	}
	return &StagingRuntimeBinding{verified: true}, nil
}

// VerifyStagingApplicationBinding prepares the same Stage policy in the HTTP
// process without opening provider custody. It positively attests the existing
// application pool's physical Stage identity and exact restricted app/session
// role. It grants no owner, worker, provider or background execution authority;
// the ordinary native preflight and genuine sealed owner POST remain required
// by releasechannel.AdmitStageMutation in each process.
func VerifyStagingApplicationBinding(ctx context.Context, database *sql.DB) (*StagingRuntimeBinding, error) {
	verified, err := verifyStagingDatabaseRole(ctx, database, "DATABASE_URL")
	if err != nil {
		return nil, err
	}
	if !verified {
		return nil, ErrStagingRuntimeBinding
	}
	return &StagingRuntimeBinding{verified: true}, nil
}

// IsStagingApplicationDatabase classifies the already-open canonical app pool
// before optional provider composition. A name-only pin is valid elsewhere but
// cannot hide this physical Stage database. The bool can only tighten startup;
// it cannot construct provider attestation or grant tenant execution.
func IsStagingApplicationDatabase(ctx context.Context, database *sql.DB) (bool, error) {
	var binding struct {
		Database string `json:"database"`
		Identity string `json:"identity"`
	}
	if json.Unmarshal(stagingBindingJSON, &binding) != nil || binding.Database == "" ||
		binding.Identity == "" || database == nil {
		return false, ErrStagingRuntimeBinding
	}
	pin, pinned, err := ExpectedDatabaseIdentityFromEnv()
	if err != nil {
		return false, err
	}
	if pinned && pin.SystemIdentifier != "" && pin.DatabaseName+"@"+pin.SystemIdentifier != binding.Identity {
		// Reuse the normal live pin assertion before excluding a physically
		// distinct database; a configured string alone is not evidence.
		if err := VerifyExpectedDatabaseIdentity(ctx, database); err != nil {
			return false, err
		}
		return false, nil
	}
	var databaseName string
	if err := database.QueryRowContext(ctx, "SELECT current_database()").Scan(&databaseName); err != nil {
		return false, ErrStagingRuntimeBinding
	}
	if databaseName != binding.Database {
		return false, nil
	}
	// Same-name/name-only pins cannot distinguish Stage from production.
	// Unknown live identity must never fall through to ordinary admission.
	return verifyStagingDatabaseRole(ctx, database, "DATABASE_URL")
}

func verifyStagingDatabaseRole(ctx context.Context, database *sql.DB, roleKey string) (bool, error) {
	var binding struct {
		Identity string            `json:"identity"`
		Roles    map[string]string `json:"roles"`
	}
	if json.Unmarshal(stagingBindingJSON, &binding) != nil || binding.Identity == "" ||
		binding.Roles[roleKey] == "" || database == nil {
		return false, ErrStagingRuntimeBinding
	}
	tx, err := database.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return false, ErrStagingRuntimeBinding
	}
	defer func() { _ = tx.Rollback() }()
	var systemID, databaseName, role, sessionRole string
	if err := tx.QueryRowContext(ctx, `
		SELECT authority.system_identifier::text, authority.database_name::text,
		       current_user::text, session_user::text
		FROM provider_control_runtime_authority() AS authority
	`).Scan(&systemID, &databaseName, &role, &sessionRole); err != nil {
		return false, ErrStagingRuntimeBinding
	}
	if err := tx.Commit(); err != nil {
		return false, ErrStagingRuntimeBinding
	}
	if databaseName+"@"+systemID != binding.Identity {
		return false, nil
	}
	if role != binding.Roles[roleKey] || sessionRole != role {
		return false, ErrStagingRuntimeBinding
	}
	return true, nil
}
