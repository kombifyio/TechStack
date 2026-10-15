package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// The pinned CLI stand-in behaves like `stackkit fleet join <file>`: it reads
// the admission it was handed and writes the evidence-key request the member
// relays back to Home.
const handoffJoinScript = `#!/bin/sh
for word in "$@"; do last="$word"; done
case "$*" in
  *"fleet join"*) ;;
  *) echo "unexpected argv: $*" >&2; exit 2 ;;
esac
file=""
for word in "$@"; do
  if [ "$prev" = "join" ]; then file="$word"; fi
  prev="$word"
done
mkdir -p .stackkit/fleet
printf '{"request":%s}' "$(cat "$file")" > .stackkit/fleet/member-evidence-key-request.json
printf '%s\n' '{"schemaVersion":"stackkit.command-result/v1","command":"stackkit fleet join","status":"success","data":{}}'
`

func TestAgentRelaysAPublicDocumentThroughTheCLIAndRemovesItsStagingFile(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "stackkit")
	if err := os.WriteFile(binary, []byte(handoffJoinScript), 0o755); err != nil { // #nosec G306 -- executable test stand-in.
		t.Fatal(err)
	}
	binaryDigest := sha256.Sum256([]byte(handoffJoinScript))
	archive, index := strings.Repeat("a", 64), strings.Repeat("b", 64)
	pin, _ := json.Marshal(map[string]any{
		"schemaVersion": "techstack.stackkit-release-pin/v2", "kit": "basement-kit", "version": "v0.50.9",
		"platform":      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
		"archiveSha256": archive, "indexSha256": index, "binarySha256": hex.EncodeToString(binaryDigest[:]), "binaryPath": binary,
	})
	pinPath := filepath.Join(root, "stackkits-release-pin.json")
	if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
		t.Fatal(err)
	}
	workDir := filepath.Join(root, "workspace")
	if err := os.Mkdir(workDir, 0o750); err != nil {
		t.Fatal(err)
	}
	command := &agentpb.StackKitCommand{
		CommandId: "job-join-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_FEDERATION_HANDOFF,
		WorkingDirectory: workDir, OwnerApproved: true,
		HandoffStep: stackkitcommand.HandoffMemberJoin, HandoffRef: "ed25519://sha256/" + strings.Repeat("c", 64),
		HandoffDocument: []byte(`{"kind":"MemberAdmission","signature":"sig+/="}`),
		Release:         &agentpb.StackKitReleasePin{Version: "v0.50.9", PlatformOs: runtime.GOOS, PlatformArch: runtime.GOARCH, ArchiveSha256: archive, ReleaseIndexSha256: index},
	}
	if err := stackkitcommand.ValidateCommand(command); err != nil {
		t.Fatal(err)
	}
	executor := &StackKitExecutor{pinPath: pinPath, cacheRoot: filepath.Join(root, "cache")}
	result := executor.Execute(context.Background(), command)
	if !result.Success {
		t.Fatalf("hand-off failed: %s", result.Stderr)
	}
	if err := stackkitcommand.ValidateResult(result, command); err != nil {
		t.Fatalf("Core would refuse the result: %v", err)
	}
	if !bytes.Contains(result.HandoffDocument, []byte(`"signature":"sig+/="`)) {
		t.Fatalf("the document the CLI wrote did not come back intact: %s", result.HandoffDocument)
	}
	left, _ := filepath.Glob(filepath.Join(workDir, ".stackkit", ".techstack-handoff-*"))
	if len(left) != 0 {
		t.Fatalf("staging files left after the command: %v", left)
	}
}

// A hand-off reference reaches the pinned CLI's argv, so a value that could be
// read as a flag, or any reference on a step that takes none, is refused
// before the command leaves Core.
func TestHandoffRefusesReferencesThatCouldSteerTheCLI(t *testing.T) {
	base := func() *agentpb.StackKitCommand {
		return &agentpb.StackKitCommand{
			CommandId: "job-admit-1", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_FEDERATION_HANDOFF,
			WorkingDirectory: "/var/lib/techstack/stack", OwnerApproved: true,
			HandoffStep: stackkitcommand.HandoffMemberAdmit, HandoffRef: "cloud-edge",
			Release: &agentpb.StackKitReleasePin{Version: "v0.50.9", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: strings.Repeat("a", 64), ReleaseIndexSha256: strings.Repeat("b", 64)},
		}
	}
	if err := stackkitcommand.ValidateCommand(base()); err != nil {
		t.Fatalf("a well-formed admission step was refused: %v", err)
	}
	for name, mutate := range map[string]func(*agentpb.StackKitCommand){
		"flag-shaped node": func(c *agentpb.StackKitCommand) { c.HandoffRef = "--owner-approve" },
		"path-shaped node": func(c *agentpb.StackKitCommand) { c.HandoffRef = "../cloud-edge" },
		"reference on a bare step": func(c *agentpb.StackKitCommand) {
			c.HandoffStep = stackkitcommand.HandoffMemberImport
			c.HandoffDocument = []byte(`{}`)
		},
		"unknown step":           func(c *agentpb.StackKitCommand) { c.HandoffStep = "shell" },
		"without owner approval": func(c *agentpb.StackKitCommand) { c.OwnerApproved = false },
	} {
		command := base()
		mutate(command)
		if err := stackkitcommand.ValidateCommand(command); err == nil {
			t.Fatalf("%s was accepted", name)
		}
	}
}

// The external binding must match the node's own requirement exactly, so the
// requirement step returns that one entry of the ResolvedPlan and nothing else.
func TestLinkRequirementStepReturnsOnlyTheInterSiteRequirement(t *testing.T) {
	plan := []byte(`{"stackId":"s","federationLinkRequirements":{"inter-site-link":{"requirementsHash":"sha256:` + strings.Repeat("a", 64) + `"}},"nodes":[]}`)
	requirement, err := extractLinkRequirement(plan)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(requirement, []byte(strings.Repeat("a", 64))) || bytes.Contains(requirement, []byte("stackId")) {
		t.Fatalf("requirement = %s", requirement)
	}
	if _, err := extractLinkRequirement([]byte(`{"federationLinkRequirements":{}}`)); err == nil {
		t.Fatal("a plan without the requirement was accepted")
	}
}
