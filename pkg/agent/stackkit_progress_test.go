package agent

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// The pinned CLI stand-in plays the apply progress stream of one scenario on the
// event spool it is handed, then answers like `stackkit apply --json`.
const fakeStackKitApplyScript = `#!/bin/sh
spool=""
previous=""
for word in "$@"; do
  if [ "$previous" = "--progress-jsonl" ]; then spool="$word"; fi
  previous="$word"
done
event() {
  printf '{"time":"2026-10-09T08:00:00Z","phase":"apply.owner","status":"%s","attributes":{"scope":"owner","owner":"%s","position":"%s","total":"3"}}\n' "$1" "$2" "$3" >> "$spool"
}
result='{"schemaVersion":"stackkit.command-result/v1","command":"stackkit apply","status":"success","data":{}}'
case "$FAKE_STACKKIT_SCENARIO" in
in-flight-silence)
  # owner 1 is in flight (its label carries a secret), owner 2 starts and
  # finishes, a late heartbeat of owner 2 and an out-of-range one of owner 1
  # follow, then nothing.
  event started "kit owner-1 password=hunter2hunter2" 1
  event started kit/tofu/owner-2 2
  event succeeded kit/tofu/owner-2 2
  event running kit/tofu/owner-2 99
  event running "kit owner-1 password=hunter2hunter2" 9
  sleep 30
  ;;
finished-silence)
  # the only owner finishes after the spool was rewritten (shorter than what
  # was already read), a late heartbeat follows, then reconciliation stays
  # silent for longer than the limit.
  event started kit/tofu/owner-1 1
  event running kit/tofu/owner-1 1
  event running kit/tofu/owner-1 1
  sleep 0.4
  : > "$spool"
  event succeeded kit/tofu/owner-1 1
  event running kit/tofu/owner-1 1
  sleep 1.2
  printf '%s\n' "$result"
  ;;
no-owner-silence)
  printf '{"time":"2026-10-09T08:00:00Z","phase":"docker.install","status":"running"}\n' >> "$spool"
  sleep 30
  ;;
esac
`

