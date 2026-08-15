package grpcserver

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/getsentry/sentry-go"
)

const (
	defaultRuntimeLogMaxEntries = 5000
	maxRuntimeLogMessageBytes   = 16 * 1024
	maxRuntimeLogFieldBytes     = 4 * 1024
	maxRuntimeLogFields         = 80
	runtimeLogLevelInfo         = "info"
)

// AgentLogEntry is an in-memory representation of a runtime log line.
// It is populated from agentpb.LogEntry messages received over CommandStream
// and OTLP log records received through the embedded gRPC collector service.
type AgentLogEntry struct {
	ID              string            `json:"id,omitempty"`
	Timestamp       time.Time         `json:"timestamp"`
	IngestedAt      time.Time         `json:"ingested_at,omitempty"`
	TenantID        string            `json:"tenant_id,omitempty"`
	AgentID         string            `json:"agent_id,omitempty"`
	Source          string            `json:"source,omitempty"`
	Level           string            `json:"level"`
	Message         string            `json:"message"`
	StackID         string            `json:"stack_id,omitempty"`
	JobID           string            `json:"job_id,omitempty"`
	LeaseID         string            `json:"lease_id,omitempty"`
	Provider        string            `json:"provider,omitempty"`
	RuntimeActionID string            `json:"runtime_action_id,omitempty"`
	RuntimeTargetID string            `json:"runtime_target_id,omitempty"`
	ServerID        string            `json:"server_id,omitempty"`
	ServiceID       string            `json:"service_id,omitempty"`
	ServiceName     string            `json:"service_name,omitempty"`
	ContainerID     string            `json:"container_id,omitempty"`
	TraceID         string            `json:"trace_id,omitempty"`
	SpanID          string            `json:"span_id,omitempty"`
	RuntimeScopeKey string            `json:"runtime_scope_key,omitempty"`
	ServerScopeKey  string            `json:"server_scope_key,omitempty"`
	ServiceScopeKey string            `json:"service_scope_key,omitempty"`
	ScopeStatus     string            `json:"scope_status"`
	Fields          map[string]string `json:"fields,omitempty"`
}

type RuntimeLogQuery struct {
	TenantID        string
	AgentID         string
	StackID         string
	JobID           string
	LeaseID         string
	Provider        string
	ServiceName     string
	ServerID        string
	ServiceID       string
	RuntimeTargetID string
	Cursor          string
	MinLevel        string
	Since           time.Time
	Limit           int
}

type runtimeLogBufferConfig struct {
	Path       string
	MaxEntries int
}

type runtimeLogBuffer struct {
	mu          sync.RWMutex
	entries     []AgentLogEntry
	subscribers map[chan AgentLogEntry]RuntimeLogQuery
	maxEntries  int
	file        *os.File
	nextSeq     uint64
}

func newRuntimeLogBuffer(cfg runtimeLogBufferConfig) (*runtimeLogBuffer, error) {
	maxEntries := cfg.MaxEntries
	if maxEntries <= 0 {
		maxEntries = defaultRuntimeLogMaxEntries
	}
	buffer := &runtimeLogBuffer{
		maxEntries:  maxEntries,
		subscribers: map[chan AgentLogEntry]RuntimeLogQuery{},
	}
	path := strings.TrimSpace(cfg.Path)
	if path == "" {
		return buffer, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return nil, err
	}
	if err := buffer.load(path); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	buffer.file = file
	return buffer, nil
}

func newMemoryRuntimeLogBuffer(maxEntries int) *runtimeLogBuffer {
	buffer, _ := newRuntimeLogBuffer(runtimeLogBufferConfig{MaxEntries: maxEntries})
	return buffer
}

func (b *runtimeLogBuffer) load(path string) error {
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for scanner.Scan() {
		var entry AgentLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			continue
		}
		entry = normalizeRuntimeLogEntry(entry, time.Now().UTC())
		b.entries = append(b.entries, entry)
		if len(b.entries) > b.maxEntries {
			b.entries = b.entries[len(b.entries)-b.maxEntries:]
		}
		b.nextSeq++
	}
	return scanner.Err()
}

