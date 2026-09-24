package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestDeviceRevertConsumesPrepareJSON(t *testing.T) {
	const receipt = `{"schemaVersion":"techstack.device-readiness/v1","resolutionId":"clock-set-from-operator","succeeded":true}`
	const report = `{"schemaVersion":"techstack.device-readiness/v1","readiness":"repairable"}`
	for name, content := range map[string]string{
		"single receipt": receipt,
		"receipt array":  "[" + receipt + "]",
		"prepare stream": report + "\n[" + receipt + "]\n" + report,
	} {
		t.Run(name, func(t *testing.T) {
			file := filepath.Join(t.TempDir(), "prepare.json")
			if err := os.WriteFile(file, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			// Exercise the command's receipt-file boundary directly: this clock
			// receipt has no reversible files, so no SSH session is needed.
			if err := revertFromReceipts(context.Background(), nil, file); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDeviceRevertValidatesWholeFileBeforeUndo(t *testing.T) {
	file := filepath.Join(t.TempDir(), "prepare.json")
	content := `[{"schemaVersion":"techstack.device-readiness/v1","resolutionId":"resolver-fallback","files":[{"path":"/must-not-be-touched"}]}]` + "\n{"
	if err := os.WriteFile(file, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	// A premature Undo would use the deliberately absent session. Invalid
	// trailing data must instead fail before any device operation is attempted.
	if err := revertFromReceipts(context.Background(), nil, file); err == nil {
		t.Fatal("accepted incomplete repair evidence")
	}
}
