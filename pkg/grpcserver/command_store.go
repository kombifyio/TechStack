// Package grpcserver provides the gRPC server implementation for agent communication.
package grpcserver

import (
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/pocketbase/pocketbase/core"
)

// CommandStore handles persistence of agent commands in PocketBase.
// This enables command recovery after server restarts and provides
// a full audit trail of all commands sent to agents.
type CommandStore struct {
	app core.App
	log *logger.Logger
}

// NewCommandStore creates a new CommandStore.
func NewCommandStore(app core.App) *CommandStore {
	return &CommandStore{
		app: app,
		log: logger.Get(),
	}
}

// PersistCommand saves a command to the database before queueing.
// Returns the PocketBase record for further updates.
func (s *CommandStore) PersistCommand(cmd *AgentCommand, ownerID string) (*core.Record, error) {
	collection, err := s.app.FindCollectionByNameOrId("agent_commands")
	if err != nil {
		return nil, fmt.Errorf("agent_commands collection not found: %w", err)
	}

	record := core.NewRecord(collection)
	record.Set("command_id", cmd.ID)
	record.Set("agent_id", cmd.AgentID)
	record.Set("type", cmd.Type)
	record.Set("command", cmd.Command)
	record.Set("args", cmd.Args)
	record.Set("environment", cmd.Environment)
	record.Set("work_dir", cmd.WorkDir)
	record.Set("timeout_seconds", int(cmd.Timeout.Seconds()))
	record.Set("status", "pending")
	record.Set("queued_at", time.Now().UTC())
	record.Set("owner_id", ownerID)
	record.Set("retry_count", 0)
	record.Set("max_retries", 3)

	if err := s.app.Save(record); err != nil {
		return nil, fmt.Errorf("failed to save command: %w", err)
	}

	s.log.Debug("command_persisted", "command_id", cmd.ID, "agent_id", cmd.AgentID)
	return record, nil
}

// UpdateStatus updates the status of a command.
func (s *CommandStore) UpdateStatus(commandID string, status string) error {
	record, err := s.findByCommandID(commandID)
	if err != nil {
		return err
	}

	record.Set("status", status)
	if status == "sent" {
		record.Set("sent_at", time.Now().UTC())
	}

	return s.app.Save(record)
}

// UpdateStatusWithTimestamp updates status with a specific timestamp field.
func (s *CommandStore) UpdateStatusWithTimestamp(commandID, status, timestampField string) error {
	record, err := s.findByCommandID(commandID)
	if err != nil {
		return err
	}

	record.Set("status", status)
	record.Set(timestampField, time.Now().UTC())

	return s.app.Save(record)
}

// CompleteCommand marks a command as completed with its result.
func (s *CommandStore) CompleteCommand(result *CommandResult) error {
	record, err := s.findByCommandID(result.CommandID)
	if err != nil {
		return err
	}
	if err := validateCommandResultAgent(record, result); err != nil {
		return err
	}

	status := "completed"
	if result.ExitCode != 0 {
		status = "failed"
	}
	if result.Error != nil {
		status = "failed"
	}

	completedAt := time.Now().UTC()
	var durationMs int64 = 0

	// Calculate duration if we have sent_at
	sentAt := record.GetDateTime("sent_at")
	if !sentAt.Time().IsZero() {
		durationMs = completedAt.Sub(sentAt.Time()).Milliseconds()
	}

	// Convert error to string for storage
	var errorMsg string
	if result.Error != nil {
		errorMsg = result.Error.Error()
	}

	// Redact before truncate so a redacted token never gets sliced mid-pattern.
	// Redaction here is defense-in-depth — agent-side executor.go also redacts
	// before send. Leaving server-side redaction in place protects against
	// rogue or older agents that omit it.
	record.Set("status", status)
	record.Set("exit_code", result.ExitCode)
	record.Set("stdout", truncateString(secrets.Redact(result.Stdout), 100000))
	record.Set("stderr", truncateString(secrets.Redact(result.Stderr), 100000))
	record.Set("error_message", truncateString(secrets.Redact(errorMsg), 4096))
	record.Set("completed_at", completedAt)
	record.Set("duration_ms", durationMs)

	return s.app.Save(record)
}

func validateCommandResultAgent(record *core.Record, result *CommandResult) error {
	if record == nil {
		return fmt.Errorf("command record is nil")
	}
	if result == nil {
		return fmt.Errorf("command result is nil")
	}
	storedAgentID := strings.TrimSpace(record.GetString("agent_id"))
	reportedAgentID := strings.TrimSpace(result.AgentID)
	if storedAgentID == "" {
		return fmt.Errorf("stored command agent_id is empty")
	}
	if reportedAgentID == "" {
		return fmt.Errorf("command result agent_id is required")
	}
	if reportedAgentID != storedAgentID {
		return fmt.Errorf("command result for %s came from agent %s, expected %s", result.CommandID, reportedAgentID, storedAgentID)
	}
	return nil
}

// TimeoutCommand marks a command as timed out.
func (s *CommandStore) TimeoutCommand(commandID string) error {
	record, err := s.findByCommandID(commandID)
	if err != nil {
		return err
	}

	record.Set("status", "timeout")
	record.Set("completed_at", time.Now().UTC())
	record.Set("error_message", "Command execution timed out")

	return s.app.Save(record)
}

// CancelCommand marks a command as canceled.
func (s *CommandStore) CancelCommand(commandID string) error {
	return s.UpdateStatusWithTimestamp(commandID, "canceled", "completed_at")
}

