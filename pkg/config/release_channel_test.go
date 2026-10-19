package config

import "testing"

func TestPreviewReportsOnlyInstalledScopeAndKeepsSharedWorkOnStable(t *testing.T) {
	t.Setenv(EnvReleaseChannel, "preview")
	t.Setenv("TECHSTACK_STABLE_ORIGIN", "https://techstack.kombify.io/")
	if RunsSharedWork() || AgentOriginOverride() != "https://techstack.kombify.io" {
		t.Fatal("preview ran shared work or kept agents on itself")
	}
	execution := NewPreviewExecution()
	if scope := execution.Snapshot(); scope["http"] != "unqualified" || scope["queues"] != "unqualified" {
		t.Fatalf("configuration claimed installed scope: %v", scope)
	}
	execution.HTTPInstalled()
	execution.StartupSuppressed()
	if scope := execution.Snapshot(); scope["http"] != "opted-in-users" || scope["queues"] != "unqualified" {
		t.Fatalf("scope before partitioned background = %v", scope)
	}
	execution.BackgroundPartitioned()
	if scope := execution.Snapshot(); scope["startup"] != "verify-only" || scope["scheduled"] != "owned-tenants" || scope["queues"] != "owned-tenants" {
		t.Fatalf("scope = %v", scope)
	}
	t.Setenv(EnvReleaseChannel, "stable")
	if !RunsSharedWork() || AgentOriginOverride() != "" || NewPreviewExecution() != nil {
		t.Fatal("stable changed")
	}
}
