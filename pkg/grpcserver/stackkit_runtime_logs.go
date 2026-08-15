package grpcserver

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func (s *Server) appendStackKitResultLogs(agentID string, result *agentpb.StackKitResult) {
	if s == nil || result == nil {
		return
	}
	jobID := stackKitJobID(result.CommandId)
	for _, raw := range result.EventsJsonl {
		var event map[string]any
		if json.Unmarshal(raw, &event) != nil {
			continue
		}
		phase := scalarLogField(event["phase"])
		status := scalarLogField(event["status"])
		message := scalarLogField(event["message"])
		if message == "" {
			message = strings.TrimSpace(phase + " " + status)
		}
		fields := map[string]string{"command_id": result.CommandId}
		for key, value := range event {
			if rendered := scalarLogField(value); rendered != "" {
				fields[key] = rendered
			}
		}
		s.addAgentLog(agentID, AgentLogEntry{
			Source:    "stackkits",
			Timestamp: stackKitEventTime(event, result),
			Level:     stackKitEventLevel(status),
			Message:   message,
			JobID:     jobID,
			Fields:    fields,
		})
	}
	if stderr := strings.TrimSpace(result.Stderr); stderr != "" {
		s.addAgentLog(agentID, AgentLogEntry{
			Source:    "stackkits",
			Timestamp: stackKitResultTime(result),
			Level:     "error",
			Message:   stderr,
			JobID:     jobID,
			Fields:    map[string]string{"command_id": result.CommandId, "exit_code": strconv.Itoa(int(result.ExitCode))},
		})
	}
	level, status := "info", "succeeded"
	if !result.Success {
		level, status = "error", "failed"
	}
	s.addAgentLog(agentID, AgentLogEntry{
		Source:    "stackkits",
		Timestamp: stackKitResultTime(result),
		Level:     level,
		Message:   "StackKits command " + status,
		JobID:     jobID,
		Fields:    map[string]string{"command_id": result.CommandId, "exit_code": strconv.Itoa(int(result.ExitCode)), "status": status},
	})
}

func stackKitJobID(commandID string) string {
	value := strings.TrimSpace(commandID)
	for _, suffix := range []string{"-plan", "-verify"} {
		value = strings.TrimSuffix(value, suffix)
	}
	return value
}

func stackKitEventTime(event map[string]any, result *agentpb.StackKitResult) time.Time {
	if raw := scalarLogField(event["time"]); raw != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, raw); err == nil {
			return parsed
		}
	}
	return stackKitResultTime(result)
}

func stackKitResultTime(result *agentpb.StackKitResult) time.Time {
	if result != nil && result.FinishedAtUnix > 0 {
		return time.Unix(result.FinishedAtUnix, 0).UTC()
	}
	return time.Now().UTC()
}

func stackKitEventLevel(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "failed", "error":
		return "error"
	case "skipped", "warning", "warn":
		return "warn"
	default:
		return "info"
	}
}

func scalarLogField(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64, bool, json.Number:
		return fmt.Sprint(typed)
	default:
		return ""
	}
}
