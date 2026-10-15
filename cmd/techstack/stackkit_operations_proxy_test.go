package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeexecutor"
)

// The root-run StackKits process reaches the control plane only through the
// daemon: the daemon's credential signs the request, an escape from the enrolled
// stack is refused before any HTTP, and the credential never appears in what the
// privileged side receives.
func TestOperationsProxyKeepsTheCredentialInTheDaemon(t *testing.T) {
	var authorization string
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls++
		authorization = request.Header.Get("Authorization")
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(stackKitOperationsResponseEnvelope{
			SchemaVersion: stackKitOperationsResponseSchema, ChannelRef: stackKitOperationsChannelRef, Outcome: runtimeexecutor.ExecutionOutcome{},
		})
	}))
	defer server.Close()
	enrollment, err := loadAgentEnrollment(writeOperationsEnrollment(t, server.URL))
	if err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(t.TempDir(), "ops.sock")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = serveStackKitOperationsProxy(ctx, socket, enrollment, server.Client(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if info, statErr := os.Stat(socket); statErr == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the operations socket did not appear")
		}
		time.Sleep(10 * time.Millisecond)
	}

	var output bytes.Buffer
	if err := proxyStackKitOperationsProcess(ctx, bytes.NewReader(encodeOperationsRequest(t, "stack-1")), &output, socket); err != nil {
		t.Fatal(err)
	}
	if authorization != "Bearer secret-token" || !strings.Contains(output.String(), stackKitOperationsResponseSchema) {
		t.Fatalf("authorization = %q output = %s", authorization, output.String())
	}
	if strings.Contains(output.String(), "secret-token") {
		t.Fatal("the credential reached the privileged side")
	}

	calls = 0
	escaped := operationsRequest("stack-1")
	escaped.Request.BackupTargetBindings[0].StackID = "other-stack"
	payload, _ := json.Marshal(escaped)
	err = proxyStackKitOperationsProcess(ctx, bytes.NewReader(payload), &bytes.Buffer{}, socket)
	if err == nil || !strings.Contains(err.Error(), "escaped") || calls != 0 {
		t.Fatalf("a request outside the enrolled stack was not refused before HTTP: err=%v calls=%d", err, calls)
	}
}
