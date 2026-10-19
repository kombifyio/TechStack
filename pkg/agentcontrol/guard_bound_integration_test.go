package agentcontrol

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	integrationTenant = "tenant-guard"
	integrationAgent  = "worker-guard"
)

// openGuardHub applies exactly the migrations of the typed command store to a
// throwaway schema of the integration PostgreSQL.
func openGuardHub(t *testing.T) (*Hub, *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("TECHSTACK_TEST_POSTGRES_URL"))
	if dsn == "" {
		t.Skip("TECHSTACK_TEST_POSTGRES_URL not set; skipping typed command PostgreSQL integration test")
	}
	schema := "guard_bound_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	query.Set("search_path", schema)
	parsed.RawQuery = query.Encode()
	admin, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	adminDB := stdlib.OpenDB(*admin)
	if _, err := adminDB.ExecContext(t.Context(), "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = adminDB.ExecContext(context.Background(), "DROP SCHEMA "+schema+" CASCADE")
		_ = adminDB.Close()
	})
	config, err := pgx.ParseConfig(parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	database := stdlib.OpenDB(*config)
	t.Cleanup(func() { _ = database.Close() })
	for _, name := range []string{"087_durable_typed_agent_commands.sql", "109_substrate_worker_commands.sql", "132_guard_command_outcome_binding.sql", "134_guard_operator_outcome_resolution.sql", "143_typed_agent_commands_drop_sealed_delivery.sql"} {
		migration, err := os.ReadFile("../db/migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.ExecContext(t.Context(), string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
	return NewHub(database), database
}

func queueCommand(t *testing.T, database *sql.DB, command *agentpb.StackKitCommand) {
	t.Helper()
	payload, err := protojson.Marshal(command)
	if err != nil {
		t.Fatal(err)
	}
	tenantExec(t, database, `
		INSERT INTO typed_agent_commands (command_id, tenant_id, agent_id, command_json, expires_at)
		VALUES ($1, $2, $3, $4::jsonb, now() + interval '15 minutes')`,
		command.CommandId, integrationTenant, integrationAgent, payload)
}

// tenantExec runs one statement under the tenant scope row-level security
// requires.
func tenantExec(t *testing.T, database *sql.DB, query string, args ...any) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, integrationTenant); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(t.Context(), query, args...); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func pollBound(t *testing.T, hub *Hub, generation int64, known ...string) *Offer {
	t.Helper()
	offer, found, err := hub.PollBoundForTenant(t.Context(), PollOptions{
		TenantID: integrationTenant, AgentID: integrationAgent, CurrentGeneration: generation, KnownCommands: known,
		Capabilities: []string{guardbinding.Capability, stackkitcommand.WorkspaceInstanceCapability, stackkitcommand.ExpectedPlanHashCapability},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		return nil
	}
	return offer
}

func terminalReport(t *testing.T, command *agentpb.StackKitCommand, mutate func(*agentpb.StackKitResult)) (guardbinding.ResultReport, *agentpb.StackKitResult) {
	t.Helper()
	result := validHubResult(command)
	if mutate != nil {
		mutate(result)
	}
	report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportTerminal}
	var err error
	if report.Digest, err = report.ComputeDigest(command.CommandId, result); err != nil {
		t.Fatal(err)
	}
	return report, result
}

func submit(hub *Hub, generation int64, command *agentpb.StackKitCommand, report guardbinding.ResultReport, result *agentpb.StackKitResult) (guardbinding.Acknowledgement, error) {
	return hub.SubmitReportForTenant(context.Background(), ReportRequest{
		TenantID: integrationTenant, AgentID: integrationAgent, CommandID: command.CommandId,
		Generation: generation, Report: report, Result: result,
	})
}

func commandState(t *testing.T, database *sql.DB, id string) string {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, integrationTenant); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := tx.QueryRowContext(t.Context(), `SELECT state FROM typed_agent_commands WHERE command_id = $1`, id).Scan(&state); err != nil {
		t.Fatal(err)
	}
	return state
}

