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

// A host security repair names only controls of the closed evidence vocabulary:
// a free-form argument, an unapproved request or a repeated control never reaches
// the CLI, and the argv around the controls is constant.
func TestStackKitHostSecurityRepairTakesOnlyClosedControlIDs(t *testing.T) {
	digest := strings.Repeat("a", 64)
	repair := func(approved bool, controls ...string) *agentpb.StackKitCommand {
		return &agentpb.StackKitCommand{
			CommandId: "guard-sec-repair:1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_SECURITY_REPAIR,
			OwnerApproved: approved, HostSecurityControls: controls,
			Release: &agentpb.StackKitReleasePin{Version: "v0.50.0", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: digest, ReleaseIndexSha256: digest},
		}
	}
	got, err := stackKitHostArgs(repair(true, "bruteforce.fail2ban", "firewall.default_inbound"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0] != "host" || got[1] != "security" || got[2] != "repair" || got[3] != "--apply" {
		t.Fatalf("argv = %q", got)
	}
	controlArgs := []string{}
	for i, arg := range got {
		if arg == "--control" {
			controlArgs = append(controlArgs, got[i+1])
		}
	}
	if !reflect.DeepEqual(controlArgs, []string{"bruteforce.fail2ban", "firewall.default_inbound"}) {
		t.Fatalf("controls in argv = %q", controlArgs)
	}
	for name, command := range map[string]*agentpb.StackKitCommand{
		"a flag smuggled as a control": repair(true, "firewall.default_inbound --management-source 0.0.0.0/1"),
		"an unknown control":           repair(true, "ssh.everything"),
		"a repeated control":           repair(true, "kernel.sysctl", "kernel.sysctl"),
		"no control":                   repair(true),
		"no owner approval":            repair(false, "kernel.sysctl"),
	} {
		if args, err := stackKitHostArgs(command); err == nil {
			t.Errorf("%s rendered %q, want a refusal", name, args)
		}
	}
}
