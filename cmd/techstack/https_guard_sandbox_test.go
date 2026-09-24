package main

import (
	"strings"
	"testing"
)

func TestRewriteHTTPSGuardSandboxUnitRepairsStackKitsHostAuthority(t *testing.T) {
	const unit = `[Service]
NoNewPrivileges=yes
ProtectSystem=true
ReadWritePaths=/etc
`
	got, changed := rewriteHTTPSGuardSandboxUnit(unit)
	if !changed {
		t.Fatal("expected the HTTPS Guard unit to change")
	}
	if !strings.Contains(got, "NoNewPrivileges=no") {
		t.Fatalf("rewritten unit = %q, want NoNewPrivileges=no", got)
	}
	if strings.Contains(got, "NoNewPrivileges=yes") {
		t.Fatal("rewritten unit still forbids privilege dropping")
	}
	if !strings.Contains(got, "ReadWritePaths=/etc -/home/kombify/.ssh") {
		t.Fatalf("rewritten unit = %q, want execution-channel write custody", got)
	}
	if !strings.Contains(got, "ProtectSystem=no") || strings.Contains(got, "ProtectSystem=true") {
		t.Fatalf("rewritten unit = %q, want a writable /usr for StackKits package installs", got)
	}
}

func TestRewriteHTTPSGuardSandboxUnitIsIdempotentWhenAlreadyRelaxed(t *testing.T) {
	const unit = "[Service]\nNoNewPrivileges=no\nProtectSystem=no\nReadWritePaths=/etc -/home/kombify/.ssh\n"
	got, changed := rewriteHTTPSGuardSandboxUnit(unit)
	if changed || got != unit {
		t.Fatalf("got changed=%v unit=%q", changed, got)
	}
}