// RecoverAllPending retrieves all pending commands across all agents.
// Used at server startup to re-queue unfinished commands.
func (s *CommandStore) RecoverAllPending() ([]*AgentCommand, error) {
	records, err := s.app.FindRecordsByFilter(
		"agent_commands",
		"status IN ('pending', 'queued', 'sent')", // Include 'sent' as they may not have completed
		"-queued_at",
		500, // Reasonable limit for startup recovery
		0,
		nil,
	)
	if err != nil {
		// Collection might not exist yet (first run)
		s.log.Warn("failed_to_recover_commands", "error", err)
		return nil, nil
	}

	commands := make([]*AgentCommand, 0, len(records))
	for _, record := range records {
		cmd := s.recordToCommand(record)
		if cmd != nil {
			commands = append(commands, cmd)
		}
	}

	s.log.Info("recovered_pending_commands", "count", len(commands))
	return commands, nil
}

// GetCommandHistory retrieves recent commands for an agent, scoped to owner.
//
// SAFETY: Both agent_id AND owner_id must match. Without the owner predicate
// any authenticated caller could enumerate another user's agent_id and dump
// their command history (IDOR). ownerID must not be empty; empty ownerID
// returns an error to prevent accidental bypass.
func (s *CommandStore) GetCommandHistory(agentID, ownerID string, limit int) ([]*AgentCommand, error) {
	if limit <= 0 {
		limit = 50
	}
	if agentID == "" || ownerID == "" {
		return nil, fmt.Errorf("agentID and ownerID are required")
	}

	records, err := s.app.FindRecordsByFilter(
		"agent_commands",
		"agent_id = {:agentID} && owner_id = {:ownerID}",
		"-created", // Newest first
		limit,
		0,
		map[string]any{"agentID": agentID, "ownerID": ownerID},
	)
	if err != nil {
		return nil, err
	}

	commands := make([]*AgentCommand, 0, len(records))
	for _, record := range records {
		cmd := s.recordToCommand(record)
		if cmd != nil {
			commands = append(commands, cmd)
		}
	}

	return commands, nil
}

// IncrementRetry increments the retry count for a command.
// Returns true if more retries are allowed, false if max retries reached.
func (s *CommandStore) IncrementRetry(commandID string) (bool, error) {
	record, err := s.findByCommandID(commandID)
	if err != nil {
		return false, err
	}

	retryCount := record.GetInt("retry_count") + 1
	maxRetries := record.GetInt("max_retries")
	if maxRetries <= 0 {
		maxRetries = 3
	}

	record.Set("retry_count", retryCount)

	if retryCount >= maxRetries {
		record.Set("status", "failed")
		record.Set("error_message", fmt.Sprintf("Max retries (%d) exceeded", maxRetries))
		record.Set("completed_at", time.Now().UTC())
	} else {
		record.Set("status", "pending") // Re-queue for retry
	}

	if err := s.app.Save(record); err != nil {
		return false, err
	}

	return retryCount < maxRetries, nil
}

// CleanupOldCommands removes completed/failed commands older than the retention period.
func (s *CommandStore) CleanupOldCommands(retentionDays int) (int, error) {
	if retentionDays <= 0 {
		retentionDays = 30
	}

	cutoff := time.Now().AddDate(0, 0, -retentionDays).UTC()

	records, err := s.app.FindRecordsByFilter(
		"agent_commands",
		"status IN ('completed', 'failed', 'cancelled', 'timeout') && created < {:cutoff}",
		"",
		1000,
		0,
		map[string]any{"cutoff": cutoff},
	)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, record := range records {
		if err := s.app.Delete(record); err == nil {
			deleted++
		}
	}

	if deleted > 0 {
		s.log.Info("cleaned_old_commands", "deleted", deleted, "retention_days", retentionDays)
	}

	return deleted, nil
}

// Helper methods

func (s *CommandStore) findByCommandID(commandID string) (*core.Record, error) {
	record, err := s.app.FindFirstRecordByFilter(
		"agent_commands",
		"command_id = {:commandID}",
		map[string]any{"commandID": commandID},
	)
	if err != nil {
		return nil, fmt.Errorf("command not found: %s", commandID)
	}
	return record, nil
}

func (s *CommandStore) recordToCommand(record *core.Record) *AgentCommand {
	if record == nil {
		return nil
	}

	// Parse args from JSON
	var args []string
	if argsRaw := record.Get("args"); argsRaw != nil {
		if argsSlice, ok := argsRaw.([]any); ok {
			for _, a := range argsSlice {
				if s, ok := a.(string); ok {
					args = append(args, s)
				}
			}
		}
	}

	// Parse environment from JSON
	var env map[string]string
	if envRaw := record.Get("environment"); envRaw != nil {
		if envMap, ok := envRaw.(map[string]any); ok {
			env = make(map[string]string)
			for k, v := range envMap {
				if s, ok := v.(string); ok {
					env[k] = s
				}
			}
		}
	}

	timeout := time.Duration(record.GetInt("timeout_seconds")) * time.Second
	if timeout == 0 {
		timeout = 30 * time.Second // Default timeout
	}

	return &AgentCommand{
		ID:          record.GetString("command_id"),
		AgentID:     record.GetString("agent_id"),
		Type:        record.GetString("type"),
		Command:     record.GetString("command"),
		Args:        args,
		Environment: env,
		WorkDir:     record.GetString("work_dir"),
		Timeout:     timeout,
	}
}

func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