func (b *runtimeLogBuffer) Append(entry AgentLogEntry) AgentLogEntry {
	if b == nil {
		return normalizeRuntimeLogEntry(entry, time.Now().UTC())
	}
	now := time.Now().UTC()
	entry = normalizeRuntimeLogEntry(entry, now)

	b.mu.Lock()
	b.nextSeq++
	if entry.ID == "" {
		entry.ID = "runtime-log-" + strconv.FormatUint(b.nextSeq, 10)
	}
	b.entries = append(b.entries, entry)
	if len(b.entries) > b.maxEntries {
		b.entries = b.entries[len(b.entries)-b.maxEntries:]
	}
	if b.file != nil {
		if data, err := json.Marshal(entry); err == nil {
			_, _ = b.file.Write(append(data, '\n'))
		}
	}
	for ch, query := range b.subscribers {
		if runtimeLogMatchesQuery(entry, query) {
			select {
			case ch <- entry:
			default:
			}
		}
	}
	b.mu.Unlock()

	captureRuntimeLogIfError(entry)
	return entry
}

func (b *runtimeLogBuffer) Query(query RuntimeLogQuery) []AgentLogEntry {
	if b == nil {
		return nil
	}
	query = normalizeRuntimeLogQuery(query)
	b.mu.RLock()
	defer b.mu.RUnlock()

	out := make([]AgentLogEntry, 0)
	cursorReached := query.Cursor == ""
	for i := len(b.entries) - 1; i >= 0; i-- {
		entry := b.entries[i]
		if !cursorReached {
			if entry.ID == query.Cursor {
				cursorReached = true
			}
			continue
		}
		if !runtimeLogMatchesQuery(entry, query) {
			continue
		}
		out = append(out, entry)
		if query.Limit > 0 && len(out) >= query.Limit {
			break
		}
	}
	reverseRuntimeLogs(out)
	return out
}

func (b *runtimeLogBuffer) Subscribe(query RuntimeLogQuery, buffer int) (<-chan AgentLogEntry, func()) {
	if b == nil {
		ch := make(chan AgentLogEntry)
		close(ch)
		return ch, func() {}
	}
	if buffer <= 0 {
		buffer = 100
	}
	query = normalizeRuntimeLogQuery(query)
	ch := make(chan AgentLogEntry, buffer)
	b.mu.Lock()
	b.subscribers[ch] = query
	b.mu.Unlock()
	cancel := func() {
		b.mu.Lock()
		if _, ok := b.subscribers[ch]; ok {
			delete(b.subscribers, ch)
			close(ch)
		}
		b.mu.Unlock()
	}
	return ch, cancel
}

func (b *runtimeLogBuffer) Close() error {
	if b == nil || b.file == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	err := b.file.Close()
	b.file = nil
	return err
}

func (s *Server) appendRuntimeLog(entry AgentLogEntry) AgentLogEntry {
	if s == nil {
		return normalizeRuntimeLogEntry(entry, time.Now().UTC())
	}
	store := s.runtimeLogStore()
	return store.Append(entry)
}

// AppendRuntimeLog admits one already-authenticated runtime record into the
// shared redacted history/SSE spool.
func (s *Server) AppendRuntimeLog(entry AgentLogEntry) AgentLogEntry {
	return s.appendRuntimeLog(entry)
}

func (s *Server) runtimeLogStore() *runtimeLogBuffer {
	if s.runtimeLogs != nil {
		return s.runtimeLogs
	}
	s.agentLogsMu.Lock()
	defer s.agentLogsMu.Unlock()
	if s.runtimeLogs == nil {
		s.runtimeLogs = newMemoryRuntimeLogBuffer(defaultRuntimeLogMaxEntries)
	}
	return s.runtimeLogs
}

func (s *Server) GetRuntimeLogs(query RuntimeLogQuery) []AgentLogEntry {
	if s == nil {
		return nil
	}
	return s.runtimeLogStore().Query(query)
}

