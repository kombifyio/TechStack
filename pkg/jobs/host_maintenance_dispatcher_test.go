package jobs

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/servermaintenance"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

func TestHostUpdateDispatchRefusesReleasesWithoutUnitAwarePlan(t *testing.T) {
	for _, source := range []string{"configured-pin", "windows-linux-bundle"} {
		for _, version := range []string{"v0.48.8", "v0.48.9-beta.1", "v0.48.9", "v0.48.10"} {
			t.Run(source+"/"+version, func(t *testing.T) {
				root := t.TempDir()
				binary := []byte("immutable test artifact; never executed")
				binaryPath := filepath.Join(root, "stackkit")
				if err := os.WriteFile(binaryPath, binary, 0o700); err != nil {
					t.Fatal(err)
				}
				digest := sha256.Sum256(binary)
				platform := stackkitrelease.Platform{OS: runtime.GOOS, Arch: runtime.GOARCH}
				if source == "windows-linux-bundle" {
					platform = stackkitrelease.Platform{OS: "linux", Arch: "amd64"}
					binaryPath = "/app/.stackkit/bin/stackkit"
				}
				pin, err := json.Marshal(stackkitrelease.Pin{
					SchemaVersion: stackkitrelease.PinSchemaVersion, Kit: "basement-kit", Version: version,
					Platform: platform, ArchiveSHA256: strings.Repeat("a", 64), IndexSHA256: strings.Repeat("b", 64),
					BinarySHA256: hex.EncodeToString(digest[:]), BinaryPath: binaryPath,
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Setenv(stackKitReleasePinEnv, "")
				t.Setenv(stackKitReleaseCacheEnv, "")
				t.Setenv(stackKitReleaseBundleEnv, "")
				if source == "configured-pin" {
					pinPath := filepath.Join(root, "release-pin.json")
					if err := os.WriteFile(pinPath, pin, 0o600); err != nil {
						t.Fatal(err)
					}
					t.Setenv(stackKitReleasePinEnv, pinPath)
					t.Setenv(stackKitReleaseCacheEnv, root)
				} else {
					bundlePath := filepath.Join(root, "linux-runtime.tar.gz")
					file, err := os.Create(bundlePath)
					if err != nil {
						t.Fatal(err)
					}
					gz := gzip.NewWriter(file)
					archive := tar.NewWriter(gz)
					for _, entry := range []struct {
						name string
						data []byte
					}{
						{".stackkit/stackkits-release-pin.json", pin}, {".stackkit/bin/stackkit", binary},
					} {
						if err := archive.WriteHeader(&tar.Header{Name: entry.name, Mode: 0o700, Size: int64(len(entry.data))}); err != nil {
							t.Fatal(err)
						}
						if _, err := archive.Write(entry.data); err != nil {
							t.Fatal(err)
						}
					}
					for _, close := range []func() error{archive.Close, gz.Close, file.Close} {
						if err := close(); err != nil {
							t.Fatal(err)
						}
					}
					t.Setenv(stackKitReleaseBundleEnv, bundlePath)
				}
				for _, action := range []string{"plan", "apply"} {
					sender := &advancedResultSender{success: true, result: `{"schemaVersion":"stackkit.command-result/v1","status":"success","data":{"schema_version":"stackkit.host-maintenance/v1","plan_digest":"sha256:` + strings.Repeat("c", 64) + `","outcome":"applied"}}`}
					dispatcher := NewHostMaintenanceDispatcher(sender)
					target := servermaintenance.Target{TenantID: "tenant-1", AgentID: "agent-1", JobID: "job-1"}
					var dispatchErr error
					resolved, err := configuredTargetStackKitRelease()
					if err != nil {
						t.Fatal(err)
					}
					command := &agentpb.StackKitCommand{CommandId: "admission-check", Release: grpcserver.StackKitReleasePinFor(*resolved),
						Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_PLAN}
					if action == "apply" {
						command.Operation = agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_APPLY
						command.OwnerApproved, command.HostPlanDigest = true, "sha256:"+strings.Repeat("c", 64)
					}
					admissionErr := stackkitcommand.ValidateCommand(command)
					if action == "plan" {
						_, dispatchErr = dispatcher.Plan(t.Context(), target)
					} else {
						_, dispatchErr = dispatcher.Apply(t.Context(), target, "sha256:"+strings.Repeat("c", 64))
					}
					if version == "v0.48.8" || version == "v0.48.9-beta.1" {
						var refusal *servermaintenance.Refusal
						if admissionErr == nil || !errors.As(dispatchErr, &refusal) || refusal.Code != "stackkits_upgrade_required" || len(sender.commands) != 0 || !strings.Contains(refusal.Message, "v0.48.9") {
							t.Fatalf("%s with %s: err=%v commands=%v, want upgrade refusal before dispatch", action, version, dispatchErr, sender.commands)
						}
					} else if dispatchErr != nil || admissionErr != nil {
						t.Fatalf("%s with supported %s: dispatch=%v admission=%v", action, version, dispatchErr, admissionErr)
					}
				}
			})
		}
	}
}

// The fixtures are StackKits v0.46.6 stackkit.command-result/v1 documents.
// The success and plan_stale ones are unmodified outputs of `stackkit host
// updates plan|apply` and `stackkit host reboot` from an Ubuntu 24.04
// container run (2026-09-25). apply-failed (exit 1) and
// apply-still-running-exit4 are that apply document with the failure fields
// v0.46.6 writes on those paths (internal/hostmaintenance runUpdateUnit),
// since a real install failure and a 20-minute wait cannot be reproduced in
// such a run.
func TestParseHostMaintenanceResultReadsStackKitsDocuments(t *testing.T) {
	for _, tc := range []struct {
		file     string
		exitCode int32
		check    func(t *testing.T, data hostMaintenanceData, err error)
	}{
		{file: "plan-success.json", check: func(t *testing.T, data hostMaintenanceData, err error) {
			if err != nil || data.PendingCount != 12 || data.SecurityCount != 9 || len(data.Packages) != 12 || data.HoldScope == "" ||
				data.PlanDigest != "sha256:0b734babcadb52d832cdc4c0b176aa2f05ffab04aeb2737abf615be689ad3204" {
				t.Fatalf("plan = %+v, %v", data, err)
			}
		}},
		{file: "apply-success.json", check: func(t *testing.T, data hostMaintenanceData, err error) {
			if err != nil || data.Outcome != "applied" || data.Unit != "kombify-host-update-0b734babcadb-1790335030" {
				t.Fatalf("apply = %+v, %v", data, err)
			}
		}},
		{file: "reboot-success.json", check: func(t *testing.T, data hostMaintenanceData, err error) {
			if err != nil || data.BootIDBefore != "27e032ef-b334-4e84-8b0d-b570a560c90f" || data.ScheduledAt == "" {
				t.Fatalf("reboot = %+v, %v", data, err)
			}
		}},
		{file: "apply-denied-plan-stale.json", exitCode: 3, check: func(t *testing.T, _ hostMaintenanceData, err error) {
			var refusal *servermaintenance.Refusal
			if !errors.As(err, &refusal) || refusal.Code != servermaintenance.RefusalPlanStale {
				t.Fatalf("denied err = %v, want refusal plan_stale", err)
			}
		}},
		{file: "apply-failed.json", exitCode: 1, check: func(t *testing.T, data hostMaintenanceData, err error) {
			var failure *servermaintenance.Failure
			if !errors.As(err, &failure) || failure.Code != "install_failed" || data.Outcome != "failed" {
				t.Fatalf("failed err = %v outcome = %q, want failure install_failed", err, data.Outcome)
			}
		}},
		{file: "apply-still-running-exit4.json", exitCode: 4, check: func(t *testing.T, data hostMaintenanceData, err error) {
			var failure *servermaintenance.Failure
			if !errors.As(err, &failure) || failure.Code != "apply_wait_expired" || data.Outcome != "running" {
				t.Fatalf("exit 4 err = %v outcome = %q, want failure apply_wait_expired with outcome running", err, data.Outcome)
			}
		}},
	} {
		t.Run(tc.file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", "host-maintenance-v0.46.6", tc.file))
			if err != nil {
				t.Fatal(err)
			}
			data, exitCode, parseErr := parseHostMaintenanceResult(&agentpb.StackKitResult{
				CommandId: "job-1:apply:1", CommandResultJson: raw, ExitCode: tc.exitCode, Success: tc.exitCode == 0,
			})
			if exitCode != tc.exitCode {
				t.Fatalf("exit code = %d, want %d", exitCode, tc.exitCode)
			}
			tc.check(t, data, parseErr)
		})
	}
}
