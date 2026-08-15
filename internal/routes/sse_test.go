// Package routes provides unit tests for SSE (Server-Sent Events) endpoints.
package routes

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pocketbase/pocketbase/core"
)

// =============================================================================
// Test Helpers
// =============================================================================

// mockResponseWriter implements http.ResponseWriter and http.Flusher for SSE tests.
type mockResponseWriter struct {
	headers    http.Header
	body       bytes.Buffer
	statusCode int
	flushed    int
}

func newMockResponseWriter() *mockResponseWriter {
	return &mockResponseWriter{
		headers:    make(http.Header),
		statusCode: 0,
	}
}

func (m *mockResponseWriter) Header() http.Header {
	return m.headers
}

func (m *mockResponseWriter) Write(b []byte) (int, error) {
	return m.body.Write(b)
}

func (m *mockResponseWriter) WriteHeader(statusCode int) {
	m.statusCode = statusCode
}

func (m *mockResponseWriter) Flush() {
	m.flushed++
}

// parseSSEEvents parses SSE formatted data into individual events.
func parseSSEEvents(data string) []sseEvent {
	var events []sseEvent
	scanner := bufio.NewScanner(strings.NewReader(data))

	var currentEvent sseEvent
	for scanner.Scan() {
		line := scanner.Text()

		if line == "" {
			// Empty line marks end of event
			if currentEvent.EventType != "" || currentEvent.Data != "" {
				events = append(events, currentEvent)
				currentEvent = sseEvent{}
			}
			continue
		}

		if strings.HasPrefix(line, "event: ") {
			currentEvent.EventType = strings.TrimPrefix(line, "event: ")
		} else if strings.HasPrefix(line, "data: ") {
			currentEvent.Data = strings.TrimPrefix(line, "data: ")
		}
	}

	// Add last event if exists
	if currentEvent.EventType != "" || currentEvent.Data != "" {
		events = append(events, currentEvent)
	}

	return events
}

// sseEvent represents a parsed SSE event.
type sseEvent struct {
	EventType string
	Data      string
}

// =============================================================================
// sendSSEEvent Tests
// =============================================================================

func TestSendSSEEvent_FormatsSingleEvent(t *testing.T) {
	tests := []struct {
		name     string
		event    string
		data     interface{}
		wantErr  bool
		contains string
	}{
		{
			name:     "simple string data",
			event:    "message",
			data:     map[string]string{"text": "hello"},
			wantErr:  false,
			contains: `event: message`,
		},
		{
			name:     "progress event",
			event:    "progress",
			data:     map[string]int{"percent": 50},
			wantErr:  false,
			contains: `event: progress`,
		},
		{
			name:     "complex object",
			event:    "state",
			data:     JobProgress{ID: "job-1", State: "running", Progress: 75, CurrentStep: "deploying"},
			wantErr:  false,
			contains: `"id":"job-1"`,
		},
		{
			name:     "done event",
			event:    "done",
			data:     map[string]string{"status": "completed"},
			wantErr:  false,
			contains: `event: done`,
		},
		{
			name:     "error event",
			event:    "error",
			data:     map[string]string{"message": "something went wrong"},
			wantErr:  false,
			contains: `"message":"something went wrong"`,
		},
		{
			name:     "timeout event",
			event:    "timeout",
			data:     map[string]string{"message": "Connection timeout after 10 minutes"},
			wantErr:  false,
			contains: `event: timeout`,
		},
		{
			name:     "ping event",
			event:    "ping",
			data:     map[string]string{"ts": "2025-01-08T12:00:00Z"},
			wantErr:  false,
			contains: `event: ping`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newMockResponseWriter()

			err := sendSSEEvent(w, tt.event, tt.data)

			if (err != nil) != tt.wantErr {
				t.Errorf("sendSSEEvent() error = %v, wantErr %v", err, tt.wantErr)
				return
			}

			output := w.body.String()

			// Check SSE format: event: <type>\ndata: <json>\n\n
			if !strings.Contains(output, tt.contains) {
				t.Errorf("sendSSEEvent() output does not contain %q, got: %s", tt.contains, output)
			}

			// Verify ends with double newline
			if !strings.HasSuffix(output, "\n\n") {
				t.Errorf("sendSSEEvent() output should end with double newline, got: %q", output)
			}

			// Parse and verify JSON data
			events := parseSSEEvents(output)
			if len(events) != 1 {
				t.Errorf("expected 1 event, got %d", len(events))
				return
			}

			if events[0].EventType != tt.event {
				t.Errorf("expected event type %q, got %q", tt.event, events[0].EventType)
			}

			// Verify data is valid JSON
			var parsed interface{}
			if err := json.Unmarshal([]byte(events[0].Data), &parsed); err != nil {
				t.Errorf("event data is not valid JSON: %v", err)
			}
		})
	}
}