func denialCode(err error) string {
	var denial *guardbinding.Denial
	if errors.As(err, &denial) {
		return denial.Code
	}
	return ""
}

// The acknowledgement follows the committed revision: an identical repeat is
// acknowledged again, a different record for the same revision is refused, and
// a report from an older credential generation is denied.
func TestReportAcknowledgementIsIdempotentAndConflictingRevisionIsRefused(t *testing.T) {
	hub, database := openGuardHub(t)
	first, second := validHubCommand("cmd-ack-1"), validHubCommand("cmd-ack-2")
	queueCommand(t, database, first)
	queueCommand(t, database, second)

	offer := pollBound(t, hub, 5)
	if offer == nil || offer.Binding == nil || offer.Binding.WorkerGeneration != 5 {
		t.Fatalf("offer = %+v", offer)
	}
	if digest, _ := guardbinding.DigestOf(offer.Command); digest != offer.Binding.PayloadDigest {
		t.Fatal("binding digest does not match the offered payload")
	}
	report, result := terminalReport(t, first, nil)
	ack, err := submit(hub, 5, first, report, result)
	if err != nil || !ack.Acknowledged || ack.Duplicate {
		t.Fatalf("ack = %+v err = %v", ack, err)
	}
	if ack, err = submit(hub, 5, first, report, result); err != nil || !ack.Acknowledged || !ack.Duplicate {
		t.Fatalf("identical repeat was not acknowledged again: %+v err = %v", ack, err)
	}
	conflicting, conflictingResult := terminalReport(t, first, func(r *agentpb.StackKitResult) { r.Stderr = "different evidence" })
	if _, err = submit(hub, 5, first, conflicting, conflictingResult); denialCode(err) != guardbinding.CodeRevisionConflict {
		t.Fatalf("conflicting revision err = %v", err)
	}
	if state := commandState(t, database, first.CommandId); state != "completed" {
		t.Fatalf("state after conflicting repeat = %s", state)
	}

	if offer = pollBound(t, hub, 5); offer == nil || offer.Command.CommandId != second.CommandId {
		t.Fatalf("second offer = %+v", offer)
	}
	report, result = terminalReport(t, second, nil)
	if _, err = submit(hub, 4, second, report, result); denialCode(err) != guardbinding.CodeStaleGeneration {
		t.Fatalf("stale generation err = %v", err)
	}
	if state := commandState(t, database, second.CommandId); state != "dispatched" {
		t.Fatalf("a denied report changed the command to %s", state)
	}
}

func mutatingCommand(id string) *agentpb.StackKitCommand {
	command := validHubCommand(id)
	command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_RESTART
	return command
}

// An unsettled mutating outcome fences further mutating work on the worker but
// not observation, a dispatched command the Guard does not hold is offered
// again, and only a committed resolution releases the fence.
func TestUnsettledMutatingOutcomeFencesNewMutatingWork(t *testing.T) {
	hub, database := openGuardHub(t)
	first, second, reader := mutatingCommand("cmd-fence-1"), mutatingCommand("cmd-fence-2"), validHubCommand("cmd-fence-read")
	queueCommand(t, database, first)
	queueCommand(t, database, second)
	queueCommand(t, database, reader)

	if offer := pollBound(t, hub, 1); offer == nil || offer.Command.CommandId != first.CommandId {
		t.Fatalf("first offer = %+v", offer)
	}
	// The Guard holds the first command: the second mutation waits, the read
	// does not.
	if offer := pollBound(t, hub, 1, first.CommandId); offer == nil || offer.Command.CommandId != reader.CommandId {
		t.Fatalf("a fenced mutation must not block observation: %+v", offer)
	}
	if offer := pollBound(t, hub, 1, first.CommandId, reader.CommandId); offer != nil {
		t.Fatalf("mutation offered while another is unsettled: %+v", offer.Command.CommandId)
	}

	// A Guard that lost its journal entry is offered the same identity again.
	tenantExec(t, database, `UPDATE typed_agent_commands SET last_offered_at = now() - interval '1 minute'`)
	if offer := pollBound(t, hub, 2, reader.CommandId); offer == nil || offer.Command.CommandId != first.CommandId || !offer.Binding.Reoffer || offer.Binding.WorkerGeneration != 2 {
		t.Fatalf("reoffer = %+v", offer)
	}

	report := guardbinding.ResultReport{Revision: 1, State: guardbinding.ReportOutcomeUnknown, Reason: "interrupted_before_result"}
	report.Digest, _ = report.ComputeDigest(first.CommandId, nil)
	if _, err := submit(hub, 2, first, report, nil); err != nil {
		t.Fatal(err)
	}
	if state := commandState(t, database, first.CommandId); state != "outcome_unknown" {
		t.Fatalf("state = %s", state)
	}
	if offer := pollBound(t, hub, 2, first.CommandId, reader.CommandId); offer != nil {
		t.Fatalf("an unknown outcome released the fence: %+v", offer.Command.CommandId)
	}

	resolution := guardbinding.ResultReport{Revision: 2, State: guardbinding.ReportResolved, PriorDigest: report.Digest, Resolution: guardbinding.ResolutionNotApplied, Reason: "verified"}
	resolution.Digest, _ = resolution.ComputeDigest(first.CommandId, nil)
	if _, err := submit(hub, 2, first, resolution, nil); err != nil {
		t.Fatal(err)
	}
	if offer := pollBound(t, hub, 2, reader.CommandId); offer == nil || offer.Command.CommandId != second.CommandId {
		t.Fatalf("a committed resolution did not release the fence: %+v", offer)
	}
}

