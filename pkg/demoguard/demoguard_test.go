package demoguard

import "testing"

func TestIsDemoUserAcceptsCanonicalJSONSubjectList(t *testing.T) {
	t.Setenv(EnvDemoUserIDs, `["auth0|demo-subject","auth0|second"]`)

	if !IsDemoUser("auth0|demo-subject") {
		t.Fatal("JSON-encoded demo subject was not recognized")
	}
	if !IsDemoUser("auth0|second") {
		t.Fatal("second JSON-encoded demo subject was not recognized")
	}
	if IsDemoUser("auth0|not-demo") {
		t.Fatal("unconfigured subject was recognized as demo")
	}
}

func TestIsDemoUserRetainsCSVCompatibility(t *testing.T) {
	t.Setenv(EnvDemoUserIDs, "auth0|demo-subject, auth0|second")

	if !IsDemoUser("auth0|second") {
		t.Fatal("CSV demo subject was not recognized")
	}
}
