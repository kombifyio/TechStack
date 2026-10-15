package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

func (s *PostgresStore) CommitWizardJoinProjection(ctx context.Context, command StackConfigCAS, run WizardRun) (*Stack, *WizardRun, error) {
	if s == nil || s.db == nil {
		return nil, nil, fmt.Errorf("controlplane: database not configured")
	}
	prepared, err := normalizeStackConfigCAS(command)
	if err != nil {
		return nil, nil, err
	}
	if err = validateWizardProjectionRun(prepared, run); err != nil {
		return nil, nil, err
	}
	var stack *Stack
	var stored *WizardRun
	err = s.withTenant(ctx, prepared.TenantID, func(tx *sql.Tx) error {
		existing, e := scanWizardRun(tx.QueryRowContext(ctx, `SELECT `+wizardRunColumns+` FROM wizard_runs WHERE tenant_id=$1 AND owner_subject_id=$2 AND idempotency_key=$3 FOR UPDATE`, prepared.TenantID, run.OwnerSubjectID, run.IdempotencyKey))
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		replaceExisting := e == nil
		if replaceExisting && !sameWizardProjectionAttempt(existing, run) {
			return ErrConflict
		}
		// Insert/lock the key before CAS. A concurrent key claimant loses rather
		// than overwriting the winner; CAS failure rolls this reservation back too.
		stored, e = upsertWizardRunTx(ctx, tx, run, replaceExisting)
		if e != nil {
			return e
		}
		stack, e = compareAndSwapStackConfigTx(ctx, tx, prepared)
		if e != nil {
			return e
		}
		if stack.OwnerSubjectID != run.OwnerSubjectID {
			return ErrConflict
		}
		return nil
	})
	if err != nil {
		return nil, nil, err
	}
	return stack, stored, nil
}
func validateWizardProjectionRun(command StackConfigCAS, run WizardRun) error {
	if err := validateWizardRun(run); err != nil {
		return err
	}
	if strings.TrimSpace(run.IdempotencyKey) == "" || strings.TrimSpace(run.NodeID) == "" || run.TenantID != command.TenantID || run.StackID != command.StackID || run.Status != "failed" || run.ErrorReason != "projection_pending" {
		return fmt.Errorf("controlplane: matching keyed pending Wizard projection required")
	}
	return nil
}
func sameWizardProjectionAttempt(existing *WizardRun, run WizardRun) bool {
	return existing.ID == run.ID && existing.RequestSHA256 == run.RequestSHA256 && existing.Status != "completed" && (existing.StackID == "" || existing.StackID == run.StackID) && (existing.NodeID == "" || existing.NodeID == run.NodeID)
}
