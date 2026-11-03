package actions

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"time"
)

// RunIssuedLeaseReaper resumes both secret-free due indexes after restart.
// Each deletion remains fenced by exact tenant, identity and database clock.
func RunIssuedLeaseReaper(ctx context.Context, db *sql.DB) {
	if db == nil {
		return
	}
	pass := func() {
		if err := reapIssuedLeases(ctx, db); err != nil && ctx.Err() == nil {
			slog.Error("ril_issued_lease_reaper_failed", "error", err)
		}
		if err := reapQueuedProofs(ctx, db); err != nil && ctx.Err() == nil {
			slog.Error("ril_queued_proof_reaper_failed", "error", err)
		}
	}
	pass()
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass()
		}
	}
}
func reapIssuedLeases(ctx context.Context, db *sql.DB) error {
	return reapRILExpiry(ctx, db, `SELECT tenant_id,card_id FROM techstack_list_due_ril_leases()`,
		`UPDATE ril_action_cards SET issued_lease_ciphertext=NULL,issued_lease_expires_at=NULL WHERE tenant_id=$1 AND id=$2 AND issued_lease_expires_at<=clock_timestamp()`)
}
func reapQueuedProofs(ctx context.Context, db *sql.DB) error {
	return reapRILExpiry(ctx, db, `SELECT tenant_id,command_id FROM techstack_list_due_ril_queue_proofs()`,
		`UPDATE typed_agent_commands SET ril_proof_ciphertext=NULL,ril_proof_expires_at=NULL WHERE tenant_id=$1 AND command_id=$2 AND ril_proof_expires_at<=clock_timestamp()`)
}

// The fixed query pairs are internal source constants; neither is caller input.
func reapRILExpiry(ctx context.Context, db *sql.DB, listSQL, clearSQL string) error {
	rows, err := db.QueryContext(ctx, listSQL)
	if err != nil {
		return fmt.Errorf("list due RIL authority: %w", err)
	}
	type item struct{ tenant, id string }
	var due []item
	for rows.Next() {
		var x item
		if err = rows.Scan(&x.tenant, &x.id); err != nil {
			rows.Close()
			return err
		}
		due = append(due, x)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, x := range due {
		if x.tenant == "" || x.id == "" {
			return fmt.Errorf("invalid RIL expiry index identity")
		}
		tx, e := db.BeginTx(ctx, nil)
		if e != nil {
			return e
		}
		if _, e = tx.ExecContext(ctx, `SELECT set_config('app.tenant_id',$1,true)`, x.tenant); e == nil {
			_, e = tx.ExecContext(ctx, clearSQL, x.tenant, x.id)
		}
		if e != nil {
			tx.Rollback()
			return e
		}
		if e = tx.Commit(); e != nil {
			return e
		}
	}
	return nil
}
