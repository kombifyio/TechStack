package activities

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
)

type fakeDeliverer struct {
	signals []workflow.Signal
	err     error
}

func (f *fakeDeliverer) Deliver(_ context.Context, sig workflow.Signal) error {
	f.signals = append(f.signals, sig)
	return f.err
}

func TestAgentResultSignalKey(t *testing.T) {
	if got := AgentResultSignalKey("cmd-1"); got != "agent_result:cmd-1" {
		t.Fatalf("AgentResultSignalKey = %q, want agent_result:cmd-1", got)
	}
}

func TestRoute_BuildsSignalAndMarksOK(t *testing.T) {
	fake := &fakeDeliverer{}
	r := NewResultRouter(fake)

	err := r.Route(context.Background(), CommandResult{
		CommandID: "cmd-1",
		AgentID:   "agent-7",
		ExitCode:  0,
		Stdout:    "done",
	})
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if len(fake.signals) != 1 {
		t.Fatalf("delivered %d signals, want 1", len(fake.signals))
	}
	sig := fake.signals[0]
	if sig.Key != AgentResultSignalKey("cmd-1") {
		t.Errorf("signal key = %q, want %q", sig.Key, AgentResultSignalKey("cmd-1"))
	}
	if sig.Payload["ok"] != true {
		t.Errorf("ok = %v, want true for exit 0 / no error", sig.Payload["ok"])
	}
	if sig.Payload["agent_id"] != "agent-7" || sig.Payload["command_id"] != "cmd-1" {
		t.Errorf("payload identity = %+v", sig.Payload)
	}
}

func TestRoute_OKFalseOnFailure(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  CommandResult
	}{
		{"nonzero exit", CommandResult{CommandID: "c", ExitCode: 1}},
		{"error string", CommandResult{CommandID: "c", Err: "boom"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fake := &fakeDeliverer{}
			if err := NewResultRouter(fake).Route(context.Background(), tc.res); err != nil {
				t.Fatalf("Route: %v", err)
			}
			if fake.signals[0].Payload["ok"] != false {
				t.Errorf("ok = %v, want false", fake.signals[0].Payload["ok"])
			}
		})
	}
}

func TestRoute_SwallowsNotFoundButPropagatesOtherErrors(t *testing.T) {
	// A wrapped ErrNotFound (no run awaiting) is benign and swallowed.
	notFound := &fakeDeliverer{err: fmt.Errorf("%w: nobody home", workflow.ErrNotFound)}
	if err := NewResultRouter(notFound).Route(context.Background(), CommandResult{CommandID: "c"}); err != nil {
		t.Fatalf("not-found should be swallowed, got %v", err)
	}

	// Any other delivery error propagates.
	boom := &fakeDeliverer{err: errors.New("store unavailable")}
	if err := NewResultRouter(boom).Route(context.Background(), CommandResult{CommandID: "c"}); err == nil {
		t.Fatal("non-benign delivery error should propagate")
	}
}

func TestRoute_RequiresCommandID(t *testing.T) {
	fake := &fakeDeliverer{}
	if err := NewResultRouter(fake).Route(context.Background(), CommandResult{}); err == nil {
		t.Fatal("expected error for missing command id")
	}
	if len(fake.signals) != 0 {
		t.Fatalf("no signal should be delivered for an invalid result, got %d", len(fake.signals))
	}
}

func TestRun_DrainsChannelUntilClosed(t *testing.T) {
	fake := &fakeDeliverer{}
	r := NewResultRouter(fake)
	results := make(chan CommandResult, 3)
	results <- CommandResult{CommandID: "a"}
	results <- CommandResult{CommandID: "b"}
	results <- CommandResult{CommandID: "c"}
	close(results)

	r.Run(context.Background(), results)

	if len(fake.signals) != 3 {
		t.Fatalf("routed %d results, want 3", len(fake.signals))
	}
}

func TestRun_StopsOnContextCancel(t *testing.T) {
	fake := &fakeDeliverer{}
	r := NewResultRouter(fake)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	// Canceled context: Run must return promptly without blocking on the channel.
	r.Run(ctx, make(chan CommandResult))
	if len(fake.signals) != 0 {
		t.Fatalf("no signals expected on canceled context, got %d", len(fake.signals))
	}
}