func TestSendSSEEvent_HandlesInvalidJSON(t *testing.T) {
	w := newMockResponseWriter()

	// Create a type that will fail JSON marshaling
	badData := make(chan int)

	err := sendSSEEvent(w, "test", badData)
	if err == nil {
		t.Error("sendSSEEvent() should return error for invalid JSON data")
	}
}

// =============================================================================
// SSE Headers Tests
// =============================================================================

func TestSSE_RequiredHeaders(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		expected string
	}{
		{
			name:     "Content-Type",
			header:   "Content-Type",
			expected: "text/event-stream",
		},
		{
			name:     "Cache-Control",
			header:   "Cache-Control",
			expected: "no-cache",
		},
		{
			name:     "Connection",
			header:   "Connection",
			expected: "keep-alive",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newMockResponseWriter()

			// Simulate what the SSE handlers do
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			if got := w.Header().Get(tt.header); got != tt.expected {
				t.Errorf("header %s = %q, want %q", tt.header, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// isTerminalState Tests
// =============================================================================

func TestIsTerminalState(t *testing.T) {
	tests := []struct {
		state    string
		expected bool
	}{
		{"completed", true},
		{"failed", true},
		{"canceled", true},
		{"running", false},
		{"pending", false},
		{"queued", false},
		{"", false},
		{"COMPLETED", false}, // Case-sensitive
	}

	for _, tt := range tests {
		t.Run(tt.state, func(t *testing.T) {
			if got := isTerminalState(tt.state); got != tt.expected {
				t.Errorf("isTerminalState(%q) = %v, want %v", tt.state, got, tt.expected)
			}
		})
	}
}

// =============================================================================
// JobProgress Struct Tests
// =============================================================================

func TestJobProgress_JSONSerialization(t *testing.T) {
	tests := []struct {
		name     string
		progress JobProgress
		contains []string
	}{
		{
			name: "running job",
			progress: JobProgress{
				ID:          "job-123",
				State:       "running",
				Progress:    50,
				Step:        "docker_ready",
				CurrentStep: "installing packages",
				Message:     "Installing Docker",
			},
			contains: []string{`"id":"job-123"`, `"state":"running"`, `"progress":50`, `"step":"docker_ready"`, `"current_step":"installing packages"`, `"message":"Installing Docker"`},
		},
		{
			name: "failed job with error",
			progress: JobProgress{
				ID:          "job-456",
				State:       "failed",
				Progress:    25,
				CurrentStep: "failed at step",
				Error:       "connection timeout",
			},
			contains: []string{`"error":"connection timeout"`},
		},
		{
			name: "completed job",
			progress: JobProgress{
				ID:       "job-789",
				State:    "completed",
				Progress: 100,
			},
			contains: []string{`"progress":100`, `"state":"completed"`},
		},
		{
			name: "omit empty error",
			progress: JobProgress{
				ID:       "job-000",
				State:    "running",
				Progress: 10,
			},
			contains: []string{}, // We'll check for absence of error field
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data, err := json.Marshal(tt.progress)
			if err != nil {
				t.Fatalf("failed to marshal JobProgress: %v", err)
			}

			jsonStr := string(data)

			for _, expected := range tt.contains {
				if !strings.Contains(jsonStr, expected) {
					t.Errorf("JSON output missing %q, got: %s", expected, jsonStr)
				}
			}

			// For the omit empty test, verify error is not present
			if tt.name == "omit empty error" {
				if strings.Contains(jsonStr, `"error":`) {
					t.Errorf("expected error field to be omitted when empty, got: %s", jsonStr)
				}
			}
		})
	}
}

// =============================================================================
// SSE Event Format Tests
// =============================================================================

func TestSSEEventFormat_MultipleEvents(t *testing.T) {
	w := newMockResponseWriter()

	// Send multiple events
	events := []struct {
		eventType string
		data      interface{}
	}{
		{"progress", map[string]int{"percent": 25}},
		{"progress", map[string]int{"percent": 50}},
		{"progress", map[string]int{"percent": 75}},
		{"done", map[string]string{"status": "completed"}},
	}

	for _, e := range events {
		if err := sendSSEEvent(w, e.eventType, e.data); err != nil {
			t.Fatalf("sendSSEEvent failed: %v", err)
		}
	}

	output := w.body.String()
	parsedEvents := parseSSEEvents(output)

	if len(parsedEvents) != len(events) {
		t.Errorf("expected %d events, got %d", len(events), len(parsedEvents))
	}

	// Verify event order and types
	for i, expected := range events {
		if i < len(parsedEvents) {
			if parsedEvents[i].EventType != expected.eventType {
				t.Errorf("event %d: expected type %q, got %q", i, expected.eventType, parsedEvents[i].EventType)
			}
		}
	}
}

func TestSSEEventFormat_SpecialCharacters(t *testing.T) {
	tests := []struct {
		name string
		data interface{}
	}{
		{
			name: "newlines in message",
			data: map[string]string{"message": "line1\nline2\nline3"},
		},
		{
			name: "unicode characters",
			data: map[string]string{"message": "Hëllo Wörld 你好 🚀"},
		},
		{
			name: "quotes and escapes",
			data: map[string]string{"message": `He said "hello" and 'goodbye'`},
		},
		{
			name: "special JSON characters",
			data: map[string]string{"message": "test\\path\\to\\file"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := newMockResponseWriter()

			err := sendSSEEvent(w, "message", tt.data)
			if err != nil {
				t.Fatalf("sendSSEEvent() failed: %v", err)
			}

			events := parseSSEEvents(w.body.String())
			if len(events) != 1 {
				t.Fatalf("expected 1 event, got %d", len(events))
			}

			// Verify the data can be parsed back
			var parsed map[string]string
			if err := json.Unmarshal([]byte(events[0].Data), &parsed); err != nil {
				t.Errorf("failed to parse event data: %v", err)
			}
		})
	}
}

// =============================================================================
// SSE Connection Simulation Tests
// =============================================================================

func TestSSE_FlushBehavior(t *testing.T) {
	w := newMockResponseWriter()

	// Simulate SSE setup
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	// Send event and flush
	sendSSEEvent(w, "test", map[string]string{"key": "value"})
	w.Flush()

	if w.flushed != 1 {
		t.Errorf("expected 1 flush, got %d", w.flushed)
	}

	// Send more events
	sendSSEEvent(w, "test", map[string]string{"key": "value2"})
	w.Flush()
	sendSSEEvent(w, "done", map[string]string{"status": "ok"})
	w.Flush()

	if w.flushed != 3 {
		t.Errorf("expected 3 flushes, got %d", w.flushed)
	}
}

func TestSSE_StatusCodeOK(t *testing.T) {
	w := newMockResponseWriter()

	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)

	if w.statusCode != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.statusCode)
	}
}

