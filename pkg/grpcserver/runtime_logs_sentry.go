//go:build !kombify_full_local

package grpcserver

import (
	"time"

	"github.com/getsentry/sentry-go"
)

func captureRuntimeLogIfError(entry AgentLogEntry) {
	if runtimeLogLevelRank(entry.Level) < runtimeLogLevelRank("error") {
		return
	}
	hub := sentry.CurrentHub()
	if hub == nil || hub.Client() == nil {
		return
	}
	hub.WithScope(func(scope *sentry.Scope) {
		scope.SetTag("source", firstRuntimeLogValue(entry.Source, "runtime"))
		scope.SetTag("level", entry.Level)
		for key, value := range map[string]string{
			"tenant_id":         entry.TenantID,
			"agent_id":          entry.AgentID,
			"stack_id":          entry.StackID,
			"job_id":            entry.JobID,
			"lease_id":          entry.LeaseID,
			"provider":          entry.Provider,
			"runtime_action_id": entry.RuntimeActionID,
			"runtime_target_id": entry.RuntimeTargetID,
			"server_id":         entry.ServerID,
			"service_id":        entry.ServiceID,
			"service_name":      entry.ServiceName,
			"container_id":      entry.ContainerID,
			"trace_id":          entry.TraceID,
		} {
			if value != "" {
				scope.SetTag(key, value)
			}
		}
		scope.SetContext("runtime_log", sentry.Context{
			"id":          entry.ID,
			"timestamp":   entry.Timestamp.Format(time.RFC3339Nano),
			"ingested_at": entry.IngestedAt.Format(time.RFC3339Nano),
			"fields":      entry.Fields,
		})
		hub.CaptureMessage(firstRuntimeLogValue(entry.Message, "runtime error log received"))
	})
}