// A Guard that predates the contract keeps the pre-binding semantics and is
// flagged legacy; a bound command cannot be settled through the legacy result.
func TestLegacyGuardKeepsPreBindingSemanticsAndIsFlagged(t *testing.T) {
	hub, database := openGuardHub(t)
	legacy, bound := validHubCommand("cmd-legacy"), validHubCommand("cmd-bound")
	queueCommand(t, database, legacy)
	command, found, err := hub.PollForTenant(t.Context(), integrationTenant, integrationAgent, []string{stackkitcommand.WorkspaceInstanceCapability})
	if err != nil || !found || command.CommandId != legacy.CommandId {
		t.Fatalf("legacy poll = %v %v %v", command, found, err)
	}
	var contract sql.NullString
	tenantQuery(t, database, `SELECT contract_version FROM typed_agent_commands WHERE command_id = 'cmd-legacy'`, &contract)
	if contract.String != guardbinding.LegacyContractVersion {
		t.Fatalf("legacy dispatch is not flagged: %q", contract.String)
	}
	if err := hub.SubmitResultForTenant(t.Context(), integrationTenant, integrationAgent, validHubResult(legacy)); err != nil {
		t.Fatalf("legacy result: %v", err)
	}

	queueCommand(t, database, bound)
	if offer := pollBound(t, hub, 1); offer == nil || offer.Binding == nil {
		t.Fatalf("bound offer = %+v", offer)
	}
	if err := hub.SubmitResultForTenant(t.Context(), integrationTenant, integrationAgent, validHubResult(bound)); denialCode(err) != guardbinding.CodeResultRejected {
		t.Fatalf("a bare result settled a bound command: %v", err)
	}
}

func tenantQuery(t *testing.T, database *sql.DB, query string, dest ...any) {
	t.Helper()
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, integrationTenant); err != nil {
		t.Fatal(err)
	}
	if err := tx.QueryRowContext(t.Context(), query).Scan(dest...); err != nil {
		t.Fatal(err)
	}
}