// =============================================================================
// Context Cancellation Simulation Tests
// =============================================================================

func TestSSE_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	// Simulate the context being canceled
	done := make(chan bool)

	go func() {
		select {
		case <-ctx.Done():
			done <- true
		case <-time.After(1 * time.Second):
			done <- false
		}
	}()

	// Cancel context (simulates client disconnect)
	cancel()

	if !<-done {
		t.Error("context cancellation was not detected")
	}
}

func TestSSE_TimeoutHandling(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	select {
	case <-ctx.Done():
		// Expected timeout
		if ctx.Err() != context.DeadlineExceeded {
			t.Errorf("expected DeadlineExceeded, got %v", ctx.Err())
		}
	case <-time.After(100 * time.Millisecond):
		t.Error("timeout was not triggered")
	}
}

// =============================================================================
// HTTP Request/Response Integration Tests
// =============================================================================

func TestSSE_HTTPTestRecorder(t *testing.T) {
	// Use httptest.ResponseRecorder to verify HTTP behavior
	rec := httptest.NewRecorder()

	// Set SSE headers as handlers do
	rec.Header().Set("Content-Type", "text/event-stream")
	rec.Header().Set("Cache-Control", "no-cache")
	rec.Header().Set("Connection", "keep-alive")
	rec.WriteHeader(http.StatusOK)

	// Write SSE event
	fmt.Fprintf(rec, "event: test\ndata: {\"key\":\"value\"}\n\n")

	result := rec.Result()
	defer result.Body.Close()

	// Verify headers
	if ct := result.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want %q", ct, "text/event-stream")
	}

	if cc := result.Header.Get("Cache-Control"); cc != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", cc, "no-cache")
	}

	if conn := result.Header.Get("Connection"); conn != "keep-alive" {
		t.Errorf("Connection = %q, want %q", conn, "keep-alive")
	}

	// Verify status code
	if result.StatusCode != http.StatusOK {
		t.Errorf("StatusCode = %d, want %d", result.StatusCode, http.StatusOK)
	}

	// Verify body content
	body := rec.Body.String()
	if !strings.Contains(body, "event: test") {
		t.Errorf("body missing event type, got: %s", body)
	}
	if !strings.Contains(body, `data: {"key":"value"}`) {
		t.Errorf("body missing data, got: %s", body)
	}
}

