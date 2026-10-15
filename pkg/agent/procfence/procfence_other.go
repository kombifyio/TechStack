//go:build !linux

// Package procfence proves that a previous executor process group is gone
// before kombify Guard admits conflicting work after a restart.
package procfence

import "time"

// StartTicks is unknown off Linux.
func StartTicks(int) uint64 { return 0 }

// GroupAlive is unknown off Linux; the service manager owns process cleanup.
func GroupAlive(int) bool { return false }

// EnsureGone relies on the service manager to stop the whole process tree.
func EnsureGone(int, int, uint64, time.Duration) error { return nil }