func (s *Server) SubscribeRuntimeLogs(query RuntimeLogQuery, buffer int) (<-chan AgentLogEntry, func()) {
	if s == nil {
		ch := make(chan AgentLogEntry)
		close(ch)
		return ch, func() {}
	}
	return s.runtimeLogStore().Subscribe(query, buffer)
}

func normalizeRuntimeLogEntry(entry AgentLogEntry, now time.Time) AgentLogEntry {
	if now.IsZero() {
		now = time.Now().UTC()
	}
	if entry.Timestamp.IsZero() {
		entry.Timestamp = now
	} else {
		entry.Timestamp = entry.Timestamp.UTC()
	}
	if entry.IngestedAt.IsZero() {
		entry.IngestedAt = now
	} else {
		entry.IngestedAt = entry.IngestedAt.UTC()
	}
	entry.AgentID = strings.TrimSpace(entry.AgentID)
	entry.Source = firstRuntimeLogValue(entry.Source, "runtime")
	entry.Level = normalizeRuntimeLogLevel(entry.Level)
	entry.Message = truncateRuntimeLogValue(secrets.Redact(strings.TrimSpace(entry.Message)), maxRuntimeLogMessageBytes)
	entry.Fields = normalizeRuntimeLogFields(entry.Fields)
	entry.TenantID = firstRuntimeLogValue(entry.TenantID, entry.Fields["tenant_id"], entry.Fields["tenant.id"])

	entry.StackID = firstRuntimeLogValue(entry.StackID, entry.Fields["stack_id"], entry.Fields["stack.id"])
	entry.JobID = firstRuntimeLogValue(entry.JobID, entry.Fields["job_id"], entry.Fields["job.id"])
	entry.LeaseID = firstRuntimeLogValue(entry.LeaseID, entry.Fields["lease_id"], entry.Fields["runtime_lease_id"], entry.Fields["lease.id"])
	entry.Provider = firstRuntimeLogValue(entry.Provider, entry.Fields["provider"], entry.Fields["lease_provider"], entry.Fields["simulate_provider_id"])
	entry.RuntimeActionID = firstRuntimeLogValue(entry.RuntimeActionID, entry.Fields["runtime_action_id"], entry.Fields["runtime.action_id"], entry.Fields["action_id"])
	entry.RuntimeTargetID = firstRuntimeLogValue(entry.RuntimeTargetID, entry.Fields["runtime_target_id"], entry.Fields["runtime.target_id"], entry.Fields["managed_target_ref"])
	entry.ServerID = firstRuntimeLogValue(entry.ServerID, entry.Fields["server_id"], entry.Fields["server.id"], entry.Fields["node_id"])
	entry.ServiceID = firstRuntimeLogValue(entry.ServiceID, entry.Fields["service_id"], entry.Fields["service.id"])
	entry.ServiceName = firstRuntimeLogValue(entry.ServiceName, entry.Fields["service.name"], entry.Fields["service_name"], entry.Fields["service"])
	entry.ContainerID = firstRuntimeLogValue(entry.ContainerID, entry.Fields["container.id"], entry.Fields["container_id"])
	entry.TraceID = strings.TrimSpace(entry.TraceID)
	entry.SpanID = strings.TrimSpace(entry.SpanID)
	if entry.AgentID == "" {
		entry.AgentID = firstRuntimeLogValue(entry.Fields["agent_id"], entry.Fields["agent.id"])
	}
	entry.RuntimeScopeKey = firstRuntimeLogValue(entry.RuntimeScopeKey, entry.Fields["runtime_scope_key"])
	entry.ServerScopeKey = firstRuntimeLogValue(entry.ServerScopeKey, entry.Fields["server_scope_key"], entry.ServerID)
	entry.ServiceScopeKey = firstRuntimeLogValue(entry.ServiceScopeKey, entry.Fields["service_scope_key"], entry.ServiceID)
	if entry.RuntimeScopeKey == "" {
		switch {
		case entry.RuntimeTargetID != "":
			entry.RuntimeScopeKey = "managed_target:" + entry.RuntimeTargetID
		case entry.ServerID != "":
			entry.RuntimeScopeKey = "server:" + entry.ServerID
		case entry.StackID != "":
			entry.RuntimeScopeKey = "stack:" + entry.StackID
		}
	}
	entry.ScopeStatus = "unscoped"
	if entry.TenantID != "" && entry.RuntimeScopeKey != "" {
		entry.ScopeStatus = "scoped"
	}
	return entry
}