// =============================================================================
// Event Types Tests
// =============================================================================

func TestSSE_EventTypes(t *testing.T) {
	// Test all event types used in the codebase
	eventTypes := []string{
		"progress", // jobs_sse.go
		"done",     // jobs_sse.go
		"error",    // jobs_sse.go
		"timeout",  // jobs_sse.go
		"history",  // agents.go
		"ping",     // agents.go
		"log",      // agents.go
	}

	for _, eventType := range eventTypes {
		t.Run(eventType, func(t *testing.T) {
			w := newMockResponseWriter()

			err := sendSSEEvent(w, eventType, map[string]string{"type": eventType})
			if err != nil {
				t.Fatalf("sendSSEEvent(%q) failed: %v", eventType, err)
			}

			output := w.body.String()
			expected := fmt.Sprintf("event: %s", eventType)
			if !strings.Contains(output, expected) {
				t.Errorf("output missing %q, got: %s", expected, output)
			}
		})
	}
}

// =============================================================================
// Payload Structure Tests for Different SSE Endpoints
// =============================================================================

func TestSSE_JobStreamPayload(t *testing.T) {
	// Test the payload structure expected by /api/v1/jobs/{id}/stream
	progress := JobProgress{
		ID:          "test-job-id",
		State:       "running",
		Progress:    42,
		Step:        "stackkit_prepare",
		CurrentStep: "Applying configuration",
		Message:     "Preparing managed VPS",
		Error:       "",
	}

	data, err := json.Marshal(progress)
	if err != nil {
		t.Fatalf("failed to marshal: %v", err)
	}

	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	// Verify expected fields exist
	expectedFields := []string{"id", "state", "progress", "step", "current_step", "message"}
	for _, field := range expectedFields {
		if _, ok := parsed[field]; !ok {
			t.Errorf("missing expected field: %s", field)
		}
	}
}

