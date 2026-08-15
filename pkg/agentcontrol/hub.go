// Package agentcontrol provides the typed command rendezvous used by outbound
// HTTPS Guard agents when the dedicated mTLS gRPC listener is unavailable.
package agentcontrol

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

var (
	ErrCommandNotPending = errors.New("agent control command is not pending")
	ErrResultRejected    = errors.New("agent control result was rejected")
)

type pendingCommand struct {
	agentID    string
	command    *agentpb.StackKitCommand
	dispatched bool
	outcome    chan commandOutcome
}

type commandOutcome struct {
	result *agentpb.StackKitResult
	err    error
}

// Hub is an authenticated, typed rendezvous. Authentication and tenant/owner
// binding happen in the HTTP route before Poll or SubmitResult is reached.
// The Hub never accepts argv, environment variables, or generic shell input.
type Hub struct {
	mu      sync.Mutex
	pending map[string][]*pendingCommand
	notify  map[string]chan struct{}
	db      *sql.DB
}

func NewHub(databases ...*sql.DB) *Hub {
	hub := &Hub{
		pending: make(map[string][]*pendingCommand),
		notify:  make(map[string]chan struct{}),
	}
	if len(databases) > 0 {
		hub.db = databases[0]
	}
	return hub
}

// SendStackKitCommand implements the jobs.StackKitCommandSender seam.
func (h *Hub) SendStackKitCommand(ctx context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if h != nil && h.db != nil {
		return nil, fmt.Errorf("typed HTTPS StackKits command requires tenant-scoped dispatch")
	}
	return h.sendInMemory(ctx, agentID, command)
}

// SendStackKitCommandForTenant persists the rendezvous before an agent can
// observe it. Every SaaS replica therefore polls and completes the same command.
func (h *Hub) SendStackKitCommandForTenant(ctx context.Context, tenantID, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if h == nil || h.db == nil {
		return h.sendInMemory(ctx, agentID, command)
	}
	return h.sendDurable(ctx, tenantID, agentID, command)
}

func (h *Hub) sendInMemory(ctx context.Context, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	if h == nil {
		return nil, fmt.Errorf("typed HTTPS StackKits command path is not initialized")
	}
	agentID = strings.TrimSpace(agentID)
	if agentID == "" || command == nil || strings.TrimSpace(command.GetCommandId()) == "" {
		return nil, fmt.Errorf("typed HTTPS StackKits command requires agent and command ids")
	}
	command = proto.Clone(command).(*agentpb.StackKitCommand)
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, err
	}
	entry := &pendingCommand{
		agentID: agentID,
		command: command,
		outcome: make(chan commandOutcome, 1),
	}
	h.mu.Lock()
	for _, entries := range h.pending {
		for _, pending := range entries {
			if pending.command.GetCommandId() == command.GetCommandId() {
				h.mu.Unlock()
				return nil, fmt.Errorf("typed HTTPS StackKits command_id %q is already pending", command.GetCommandId())
			}
		}
	}
	h.pending[agentID] = append(h.pending[agentID], entry)
	h.signalLocked(agentID)
	h.mu.Unlock()

	select {
	case outcome := <-entry.outcome:
		h.remove(entry)
		return outcome.result, outcome.err
	case <-ctx.Done():
		h.remove(entry)
		return nil, ctx.Err()
	}
}

// Poll returns the next undispatched typed command for this exact agent. A
// command is dispatched once; a lost response fails closed rather than
// replaying an apply operation after an ambiguous transport failure.
func (h *Hub) Poll(ctx context.Context, agentID string, capabilities []string) (*agentpb.StackKitCommand, bool, error) {
	if h != nil && h.db != nil {
		return nil, false, fmt.Errorf("typed HTTPS StackKits poll requires tenant scope")
	}
	return h.pollInMemory(ctx, agentID, capabilities)
}

func (h *Hub) PollForTenant(ctx context.Context, tenantID, agentID string, capabilities []string) (*agentpb.StackKitCommand, bool, error) {
	if h == nil || h.db == nil {
		return h.pollInMemory(ctx, agentID, capabilities)
	}
	return h.pollDurable(ctx, tenantID, agentID, capabilities)
}

func (h *Hub) pollInMemory(ctx context.Context, agentID string, capabilities []string) (*agentpb.StackKitCommand, bool, error) {
	if h == nil {
		return nil, false, nil
	}
	agentID = strings.TrimSpace(agentID)
	for {
		h.mu.Lock()
		for _, entry := range h.pending[agentID] {
			if entry.dispatched {
				continue
			}
			if entry.command.GetOperation() == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY &&
				!containsCapability(capabilities, stackkitcommand.ExpectedPlanHashCapability) {
				entry.dispatched = true
				h.mu.Unlock()
				err := fmt.Errorf("agent %q does not advertise %s", agentID, stackkitcommand.ExpectedPlanHashCapability)
				entry.outcome <- commandOutcome{err: err}
				return nil, false, err
			}
			entry.dispatched = true
			command := proto.Clone(entry.command).(*agentpb.StackKitCommand)
			h.mu.Unlock()
			return command, true, nil
		}
		notify := h.notifyChannelLocked(agentID)
		h.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, false, nil
		case <-notify:
		}
	}
}

