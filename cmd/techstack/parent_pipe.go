package main

import (
	"context"
	"io"
)

const parentPipeShutdownEnv = "TECHSTACK_SHUTDOWN_ON_STDIN_CLOSE"

// A supervising desktop owns the inherited stdin pipe. EOF, including when
// the desktop exits unexpectedly, follows the existing runtime shutdown path.
// This is opt-in so ordinary CLI stdin retains its existing meaning.
func parentPipeContext(parent context.Context, input io.Reader) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	go func() {
		_, _ = io.Copy(io.Discard, input)
		cancel()
	}()
	return ctx, cancel
}
