//go:build !linux

package agent

import "os/exec"

// Non-Linux command execution retains its existing cancellation behavior.
func configureStackKitProcess(_ *exec.Cmd) {}
