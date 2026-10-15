package jobs

import (
	"testing"
)

func TestStackKitSpecBytesRefusesV1DocumentWithUseCases(t *testing.T) {
	_, err := stackKitSpecBytesForPayload(map[string]interface{}{
		"name":     "local-stack",
		"stackkit": "basement-kit",
		"useCases": []interface{}{"photos"},
		"mode":     "simple",
	})
	if err == nil {
		t.Fatal("mixed v1+useCases payload must not become the CLI handoff")
	}
}
