package agent

import (
	"reflect"
	"strings"
	"testing"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
)

// Host maintenance argv is fixed: the plan digest of an apply is the only
// caller input, and anything that is not a sha256 digest is refused before
// the CLI runs.
func TestStackKitHostArgsAreFixedAndOnlyTakeAPlanDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, tc := range []struct {
		command *agentpb.StackKitCommand
		want    []string
	}{
		{&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_PLAN},
			[]string{"host", "updates", "plan", "--json"}},
		{&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_APPLY, OwnerApproved: true, HostPlanDigest: digest},
			[]string{"host", "updates", "apply", "--plan-digest", digest, "--yes", "--json"}},
		{&agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_REBOOT, OwnerApproved: true},
			[]string{"host", "reboot", "--yes", "--delay", "30s", "--json"}},
	} {
		got, err := stackKitHostArgs(tc.command)
		if err != nil || !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s argv = %q, %v; want %q", tc.command.Operation, got, err, tc.want)
		}
	}
	for _, malformed := range []string{"", "sha256:" + strings.Repeat("A", 64), "sha256:" + strings.Repeat("a", 63), digest + " --force", "md5:" + strings.Repeat("a", 32)} {
		if got, err := stackKitHostArgs(&agentpb.StackKitCommand{
			Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_APPLY, OwnerApproved: true, HostPlanDigest: malformed,
		}); err == nil {
			t.Fatalf("apply with digest %q rendered %q, want a refusal", malformed, got)
		}
	}
}