// The node ends a StackKits apply for silence only while a runtime owner is in
// flight, and its reason must reach the controller's parser intact. Everywhere
// else the old rule stands: the command's wall clock alone.
func TestStackKitApplyIsEndedForSilenceOnlyWhileAnOwnerIsInFlight(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the stand-in CLI is a POSIX shell script and host path admission needs a Linux ancestor chain")
	}
	run := func(t *testing.T, scenario string, timeoutSeconds int32) *agentpb.StackKitResult {
		t.Helper()
		t.Setenv("FAKE_STACKKIT_SCENARIO", scenario)
		root := t.TempDir()
		binary := filepath.Join(root, "stackkit")
		if err := os.WriteFile(binary, []byte(fakeStackKitApplyScript), 0o755); err != nil { // #nosec G306 -- executable test stand-in.
			t.Fatal(err)
		}
		digest := sha256.Sum256([]byte(fakeStackKitApplyScript))
		archive, index := strings.Repeat("a", 64), strings.Repeat("b", 64)
		pin, _ := json.Marshal(map[string]any{
			"schemaVersion": "techstack.stackkit-release-pin/v2", "kit": "basement-kit", "version": "v0.52.6",
			"platform":      map[string]string{"os": runtime.GOOS, "arch": runtime.GOARCH},
			"archiveSha256": archive, "indexSha256": index, "binarySha256": hex.EncodeToString(digest[:]), "binaryPath": binary,
		})
		pinPath := filepath.Join(root, "stackkits-release-pin.json")
		if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
			t.Fatal(err)
		}
		workDir := filepath.Join(root, "workspace")
		if err := os.Mkdir(workDir, 0o750); err != nil {
			t.Fatal(err)
		}
		executor := &StackKitExecutor{pinPath: pinPath, cacheRoot: filepath.Join(root, "cache"), silenceLimit: 600 * time.Millisecond}
		return executor.Execute(context.Background(), &agentpb.StackKitCommand{
			CommandId: "job-1-apply", Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY, WorkingDirectory: workDir,
			ExpectedPlanHash: "sha256:" + strings.Repeat("c", 64), LocalSiteRef: "home", LocalNodeRef: "main", LocalExecutionChannelRef: "local-home-main",
			TimeoutSeconds: timeoutSeconds,
			Release:        &agentpb.StackKitReleasePin{Version: "v0.52.6", PlatformOs: runtime.GOOS, PlatformArch: runtime.GOARCH, ArchiveSha256: archive, ReleaseIndexSha256: index},
		})
	}

	t.Run("a silent owner in flight ends the apply with a reason the controller reads", func(t *testing.T) {
		result := run(t, "in-flight-silence", 60)
		stall, ok := stackkitcommand.ParseApplyStall(result.Stderr)
		if result.Success || !ok {
			t.Fatalf("success = %v, stall readable = %v, stderr = %q; want a failure carrying the stalled reason", result.Success, ok, result.Stderr)
		}
		// Owner 1 stayed in flight while owner 2 finished; the out-of-range
		// heartbeat was progress but kept the place owner 1 reported before.
		if !strings.HasPrefix(stall.Owner, "kit_owner-1") || stall.Position != 1 || stall.Total != 3 {
			t.Fatalf("stall = %+v, want owner 1 at position 1 of 3", stall)
		}
		if strings.Contains(result.Stderr, "hunter2") {
			t.Fatalf("a secret in an owner label reached the failure text: %q", result.Stderr)
		}
	})

	t.Run("owners finished, spool rewritten, then a long silence leaves the apply to finish", func(t *testing.T) {
		if result := run(t, "finished-silence", 60); !result.Success {
			t.Fatalf("a healthy apply was ended: %q", result.Stderr)
		}
	})

	t.Run("without owner events only the wall clock ends the apply", func(t *testing.T) {
		result := run(t, "no-owner-silence", 1)
		if _, stalled := stackkitcommand.ParseApplyStall(result.Stderr); result.Success || stalled || !strings.Contains(result.Stderr, "timed out") {
			t.Fatalf("success = %v, stalled = %v, stderr = %q; want the wall-clock timeout", result.Success, stalled, result.Stderr)
		}
	})
}

// A rollout event's attributes can carry free text. Only the closed, bounded
// and redacted facts of the progress contract may reach the live log.
func TestStackKitProgressForwardsOnlyBoundedOwnerFacts(t *testing.T) {
	var fields map[string]string
	emitStackKitProgressLine(nil, []byte(`{"time":"2026-10-09T08:00:00Z","phase":"apply.owner","status":"failed","failureClass":"tofu_apply_failed","attributes":{`+
		`"position":"3","total":"10","owner":"module/opentofu/main","workload":"basement-core password=hunter2hunter2","elapsed_ms":"45012","failure_code":"unit_failed",`+
		`"replayed":"maybe","note":"token=abc123def","module":"`+strings.Repeat("m", 5000)+`"}}`),
		func(entry *agentpb.LogEntry) { fields = entry.Fields })

	if fields["position"] != "3" || fields["total"] != "10" || fields["owner"] != "module/opentofu/main" ||
		fields["elapsed_ms"] != "45012" || fields["failure_code"] != "unit_failed" || fields["failure_class"] != "tofu_apply_failed" {
		t.Fatalf("progress facts were not forwarded: %v", fields)
	}
	if !strings.HasPrefix(fields["workload"], "basement-core") || strings.Contains(fields["workload"], "hunter2") {
		t.Fatalf("a label was forwarded with its secret: %q", fields["workload"])
	}
	if _, leaked := fields["note"]; leaked {
		t.Fatalf("a free-form attribute reached the live log: %v", fields)
	}
	if _, invalid := fields["replayed"]; invalid {
		t.Fatalf("a value outside the contract was forwarded: %v", fields)
	}
	if len(fields["module"]) > 256 {
		t.Fatalf("an overlong identity was forwarded unbounded: %d bytes", len(fields["module"]))
	}
}
