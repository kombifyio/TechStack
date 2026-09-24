package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type stackKitProcessReadyOutput struct {
	buffer bytes.Buffer
	once   sync.Once
	ready  chan struct{}
}

func (o *stackKitProcessReadyOutput) Write(p []byte) (int, error) {
	n, err := o.buffer.Write(p)
	if bytes.Contains(o.buffer.Bytes(), []byte("ready")) {
		o.once.Do(func() { close(o.ready) })
	}
	return n, err
}

// Exercise the same process constructor as Execute at the Linux OS boundary.
// No fake release admission is needed: the observed effect is whether a child
// can mutate after its parent command has been cancelled.
func TestStackKitCancellationStopsDescendantMutation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	marker := filepath.Join(t.TempDir(), "escaped")
	output := &stackKitProcessReadyOutput{ready: make(chan struct{})}
	cmd := newStackKitProcess(ctx, "sh", "-c", `(while kill -0 $$ 2>/dev/null; do sleep 0.01; done; printf escaped > "$1") & echo ready; wait`, "sh", marker)
	cmd.Stdout, cmd.Stderr = output, output
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill() })
	select {
	case <-output.ready:
	case <-time.After(5 * time.Second):
		t.Fatal("process fixture did not become ready")
	}
	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled command reported success")
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("descendant mutated after command cancellation: %v", err)
	}
}
