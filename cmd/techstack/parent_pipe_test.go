package main

import (
	"context"
	"io"
	"testing"
	"time"
)

// Regression: closing the Windows shell must drain the real runtime lifecycle
// instead of killing it and leaving its embedded PostgreSQL running.
func TestParentPipeClosesRuntimeContextOnlyAfterParentExit(t *testing.T) {
	input, parent := io.Pipe()
	t.Cleanup(func() { _ = input.Close(); _ = parent.Close() })
	ctx, cancel := parentPipeContext(context.Background(), input)
	defer cancel()
	if _, err := parent.Write([]byte("still running")); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("parent input cancelled a running runtime")
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("parent exit did not cancel the runtime")
	}
}