func normalizeRuntimeLogFields(fields map[string]string) map[string]string {
	if len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	count := 0
	for key, value := range fields {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		out[key] = truncateRuntimeLogValue(secrets.Redact(strings.TrimSpace(value)), maxRuntimeLogFieldBytes)
		count++
		if count >= maxRuntimeLogFields {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func normalizeRuntimeLogLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "":
		return runtimeLogLevelInfo
	case "warning":
		return "warn"
	default:
		return level
	}
}

func normalizeRuntimeLogQuery(query RuntimeLogQuery) RuntimeLogQuery {
	query.TenantID = strings.TrimSpace(query.TenantID)
	query.AgentID = strings.TrimSpace(query.AgentID)
	query.StackID = strings.TrimSpace(query.StackID)
	query.JobID = strings.TrimSpace(query.JobID)
	query.LeaseID = strings.TrimSpace(query.LeaseID)
	query.Provider = strings.TrimSpace(query.Provider)
	query.ServiceName = strings.TrimSpace(query.ServiceName)
	query.ServerID = strings.TrimSpace(query.ServerID)
	query.ServiceID = strings.TrimSpace(query.ServiceID)
	query.RuntimeTargetID = strings.TrimSpace(query.RuntimeTargetID)
	query.Cursor = strings.TrimSpace(query.Cursor)
	query.MinLevel = normalizeRuntimeLogMinLevel(query.MinLevel)
	if query.Limit <= 0 {
		query.Limit = 200
	}
	if query.Limit > 1000 {
		query.Limit = 1000
	}
	return query
}

func normalizeRuntimeLogMinLevel(level string) string {
	if strings.TrimSpace(level) == "" {
		return ""
	}
	return normalizeRuntimeLogLevel(level)
}

func runtimeLogMatchesQuery(entry AgentLogEntry, query RuntimeLogQuery) bool {
	if !runtimeLogStringFiltersMatch(entry, query) {
		return false
	}
	if !query.Since.IsZero() && entry.Timestamp.Before(query.Since) {
		return false
	}
	if query.MinLevel != "" && runtimeLogLevelRank(entry.Level) < runtimeLogLevelRank(query.MinLevel) {
		return false
	}
	return true
}

func runtimeLogStringFiltersMatch(entry AgentLogEntry, query RuntimeLogQuery) bool {
	filters := []struct {
		want string
		got  string
	}{
		{query.TenantID, entry.TenantID},
		{query.AgentID, entry.AgentID},
		{query.StackID, entry.StackID},
		{query.JobID, entry.JobID},
		{query.LeaseID, entry.LeaseID},
		{query.Provider, entry.Provider},
		{query.ServiceName, entry.ServiceName},
		{query.ServerID, entry.ServerID},
		{query.ServiceID, entry.ServiceID},
		{query.RuntimeTargetID, entry.RuntimeTargetID},
	}
	for _, filter := range filters {
		if filter.want != "" && filter.got != filter.want {
			return false
		}
	}
	return true
}

func runtimeLogLevelRank(level string) int {
	switch normalizeRuntimeLogLevel(level) {
	case "debug":
		return 10
	case runtimeLogLevelInfo:
		return 20
	case "warn":
		return 30
	case "error":
		return 40
	case "fatal", "panic":
		return 50
	default:
		return 20
	}
}

func reverseRuntimeLogs(logs []AgentLogEntry) {
	for i, j := 0, len(logs)-1; i < j; i, j = i+1, j-1 {
		logs[i], logs[j] = logs[j], logs[i]
	}
}

func truncateRuntimeLogValue(value string, maxBytes int) string {
	if maxBytes <= 0 || len(value) <= maxBytes {
		return value
	}
	return value[:maxBytes] + fmt.Sprintf("... [truncated %d bytes]", len(value)-maxBytes)
}

func firstRuntimeLogValue(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

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
