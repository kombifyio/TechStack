package orchestrator

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/jobs"
)

func TestLifecycleBindsStoredStackKitInstance(t *testing.T) {
	for _, operation := range []string{jobs.StackKitLifecycleServiceRestart, jobs.StackKitLifecycleRemove} {
		t.Run(operation, func(t *testing.T) {
			got, err := bindStackKitLifecycleInstance(jobs.StackKitLifecycleRequest{Operation: operation, StackKitInstanceID: "caller-value"}, &orchestratorStack{stackKitInstanceID: "owner-kit"})
			if err != nil || got.StackKitInstanceID != "owner-kit" {
				t.Fatalf("bound request = %#v, error = %v", got, err)
			}
		})
	}
}