// A bearer-only (observe-only) Guard is never dispatched a mutating command,
// which stays queued for a key-bound Guard; read-only work still flows.
func TestObserveOnlyGuardIsNotDispatchedMutatingWork(t *testing.T) {
	hub, database := openGuardHub(t)
	mutating, readOnly := mutatingCommand("cmd-observe-mutating"), validHubCommand("cmd-observe-read")
	queueCommand(t, database, mutating)
	queueCommand(t, database, readOnly)

	offer, found, err := hub.PollBoundForTenant(t.Context(), PollOptions{
		TenantID: integrationTenant, AgentID: integrationAgent, CurrentGeneration: 1, ObserveOnly: true,
		Capabilities: []string{guardbinding.Capability, stackkitcommand.WorkspaceInstanceCapability, stackkitcommand.ExpectedPlanHashCapability},
	})
	if err != nil || !found || offer.Command.CommandId != readOnly.CommandId {
		t.Fatalf("observe-only offer = %+v, %v, %v", offer, found, err)
	}
	if state := commandState(t, database, mutating.CommandId); state != "queued" {
		t.Fatalf("mutating command state = %s, want queued", state)
	}
	if offer = pollBound(t, hub, 1); offer == nil || offer.Command.CommandId != mutating.CommandId {
		t.Fatalf("key-bound offer = %+v", offer)
	}
}

// A sealed host delivery rides in a stored command as ciphertext only while the
// command is in flight; once the command settles the stored row keeps none of it.
func TestSettledCommandKeepsNoSealedDelivery(t *testing.T) {
	hub, database := openGuardHub(t)
	digest := strings.Repeat("a", 64)
	command := &agentpb.StackKitCommand{
		CommandId: "cmd-sealed-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_BACKUP_TARGET_IMPORT, OwnerApproved: true,
		WorkingDirectory: "/srv/stack", DeliveryTenantRef: integrationTenant, SealedDelivery: []byte(`{"sealed":"ciphertext-marker"}`),
		Release: &agentpb.StackKitReleasePin{Version: "v0.16.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: digest, ReleaseIndexSha256: digest},
	}
	queueCommand(t, database, command)
	if offer := pollBound(t, hub, 5); offer == nil || string(offer.Command.SealedDelivery) != string(command.SealedDelivery) {
		t.Fatalf("the node was not offered its sealed delivery: %+v", offer)
	}
	report, result := terminalReport(t, command, func(r *agentpb.StackKitResult) {
		r.CommandResultJson = []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit backup target import","status":"success","data":{"state":"custodied","backupTargetRef":"backup-target://sha256/` + strings.Repeat("b", 64) + `","custodyAttestationRef":"backup-custody-attestation://sha256/` + strings.Repeat("c", 64) + `"}}`)
	})
	if _, err := submit(hub, 5, command, report, result); err != nil {
		t.Fatal(err)
	}
	tx, err := database.BeginTx(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(t.Context(), `SELECT set_config('app.tenant_id', $1, true)`, integrationTenant); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := tx.QueryRowContext(t.Context(), `SELECT command_json::text FROM typed_agent_commands WHERE command_id = $1`, command.CommandId).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stored, "ciphertext-marker") || strings.Contains(stored, "sealedDelivery") {
		t.Fatalf("settled command still stores the sealed delivery: %s", stored)
	}
}

// A Guard that does not speak the binding contract stays observe-only: its
// read-only work still flows, a mutating command is failed at dispatch instead
// of being sent unbound, and no later poll hands that command out.
func TestGuardWithoutBindingContractIsNeverDispatchedMutatingWork(t *testing.T) {
	hub, database := openGuardHub(t)
	mutating, readOnly := mutatingCommand("cmd-mixed-mutating"), validHubCommand("cmd-mixed-read")
	queueCommand(t, database, mutating)
	queueCommand(t, database, readOnly)

	offer, found, err := hub.PollBoundForTenant(t.Context(), PollOptions{
		TenantID: integrationTenant, AgentID: integrationAgent, CurrentGeneration: 1,
		Capabilities: []string{stackkitcommand.WorkspaceInstanceCapability, stackkitcommand.ExpectedPlanHashCapability},
	})
	if err != nil || !found || offer.Command.CommandId != readOnly.CommandId {
		t.Fatalf("pre-contract Guard offer = %+v, %v, %v", offer, found, err)
	}
	if state := commandState(t, database, mutating.CommandId); state != "failed" {
		t.Fatalf("mutating command state = %s, want failed before any dispatch", state)
	}
	if offer = pollBound(t, hub, 1); offer != nil && offer.Command.CommandId == mutating.CommandId {
		t.Fatal("a refused mutating command was dispatched later")
	}
}
