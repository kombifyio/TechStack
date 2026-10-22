package agentcontrol

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// Transports of a persisted round trip. The identity that authenticated the
// result differs per transport: the bound HTTPS hub records the credential
// generation, the gRPC listener the client certificate serial.
const (
	RoundTripTransportHTTPS = "https"
	RoundTripTransportMTLS  = "mtls"
)

// CommandRoundTrip is the last authenticated command/result round trip of one
// Guard: a typed command whose terminal result the control plane validated and
// accepted. It is the only evidence that the command channel works; a
// heartbeat proves authentication alone.
type CommandRoundTrip struct {
	AgentID   string
	CommandID string
	// Transport is RoundTripTransportHTTPS or RoundTripTransportMTLS.
	Transport string
	// Generation is the credential generation that authenticated an HTTPS
	// result; zero for mTLS.
	Generation int64
	// CredentialRef is the client certificate serial that authenticated an
	// mTLS result; empty for HTTPS.
	CredentialRef  string
	Revision       int64
	Digest         string
	AcknowledgedAt time.Time
}

// recordRoundTrip stores the round trip inside the transaction that commits
// the terminal revision, so an acknowledged result always leaves its evidence.
// A result authenticated without a credential generation proves no binding and
// leaves none.
func recordRoundTrip(ctx context.Context, tx *sql.Tx, req ReportRequest) error {
	if req.Generation <= 0 {
		return nil
	}
	return upsertRoundTrip(ctx, tx, req.TenantID, req.AgentID, RoundTripTransportHTTPS, req.CommandID, req.Generation, "", req.Report.Revision, req.Report.Digest)
}

func upsertRoundTrip(ctx context.Context, tx *sql.Tx, tenantID, agentID, transport, commandID string, generation int64, credentialRef string, revision int64, digest string) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO guard_command_round_trips
			(tenant_id, agent_id, transport, command_id, result_generation, credential_ref, result_revision, result_digest, acknowledged_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, now())
		ON CONFLICT (tenant_id, agent_id) DO UPDATE SET
			transport = EXCLUDED.transport,
			command_id = EXCLUDED.command_id,
			result_generation = EXCLUDED.result_generation,
			credential_ref = EXCLUDED.credential_ref,
			result_revision = EXCLUDED.result_revision,
			result_digest = EXCLUDED.result_digest,
			acknowledged_at = EXCLUDED.acknowledged_at
	`, tenantID, agentID, transport, commandID, generation, credentialRef, revision, digest)
	return err
}

// RecordMTLSRoundTrip persists the round trip of a typed result the gRPC
// listener accepted (correlated to its pending command and validated) from an
// agent authenticated by its client certificate. The mTLS channel has no
// revisioned report, so the record is revision 1 with the digest of the
// accepted result. A hub without a database persists nothing.
func (h *Hub) RecordMTLSRoundTrip(ctx context.Context, tenantID, agentID, certSerial string, result *agentpb.StackKitResult) error {
	tenantID, agentID, certSerial = strings.TrimSpace(tenantID), strings.TrimSpace(agentID), strings.TrimSpace(certSerial)
	if h == nil || h.db == nil {
		return nil
	}
	if tenantID == "" || agentID == "" || certSerial == "" || result == nil || strings.TrimSpace(result.GetCommandId()) == "" {
		return fmt.Errorf("mTLS round trip requires tenant, agent, certificate serial and an accepted result")
	}
	payload, err := protojson.Marshal(result)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(payload)
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := upsertRoundTrip(ctx, tx, tenantID, agentID, RoundTripTransportMTLS, strings.TrimSpace(result.GetCommandId()), 0, certSerial, 1, hex.EncodeToString(sum[:])); err != nil {
		return err
	}
	return tx.Commit()
}

// CommandRoundTripsForTenant reads the persisted round trip of every Guard of
// the tenant, keyed by agent ID. A hub without a database persists none and
// returns an empty map: no Guard of it is round-trip evidenced.
func (h *Hub) CommandRoundTripsForTenant(ctx context.Context, tenantID string) (map[string]CommandRoundTrip, error) {
	out := map[string]CommandRoundTrip{}
	tenantID = strings.TrimSpace(tenantID)
	if h == nil || h.db == nil || tenantID == "" {
		return out, nil
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback() }()
	rows, err := tx.QueryContext(ctx, `
		SELECT agent_id, transport, command_id, result_generation, credential_ref, result_revision, result_digest, acknowledged_at
		FROM guard_command_round_trips
		WHERE tenant_id = $1
	`, tenantID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var trip CommandRoundTrip
		if err := rows.Scan(&trip.AgentID, &trip.Transport, &trip.CommandID, &trip.Generation, &trip.CredentialRef, &trip.Revision, &trip.Digest, &trip.AcknowledgedAt); err != nil {
			return nil, err
		}
		trip.AcknowledgedAt = trip.AcknowledgedAt.UTC()
		out[trip.AgentID] = trip
	}
	return out, rows.Err()
}
