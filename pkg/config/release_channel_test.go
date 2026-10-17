package config

import "testing"

func TestPreviewRestrictsPrincipalsAndBackgroundExecution(t *testing.T) {
	t.Setenv(EnvReleaseChannel, "preview")
	t.Setenv("TEST_RIL_PRO_USER_ID", "")
	if PreviewSubjectAdmitted("auth0|fixture") || BackgroundExecutionEnabled() {
		t.Fatal("an unconfigured preview admitted execution")
	}
	t.Setenv("TEST_RIL_PRO_USER_ID", "auth0|fixture")
	if !PreviewSubjectAdmitted("auth0|fixture") || PreviewSubjectAdmitted("auth0|customer") || PreviewSubjectAdmitted("") {
		t.Fatal("preview admission did not retain the exact canonical fixture")
	}
	execution := NewPreviewExecution()
	if scope := execution.Snapshot(); scope["http"] != "unqualified" || scope["queues"] != "unqualified" {
		t.Fatalf("configuration claimed installed scope: %v", scope)
	}
	execution.HTTPInstalled()
	execution.StartupSuppressed()
	execution.BackgroundSuppressed()
	if scope := execution.Snapshot(); scope["http"] != "shared-test-identities" || scope["scheduled"] != "disabled" || scope["queues"] != "disabled" {
		t.Fatalf("scope = %v", scope)
	}
	t.Setenv(EnvReleaseChannel, "stable")
	if !PreviewSubjectAdmitted("auth0|customer") || !BackgroundExecutionEnabled() || NewPreviewExecution() != nil {
		t.Fatal("stable admission changed")
	}
}
