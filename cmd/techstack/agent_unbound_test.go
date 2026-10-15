package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// An image must remain inert until the instance installer confirms enrollment,
// and then use that installer's key rather than replacing it with a cached one.
func TestUnboundGuardWaitsForConfirmedInstanceIdentity(t *testing.T) {
	requests := make(chan *guardidentity.VerifiedProof, 8)
	failures := make(chan error, 8)
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proof, err := guardidentity.VerifyProof(r.Header.Get(guardidentity.ProofHeader), guardidentity.ProofExpectation{
			Method: r.Method, URLs: []string{"http://" + r.Host + r.URL.Path},
		})
		if err != nil {
			failures <- err
		} else if r.Header.Get("Authorization") != "Bearer instance-fixture" {
			failures <- fmt.Errorf("unexpected instance credential")
		} else {
			requests <- proof
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer control.Close()
	state := t.TempDir()
	enrollment := filepath.Join(t.TempDir(), "enrollment.json")
	cfg, err := parseAgentModeConfig([]string{"--transport=https", "--wait-for-enrollment", "--enrollment-file=" + enrollment, "--state-dir=" + state})
	if err != nil {
		t.Fatal(err)
	}
	progress := make(chan struct{}, 8)
	cfg.progress = func() {
		select {
		case progress <- struct{}{}:
		default:
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- runHTTPAgentMode(ctx, cfg, slog.New(slog.NewTextHandler(io.Discard, nil))) }()
	awaitProgress := func() {
		t.Helper()
		select {
		case <-progress:
		case err := <-done:
			t.Fatalf("waiting Guard exited: %v", err)
		case <-ctx.Done():
			t.Fatal("waiting Guard stopped making progress")
		}
	}
	awaitProgress()
	installer, err := hostidentity.Open(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := installer.EnsureKey(); err != nil {
		t.Fatal(err)
	}
	if _, err := installer.BeginAttempt(); err != nil {
		t.Fatal(err)
	}
	identity := installer.Record().KeyThumbprint
	data, err := json.Marshal(agentEnrollment{RuntimeAgentID: "instance", AgentToken: "instance-fixture",
		HeartbeatURL: control.URL + "/heartbeat", InventoryURL: control.URL + "/inventory",
		IdentityTokenURL: control.URL + "/identity/token"})
	if err != nil {
		t.Fatal(err)
	}
	if err := hostidentity.WriteFileAtomic(enrollment, data, 0o600); err != nil {
		t.Fatal(err)
	}
	awaitProgress()
	select {
	case <-requests:
		t.Fatal("Guard authenticated before instance enrollment was confirmed")
	case err := <-failures:
		t.Fatal(err)
	default:
	}
	if err := installer.SetState(hostidentity.StateBound, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case proof := <-requests:
		if proof.Thumbprint != identity {
			t.Fatal("Guard replaced the installer's identity")
		}
	case err := <-failures:
		t.Fatal(err)
	case err := <-done:
		t.Fatalf("Guard exited before authenticated identity handoff: %v", err)
	case <-ctx.Done():
		t.Fatal("Guard did not resume from instance enrollment")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Guard did not stop on cancellation")
	}
}
