package controlplane

import "testing"

func TestWizardProjectionIdentityCannotChangeOrRegress(t *testing.T) {
	for _, reason := range wizardRunBoundRequestReasons() {
		prior := WizardRun{Status: "failed", ErrorReason: reason, RequestSHA256: "original"}
		if wizardRunCanReplace(prior, WizardRun{Status: "failed", RequestSHA256: "different"}) {
			t.Fatalf("projection request binding changed for %s", reason)
		}
		if !wizardRunCanReplace(prior, WizardRun{Status: "failed", RequestSHA256: "original"}) {
			t.Fatalf("same-identity recovery refused for %s", reason)
		}
	}
	complete := WizardRun{Status: "completed", RequestSHA256: "original"}
	if wizardRunCanReplace(complete, WizardRun{Status: "failed", RequestSHA256: "original"}) || wizardRunCanReplace(complete, WizardRun{Status: "completed", RequestSHA256: "different"}) {
		t.Fatal("completed outcome was downgraded or rebound")
	}
	if !wizardRunCanReplace(complete, complete) {
		t.Fatal("same completed outcome was not replayable")
	}
}