func TestJobRecordDetailsIncludesMachineStepAndErrorDetails(t *testing.T) {
	collection := core.NewBaseCollection("jobs")
	collection.Fields.Add(
		&core.TextField{Name: "type"},
		&core.TextField{Name: "state"},
		&core.NumberField{Name: "progress"},
		&core.TextField{Name: "step"},
		&core.TextField{Name: "current_step"},
		&core.TextField{Name: "message"},
		&core.TextField{Name: "error"},
		&core.TextField{Name: "error_details"},
		&core.TextField{Name: "error_message"},
		&core.JSONField{Name: "result"},
		&core.JSONField{Name: "logs"},
	)
	job := core.NewRecord(collection)
	job.Id = "job-123"
	job.Set("type", "provision")
	job.Set("state", "running")
	job.Set("progress", 40)
	job.Set("step", "unify_services")
	job.Set("current_step", "Identifying services")
	job.Set("error_details", "details for troubleshooting")
	job.Set("result", map[string]any{"stack_id": "stack-123"})

	got := jobRecordDetails(job)

	if got["step"] != "unify_services" {
		t.Fatalf("step = %v, want machine-readable step id", got["step"])
	}
	if got["message"] != "Identifying services" {
		t.Fatalf("message = %v, want current_step fallback", got["message"])
	}
	if got["error_details"] != "details for troubleshooting" {
		t.Fatalf("error_details = %v", got["error_details"])
	}
	result := map[string]any{}
	switch value := got["result"].(type) {
	case map[string]any:
		result = value
	case []byte:
		_ = json.Unmarshal(value, &result)
	case string:
		_ = json.Unmarshal([]byte(value), &result)
	default:
		encoded, _ := json.Marshal(value)
		_ = json.Unmarshal(encoded, &result)
	}
	if result["stack_id"] != "stack-123" {
		t.Fatalf("result = %v", got["result"])
	}
}

func TestJobListResponseFiltersOwnedJobsAndPaginates(t *testing.T) {
	collection := core.NewBaseCollection("jobs")
	collection.Fields.Add(
		&core.TextField{Name: "type"},
		&core.TextField{Name: "state"},
		&core.TextField{Name: "stack_id"},
		&core.NumberField{Name: "progress"},
		&core.TextField{Name: "current_step"},
	)
	makeJob := func(id, stackID, jobType, state, step string) *core.Record {
		job := core.NewRecord(collection)
		job.Id = id
		job.Set("stack_id", stackID)
		job.Set("type", jobType)
		job.Set("state", state)
		job.Set("current_step", step)
		return job
	}

	records := []*core.Record{
		makeJob("job-1", "stack-owned", "provision", "running", "Installing services"),
		makeJob("job-2", "stack-foreign", "provision", "running", "Installing services"),
		makeJob("job-3", "stack-owned", "destroy", "completed", "Destroying"),
		makeJob("job-4", "stack-owned", "provision", "failed", "Failed install"),
	}
	filtered := filterJobRecords(records, map[string]bool{"stack-owned": true}, jobListQuery{
		StackID: "stack-owned",
		Type:    "provision",
		Search:  "install",
		Page:    1,
		PerPage: 1,
	})
	payload := jobListResponse(filtered, jobListQuery{Page: 1, PerPage: 1})

	if payload.TotalItems != 2 {
		t.Fatalf("TotalItems = %d, want 2", payload.TotalItems)
	}
	if payload.TotalPages != 2 {
		t.Fatalf("TotalPages = %d, want 2", payload.TotalPages)
	}
	if len(payload.Items) != 1 {
		t.Fatalf("items len = %d, want 1", len(payload.Items))
	}
	if payload.Items[0]["id"] != "job-1" {
		t.Fatalf("first item = %v, want job-1", payload.Items[0]["id"])
	}
}