func (h *Hub) SubmitResult(agentID string, result *agentpb.StackKitResult) error {
	if h != nil && h.db != nil {
		return fmt.Errorf("typed HTTPS StackKits result requires tenant scope")
	}
	return h.submitResultInMemory(agentID, result)
}

func (h *Hub) SubmitResultForTenant(ctx context.Context, tenantID, agentID string, result *agentpb.StackKitResult) error {
	if h == nil || h.db == nil {
		return h.submitResultInMemory(agentID, result)
	}
	return h.submitResultDurable(ctx, tenantID, agentID, result)
}

func (h *Hub) submitResultInMemory(agentID string, result *agentpb.StackKitResult) error {
	if h == nil || result == nil {
		return ErrResultRejected
	}
	result = proto.Clone(result).(*agentpb.StackKitResult)
	agentID = strings.TrimSpace(agentID)
	commandID := strings.TrimSpace(result.GetCommandId())
	if agentID == "" || commandID == "" {
		return ErrResultRejected
	}
	h.mu.Lock()
	var target *pendingCommand
	for _, entry := range h.pending[agentID] {
		if entry.command.GetCommandId() != commandID || !entry.dispatched {
			continue
		}
		target = entry
		break
	}
	h.mu.Unlock()
	if target == nil {
		return ErrCommandNotPending
	}
	if err := stackkitcommand.ValidateResult(result, target.command); err != nil {
		return fmt.Errorf("%w: %v", ErrResultRejected, err)
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, entry := range h.pending[agentID] {
		if entry != target || !entry.dispatched {
			continue
		}
		select {
		case entry.outcome <- commandOutcome{result: proto.Clone(result).(*agentpb.StackKitResult)}:
			return nil
		default:
			return ErrResultRejected
		}
	}
	return ErrCommandNotPending
}

func (h *Hub) sendDurable(ctx context.Context, tenantID, agentID string, command *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	tenantID = strings.TrimSpace(tenantID)
	agentID = strings.TrimSpace(agentID)
	if tenantID == "" || agentID == "" || command == nil || strings.TrimSpace(command.GetCommandId()) == "" {
		return nil, fmt.Errorf("typed HTTPS StackKits command requires tenant, agent, and command ids")
	}
	command = proto.Clone(command).(*agentpb.StackKitCommand)
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		return nil, err
	}
	payload, err := protojson.Marshal(command)
	if err != nil {
		return nil, fmt.Errorf("encode typed StackKits command: %w", err)
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	result, err := tx.ExecContext(ctx, `
		WITH cleanup AS (
			DELETE FROM typed_agent_commands
			WHERE tenant_id = $2 AND expires_at < now() - interval '1 day'
		)
		INSERT INTO typed_agent_commands (command_id, tenant_id, agent_id, command_json, expires_at)
		VALUES ($1, $2, $3, $4::jsonb, now() + interval '15 minutes')
		ON CONFLICT (command_id) DO NOTHING
	`, command.GetCommandId(), tenantID, agentID, payload)
	if err == nil {
		var inserted int64
		inserted, err = result.RowsAffected()
		if err == nil && inserted == 0 {
			err = fmt.Errorf("typed HTTPS StackKits command_id %q already exists", command.GetCommandId())
		}
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		result, done, err := h.readDurableOutcome(ctx, tenantID, command.GetCommandId())
		if err != nil || done {
			return result, err
		}
		select {
		case <-ctx.Done():
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = h.failDurable(cleanupCtx, tenantID, command.GetCommandId(), ctx.Err().Error())
			cancel()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (h *Hub) pollDurable(ctx context.Context, tenantID, agentID string, capabilities []string) (*agentpb.StackKitCommand, bool, error) {
	return h.pollDurableOnce(ctx, tenantID, agentID, capabilities)
}

func (h *Hub) pollDurableOnce(ctx context.Context, tenantID, agentID string, capabilities []string) (*agentpb.StackKitCommand, bool, error) {
	tenantID = strings.TrimSpace(tenantID)
	agentID = strings.TrimSpace(agentID)
	if tenantID == "" || agentID == "" {
		return nil, false, ErrCommandNotPending
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	var commandID string
	var payload []byte
	err = tx.QueryRowContext(ctx, `
		SELECT command_id, command_json
		FROM typed_agent_commands
		WHERE tenant_id = $1 AND agent_id = $2 AND state = 'queued' AND expires_at > now()
		ORDER BY created_at
		FOR UPDATE SKIP LOCKED
		LIMIT 1
	`, tenantID, agentID).Scan(&commandID, &payload)
	if errors.Is(err, sql.ErrNoRows) {
		_ = tx.Rollback()
		return nil, false, nil
	}
	if err != nil {
		_ = tx.Rollback()
		return nil, false, err
	}
	command := &agentpb.StackKitCommand{}
	if err := protojson.Unmarshal(payload, command); err != nil {
		_ = tx.Rollback()
		return nil, false, err
	}
	if command.GetOperation() == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY &&
		!containsCapability(capabilities, stackkitcommand.ExpectedPlanHashCapability) {
		message := fmt.Sprintf("agent %q does not advertise %s", agentID, stackkitcommand.ExpectedPlanHashCapability)
		_, _ = tx.ExecContext(ctx, `UPDATE typed_agent_commands SET state = 'failed', error = $2, completed_at = now() WHERE command_id = $1`, commandID, message)
		_ = tx.Commit()
		return nil, false, errors.New(message)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE typed_agent_commands SET state = 'dispatched', dispatched_at = now() WHERE command_id = $1`, commandID); err != nil {
		_ = tx.Rollback()
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return command, true, nil
}

func (h *Hub) submitResultDurable(ctx context.Context, tenantID, agentID string, result *agentpb.StackKitResult) error {
	if result == nil {
		return ErrResultRejected
	}
	tenantID = strings.TrimSpace(tenantID)
	agentID = strings.TrimSpace(agentID)
	commandID := strings.TrimSpace(result.GetCommandId())
	if tenantID == "" || agentID == "" || commandID == "" {
		return ErrResultRejected
	}
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return err
	}
	var payload []byte
	var state string
	err = tx.QueryRowContext(ctx, `
		SELECT command_json, state FROM typed_agent_commands
		WHERE tenant_id = $1 AND agent_id = $2 AND command_id = $3
		FOR UPDATE
	`, tenantID, agentID, commandID).Scan(&payload, &state)
	if errors.Is(err, sql.ErrNoRows) || state == "queued" {
		_ = tx.Rollback()
		return ErrCommandNotPending
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	command := &agentpb.StackKitCommand{}
	if err := protojson.Unmarshal(payload, command); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := stackkitcommand.ValidateResult(result, command); err != nil {
		_ = tx.Rollback()
		return fmt.Errorf("%w: %v", ErrResultRejected, err)
	}
	resultPayload, err := protojson.Marshal(result)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE typed_agent_commands
		SET state = 'completed', result_json = $2::jsonb, completed_at = now(), error = NULL
		WHERE command_id = $1
	`, commandID, resultPayload); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (h *Hub) readDurableOutcome(ctx context.Context, tenantID, commandID string) (*agentpb.StackKitResult, bool, error) {
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	var state string
	var payload []byte
	var commandError sql.NullString
	err = tx.QueryRowContext(ctx, `
		SELECT state, COALESCE(result_json, '{}'::jsonb), error
		FROM typed_agent_commands WHERE tenant_id = $1 AND command_id = $2
	`, tenantID, commandID).Scan(&state, &payload, &commandError)
	if err != nil {
		return nil, false, err
	}
	switch state {
	case "completed":
		result := &agentpb.StackKitResult{}
		if err := protojson.Unmarshal(payload, result); err != nil {
			return nil, true, err
		}
		return result, true, nil
	case "failed":
		return nil, true, errors.New(commandError.String)
	default:
		return nil, false, nil
	}
}

func (h *Hub) failDurable(ctx context.Context, tenantID, commandID, message string) error {
	tx, err := h.tenantTx(ctx, tenantID)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE typed_agent_commands SET state = 'failed', error = $2, completed_at = now()
		WHERE command_id = $1 AND state IN ('queued', 'dispatched')
	`, commandID, message); err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func (h *Hub) tenantTx(ctx context.Context, tenantID string) (*sql.Tx, error) {
	tx, err := h.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `SELECT set_config('app.tenant_id', $1, true)`, tenantID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func containsCapability(capabilities []string, expected string) bool {
	for _, capability := range capabilities {
		if strings.EqualFold(strings.TrimSpace(capability), expected) {
			return true
		}
	}
	return false
}

func (h *Hub) remove(target *pendingCommand) {
	h.mu.Lock()
	defer h.mu.Unlock()
	entries := h.pending[target.agentID]
	for index, entry := range entries {
		if entry != target {
			continue
		}
		entries = append(entries[:index], entries[index+1:]...)
		break
	}
	if len(entries) == 0 {
		delete(h.pending, target.agentID)
	} else {
		h.pending[target.agentID] = entries
	}
}

func (h *Hub) notifyChannelLocked(agentID string) chan struct{} {
	ch := h.notify[agentID]
	if ch == nil {
		ch = make(chan struct{})
		h.notify[agentID] = ch
	}
	return ch
}

func (h *Hub) signalLocked(agentID string) {
	ch := h.notifyChannelLocked(agentID)
	close(ch)
	h.notify[agentID] = make(chan struct{})
}
