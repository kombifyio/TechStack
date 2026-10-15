package controlplane

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

const wizardPairingJobColumns = `id, tenant_id, instance_id, stack_id, type, state, priority, progress,
 step, message, error, error_details, logs_json::text, result_json::text, payload_json::text,
 scheduled_for, started_at, completed_at, created_at, updated_at`

// Reserve commits the established run/job identity before token preparation.
// The second transaction locks that same row and commits token + result together.
func (s *PostgresStore) PrepareWizardPairing(ctx context.Context, run WizardRun, requested UpsertJobRequest, prepare func(WorkerStore) (map[string]any, error)) (*Job, bool, error) {
	if s == nil || s.db == nil || prepare == nil {
		return nil, false, fmt.Errorf("controlplane: pairing preparation is unavailable")
	}
	if err := validateWizardRun(run); err != nil {
		return nil, false, err
	}
	if run.IdempotencyKey == "" || run.NodeID == "" || requested.ID == "" || requested.TenantID != run.TenantID || requested.StackID != run.StackID || (requested.Type != "update" && requested.Type != "remote_enrollment") {
		return nil, false, fmt.Errorf("controlplane: matching keyed pairing operation required")
	}
	jobID := ""
	err := s.withTenant(ctx, run.TenantID, func(tx *sql.Tx) error {
		existing, e := scanWizardRun(tx.QueryRowContext(ctx, `SELECT `+wizardRunColumns+` FROM wizard_runs WHERE tenant_id=$1 AND owner_subject_id=$2 AND idempotency_key=$3 FOR UPDATE`, run.TenantID, run.OwnerSubjectID, run.IdempotencyKey))
		if errors.Is(e, sql.ErrNoRows) {
			existing, e = upsertWizardRunTx(ctx, tx, run, false)
		}
		if e != nil {
			return e
		}
		if existing.ID != run.ID || existing.RequestSHA256 != run.RequestSHA256 || existing.StackID != run.StackID || existing.NodeID != run.NodeID {
			return ErrConflict
		}
		jobID = existing.PairingJobID
		if jobID == "" {
			jobID = requested.ID
			result, e := marshalObject(requested.Result)
			if e != nil {
				return e
			}
			payload, e := marshalObject(requested.Payload)
			if e != nil {
				return e
			}
			// A preparation placeholder is failed, so boot recovery cannot execute it
			// before the capability/result transaction finishes. Same-key recovery owns it.
			_, e = tx.ExecContext(ctx, `INSERT INTO jobs(id,tenant_id,stack_id,type,state,step,result_json,payload_json) VALUES($1,$2,$3,$4,'failed','pairing_prepare',$5::jsonb,$6::jsonb)`, jobID, run.TenantID, run.StackID, requested.Type, result, payload)
			if e != nil {
				return e
			}
			_, e = tx.ExecContext(ctx, `UPDATE wizard_runs SET pairing_job_id=$3,updated_at=now() WHERE tenant_id=$1 AND id=$2`, run.TenantID, existing.ID, jobID)
			return e
		}
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	var out *Job
	prepared := false
	err = s.withTenant(ctx, run.TenantID, func(tx *sql.Tx) error {
		job, e := scanJobWithPayload(tx.QueryRowContext(ctx, `SELECT `+wizardPairingJobColumns+` FROM jobs WHERE tenant_id=$1 AND id=$2 FOR UPDATE`, run.TenantID, jobID))
		if e != nil {
			return e
		}
		if job.StackID != run.StackID || job.Type != requested.Type {
			return ErrConflict
		}
		if job.Step != "pairing_prepare" {
			out = job
			return nil
		}
		result, e := prepare(wizardPairingWorkerTx{WorkerStore: s, tx: tx, tenant: run.TenantID, owner: run.OwnerSubjectID, stack: run.StackID})
		if e != nil {
			return e
		}
		raw, e := marshalObject(result)
		if e != nil {
			return e
		}
		state, step, progress, message := "completed", "create_spec", 100, "Server registration prepared"
		if job.Type == "remote_enrollment" {
			state, step, progress, message = "pending", "remote_ssh_connect", 5, "Connecting to your Node over SSH…"
		}
		out, e = scanJobWithPayload(tx.QueryRowContext(ctx, `UPDATE jobs SET state=$3,step=$4,progress=$5,message=$6,result_json=$7::jsonb,updated_at=now() WHERE tenant_id=$1 AND id=$2 RETURNING `+wizardPairingJobColumns, run.TenantID, jobID, state, step, progress, message, raw))
		if e != nil {
			return e
		}
		prepared = true
		return nil
	})
	if err != nil {
		return nil, false, err
	}
	return out, prepared, nil
}

type wizardPairingWorkerTx struct {
	WorkerStore
	tx                   *sql.Tx
	tenant, owner, stack string
}

func (w wizardPairingWorkerTx) UpsertPairingToken(ctx context.Context, token PairingToken) (*PairingToken, error) {
	if strings.TrimSpace(token.TenantID) != w.tenant || token.OwnerSubjectID != w.owner || token.StackID != w.stack {
		return nil, ErrConflict
	}
	return upsertPairingTokenTx(ctx, w.tx, token)
}