// TestSSE_SimulationStreamPayload removed (cleanup plan 2026-04-27 phase 3.1):
// /api/v1/simulations/{id}/stream no longer exists. Simulation belongs to kombify-Sim.

func TestSSE_AgentLogStreamPayload(t *testing.T) {
	// Test payload structure for /api/v1/agents/{id}/logs/stream
	// History event
	historyPayload := []map[string]interface{}{
		{
			"timestamp": "2025-01-08T12:00:00Z",
			"level":     "info",
			"message":   "Agent started",
		},
		{
			"timestamp": "2025-01-08T12:00:01Z",
			"level":     "debug",
			"message":   "Heartbeat sent",
		},
	}

	data, err := json.Marshal(historyPayload)
	if err != nil {
		t.Fatalf("failed to marshal history: %v", err)
	}

	var parsed []map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	if len(parsed) != 2 {
		t.Errorf("expected 2 log entries, got %d", len(parsed))
	}

	// Log event (single entry)
	logEntry := map[string]interface{}{
		"timestamp": "2025-01-08T12:00:02Z",
		"level":     "info",
		"message":   "Command received",
	}

	data, err = json.Marshal(logEntry)
	if err != nil {
		t.Fatalf("failed to marshal log entry: %v", err)
	}

	var parsedEntry map[string]interface{}
	if err := json.Unmarshal(data, &parsedEntry); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}

	expectedFields := []string{"timestamp", "level", "message"}
	for _, field := range expectedFields {
		if _, ok := parsedEntry[field]; !ok {
			t.Errorf("missing expected field: %s", field)
		}
	}
}

// =============================================================================
// Error Handling Tests
// =============================================================================

func TestSSE_ErrorEventFormat(t *testing.T) {
	w := newMockResponseWriter()

	errorPayload := map[string]string{
		"message": "Unauthorized or job not found",
	}

	err := sendSSEEvent(w, "error", errorPayload)
	if err != nil {
		t.Fatalf("sendSSEEvent failed: %v", err)
	}

	events := parseSSEEvents(w.body.String())
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].EventType != "error" {
		t.Errorf("expected event type 'error', got %q", events[0].EventType)
	}

	var parsed map[string]string
	if err := json.Unmarshal([]byte(events[0].Data), &parsed); err != nil {
		t.Fatalf("failed to parse error data: %v", err)
	}

	if parsed["message"] != "Unauthorized or job not found" {
		t.Errorf("unexpected error message: %s", parsed["message"])
	}
}

func TestSSE_TimeoutEventFormat(t *testing.T) {
	w := newMockResponseWriter()

	timeoutPayload := map[string]string{
		"message": "Connection timeout after 10 minutes",
	}

	err := sendSSEEvent(w, "timeout", timeoutPayload)
	if err != nil {
		t.Fatalf("sendSSEEvent failed: %v", err)
	}

	events := parseSSEEvents(w.body.String())
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}

	if events[0].EventType != "timeout" {
		t.Errorf("expected event type 'timeout', got %q", events[0].EventType)
	}
}

// =============================================================================
// Benchmark Tests
// =============================================================================

func BenchmarkSendSSEEvent(b *testing.B) {
	w := newMockResponseWriter()
	data := map[string]interface{}{
		"id":       "test-id",
		"state":    "running",
		"progress": 50,
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sendSSEEvent(w, "progress", data)
	}
}

func BenchmarkParseSSEEvents(b *testing.B) {
	sseData := `event: progress
data: {"percent":25}

event: progress
data: {"percent":50}

event: progress
data: {"percent":75}

event: done
data: {"status":"completed"}

`
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		parseSSEEvents(sseData)
	}
}

func BenchmarkJobProgressMarshal(b *testing.B) {
	progress := JobProgress{
		ID:          "benchmark-job",
		State:       "running",
		Progress:    50,
		CurrentStep: "benchmark step",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		json.Marshal(progress)
	}
}
