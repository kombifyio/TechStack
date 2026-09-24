package providercontrol

import (
	"testing"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func TestTerminalTombstoneContinuationAdmitsDecommissionOrExplicitProvision(t *testing.T) {
	provision := providerexecutor.Command{Operation: providerexecutor.OperationProvision}
	if !allowsTerminalOperationContinuation(provision, []bool{true}) {
		t.Fatal("explicit provision continuation was rejected")
	}
	if !allowsTerminalOperationContinuation(providerexecutor.Command{Operation: providerexecutor.OperationDecommission}, nil) {
		t.Fatal("exact decommission continuation was rejected")
	}
	for _, test := range []struct {
		name    string
		command providerexecutor.Command
		allow   []bool
	}{
		{name: "default", command: provision},
		{name: "false", command: provision, allow: []bool{false}},
		{name: "ambiguous", command: provision, allow: []bool{true, true}},
		{name: "reconcile", command: providerexecutor.Command{Operation: providerexecutor.OperationReconcile}, allow: []bool{true}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if allowsTerminalOperationContinuation(test.command, test.allow) {
				t.Fatal("terminal tombstone continuation was widened")
			}
		})
	}
}
