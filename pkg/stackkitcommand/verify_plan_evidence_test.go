package stackkitcommand

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"strings"
	"testing"
)

func TestVerifyPlanEvidenceBindsAppliedPlan(t *testing.T) {
	hash := "sha256:" + strings.Repeat("a", 64)
	other := "sha256:" + strings.Repeat("b", 64)
	pin := &agentpb.StackKitReleasePin{Version: "v0.55.5", PlatformOs: "linux", PlatformArch: "amd64", ArchiveSha256: strings.Repeat("a", 64), ReleaseIndexSha256: strings.Repeat("b", 64)}
	cmd := &agentpb.StackKitCommand{Release: pin, CommandId: "exec-001", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_VERIFY, ExpectedPlanHash: hash, Offline: true}
	makeResult := func(plan, applied, resultHash string, offline string) *agentpb.StackKitResult {
		raw := []byte(fmt.Sprintf(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit verify","status":"success","data":{"schemaVersion":"stackkit.verify-result/v1","offline":%s,"planHash":%q,"apply":{"planHash":%q,"resultHash":%q,"evidenceBundleHash":%q}}}`, offline, plan, applied, resultHash, hash))
		sum := sha256.Sum256(raw)
		return &agentpb.StackKitResult{CommandId: cmd.CommandId, Release: pin, Success: true, CommandResultJson: raw, PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: "sha256:" + hex.EncodeToString(sum[:])}
	}
	good := makeResult(hash, hash, hash, "true")
	if _, err := ParseVerifyPlanEvidence(cmd, good); err != nil {
		t.Fatal(err)
	}
	cases := []*agentpb.StackKitResult{makeResult(other, hash, hash, "true"), makeResult(hash, other, hash, "true"), makeResult(hash, hash, "", "true"), makeResult(hash, hash, hash, "false")}
	missing := makeResult(hash, hash, hash, "true")
	missing.PinnedCliEvidenceVerified = false
	cases = append(cases, missing)
	tampered := makeResult(hash, hash, hash, "true")
	tampered.CommandResultJson = append(tampered.CommandResultJson, ' ')
	cases = append(cases, tampered)
	wrongRelease := makeResult(hash, hash, hash, "true")
	wrongRelease.Release = &agentpb.StackKitReleasePin{Version: "v0.55.4", PlatformOs: pin.PlatformOs, PlatformArch: pin.PlatformArch, ArchiveSha256: pin.ArchiveSha256, ReleaseIndexSha256: pin.ReleaseIndexSha256}
	cases = append(cases, wrongRelease)
	badExit := makeResult(hash, hash, hash, "true")
	badExit.ExitCode = 1
	cases = append(cases, badExit)
	for _, result := range cases {
		if _, err := ParseVerifyPlanEvidence(cmd, result); err == nil {
			t.Fatal("accepted mismatched or unattested verify result")
		}
	}
}
