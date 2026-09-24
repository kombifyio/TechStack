package agent

import (
	"context"
	"os/exec"
)

func newStackKitProcess(ctx context.Context, binary string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, binary, args...) // #nosec G204 -- caller admits the immutable release binary and closed typed arguments.
	configureStackKitProcess(cmd)
	return cmd
}
