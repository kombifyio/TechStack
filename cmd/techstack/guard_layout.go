package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/guardexec"
	"github.com/kombifyio/techstack/pkg/guardlayout"
)

// isGuardLayoutMode reports whether the binary was invoked as
// `techstack guard layout ...`.
func isGuardLayoutMode(args []string) bool {
	return len(args) > 2 && args[1] == "guard" && args[2] == "layout"
}

// runGuardLayout implements `techstack guard layout apply`: it creates the
// privilege-split layout (dedicated user, protected state, privileged executor
// socket) and switches the Guard service to it. install.sh runs it as root after
// enrollment; it is idempotent and is also the repair command.
func runGuardLayout(ctx context.Context, args []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("the privilege-split layout is only available on Linux")
	}
	if len(args) == 0 || args[0] != "apply" {
		return errors.New("usage: techstack guard layout apply [--activate]")
	}
	if os.Geteuid() != 0 {
		return errors.New("applying the Guard layout requires root")
	}
	flags := flag.NewFlagSet("techstack guard layout apply", flag.ContinueOnError)
	binary := flags.String("binary", guardlayout.DefaultBinaryPath, "installed Guard executable")
	operations := flags.String("operations", guardlayout.DefaultOperationsPath, "installed StackKits operations executable")
	guardUser := flags.String("user", guardlayout.DefaultUser, "system user of the unprivileged daemon")
	activate := flags.Bool("activate", false, "restart the Guard service onto the new layout")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	result, err := guardlayout.Apply(ctx, guardLayoutOptions(*binary, *operations, *guardUser, log, *activate))
	if err != nil {
		return err
	}
	_, _ = fmt.Fprintf(os.Stdout, "GUARD_LAYOUT switched=%t migrated=%t\n", result.Switched, result.Migrated)
	return nil
}

func guardLayoutOptions(binary, operations, guardUser string, log *slog.Logger, activate bool) guardlayout.Options {
	return guardlayout.Options{
		BinaryPath: binary, OperationsPath: operations, GuardUser: guardUser,
		NotifyReady: true, Activate: activate, Log: log,
		VerifyExecutor: func(ctx context.Context, socket string) error {
			probe, cancel := context.WithTimeout(ctx, 20*time.Second)
			defer cancel()
			var status guardexec.Status
			if err := (&guardexec.Client{Socket: socket}).CallInto(probe, guardexec.OpStatus, nil, &status); err != nil {
				return err
			}
			if !status.Ready || !status.Privileged {
				return errors.New("the executor answered but is not privileged and ready")
			}
			return nil
		},
	}
}

// migrateGuardLayout moves a Guard that still runs as root under a legacy unit to
// the split layout. The legacy unit stays in charge until the privileged
// executor answers, and is restored by a supervising trial unless the new
// daemon completes an authenticated round trip. It returns true when the
// service is being switched and this process should exit.
func migrateGuardLayout(ctx context.Context, log *slog.Logger) bool {
	if !guardLayoutMigrationEligible("") {
		return false
	}
	executable, err := os.Executable()
	if err != nil {
		return false
	}
	operations := envDefault("TECHSTACK_OPERATIONS_EXECUTABLE_PATH", guardlayout.DefaultOperationsPath)
	options := guardLayoutOptions(executable, operations, guardlayout.DefaultUser, log, false)
	options.Trial = func(ctx context.Context, id string) error {
		now := time.Now().UTC()
		marker := guardexec.LayoutTrialMarker{ID: id, Version: version, StartedAt: now, Deadline: now.Add(guardexec.DefaultTrialBudget)}
		if err := guardexec.WriteLayoutTrialMarker(handoffDir(), marker); err != nil {
			return err
		}
		return guardexec.ExecSystemd{}.RunTransient(ctx, "techstack-guard-trial-"+id, []string{executable, "guard-executor", "layout-trial", "--id", id})
	}
	return migrateGuardLayoutWithOptions(ctx, log, options)
}

func guardLayoutMigrationEligible(root string) bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || !envBoolDefault("TECHSTACK_GUARD_AUTO_MIGRATE", true) {
		return false
	}
	// Only the service itself migrates: a foreground run by an operator must not
	// rewrite the host's units behind their back.
	if os.Getenv("INVOCATION_ID") == "" {
		return false
	}
	unit, err := os.ReadFile(filepath.Join(root, "/etc/systemd/system/"+guardlayout.AgentUnit))
	if err != nil || !guardlayout.IsLegacyUnit(string(unit)) {
		return false
	}
	return true
}

func migrateGuardLayoutWithOptions(ctx context.Context, log *slog.Logger, options guardlayout.Options) bool {
	if !guardLayoutMigrationEligible(options.Root) {
		return false
	}
	result, err := guardlayout.Apply(ctx, options)
	if err != nil {
		log.Warn("guard_layout_migration_deferred", "error", err.Error())
		return false
	}
	log.Info("guard_layout_migration_started", "trial_id", result.TrialID)
	return result.TrialID != ""
}

func layoutTrialOptions(opts guardexec.Options) guardlayout.Options {
	binary := firstCLIValue(opts.AgentPath, guardlayout.DefaultBinaryPath)
	operations := firstCLIValue(opts.OperationsPath, guardlayout.DefaultOperationsPath)
	return guardlayout.Options{BinaryPath: binary, OperationsPath: operations, GuardUser: firstCLIValue(opts.GuardUser, guardlayout.DefaultUser), NotifyReady: true, Log: opts.Log}
}

func describeLayoutBudget(opts guardexec.Options) time.Duration {
	if parsed, err := time.ParseDuration(strings.TrimSpace(os.Getenv("TECHSTACK_GUARD_TRIAL_BUDGET"))); err == nil && parsed > 0 {
		return parsed
	}
	return guardexec.DefaultTrialBudget
}

// signedGuardReleaseAvailable reports whether the control plane publishes a signed
// Guard release. The privilege-split layout authenticates every update and every
// StackKits runtime install against one, so a root Guard stays as it is until
// its control plane has one; it does not trade a working Guard for one that can
// no longer update.
func signedGuardReleaseAvailable(ctx context.Context, client *http.Client, enrollment agentEnrollment, log *slog.Logger) bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || os.Getenv("INVOCATION_ID") == "" {
		return false
	}
	probe, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	available, err := guardexec.ReleaseEvidenceAvailable(probe, client, agentpkg.TechstackRuntimeConvergenceConfig{
		URL: derivedAgentBinaryURL(enrollment.HeartbeatURL), AgentToken: enrollment.AgentToken,
		RuntimeAgentID: enrollment.RuntimeAgentID, TenantID: enrollment.TenantID,
	})
	if err != nil {
		log.Warn("guard_layout_migration_deferred", "reason", "release_evidence_unreachable", "error", err.Error())
		return false
	}
	if !available {
		log.Info("guard_layout_migration_deferred", "reason", "control_plane_publishes_no_signed_guard_release")
	}
	return available
}
