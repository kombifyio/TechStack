package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"runtime"

	"github.com/kombifyio/techstack/pkg/guardexec"
)

// isGuardExecutorMode reports whether the binary was invoked as
// `techstack guard-executor`: the privileged half of kombify Guard.
func isGuardExecutorMode(args []string) bool { return len(args) > 1 && args[1] == "guard-executor" }

// runGuardExecutor runs one of the privileged executor's subcommands:
//
//	serve    handle one connection from the socket unit (stdin is the socket)
//	trial    supervise the first run of an updated Guard
//	layout-trial  supervise the first run of the split layout
//	recover  clear the recovery flag after an operator restored a verified Guard
//	status   print the executor's status as JSON
//	admission-check  --manifest F --bundle F: print, as JSON, whether this host would
//	         admit the release a signed manifest describes (root, read-only)
func runGuardExecutor(ctx context.Context, args []string) error {
	if runtime.GOOS != "linux" {
		return errors.New("the privileged executor is only available on Linux")
	}
	if len(args) == 0 {
		return errors.New("usage: techstack guard-executor serve|trial|recover|status|admission-check")
	}
	flags := flag.NewFlagSet("techstack guard-executor "+args[0], flag.ContinueOnError)
	opts := guardexec.Options{Version: version}
	flags.StringVar(&opts.StateDir, "state-dir", envDefault("TECHSTACK_GUARD_EXECUTOR_STATE_DIR", ""), "root-only executor state directory")
	flags.StringVar(&opts.HandoffDir, "handoff-dir", envDefault("TECHSTACK_GUARD_HANDOFF_DIR", ""), "directory shared with the unprivileged daemon")
	flags.StringVar(&opts.AgentPath, "agent-path", envDefault("TECHSTACK_AGENT_EXECUTABLE_PATH", ""), "installed Guard executable")
	flags.StringVar(&opts.OperationsPath, "operations-path", envDefault("TECHSTACK_OPERATIONS_EXECUTABLE_PATH", ""), "installed StackKits operations executable")
	flags.BoolVar(&opts.GuardOnly, "guard-only", false, "trial: this host updates only the Guard executable (a Proxmox substrate Guard)")
	flags.StringVar(&opts.GuardUser, "guard-user", envDefault("TECHSTACK_GUARD_USER", ""), "the daemon's system user")
	trialID := flags.String("id", "", "update trial ID")
	manifestPath := flags.String("manifest", "", "admission-check: signed release manifest (release-manifest.json)")
	bundlePath := flags.String("bundle", "", "admission-check: its Sigstore bundle (release-manifest.sigstore.json)")
	socket := flags.String("socket", envDefault("TECHSTACK_GUARD_EXECUTOR_SOCKET", guardexec.DefaultSocket), "executor socket for status")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	opts.Log = slog.New(slog.NewJSONHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo})).With("service", "techstack-guard-executor", "version", version)
	switch args[0] {
	case "serve":
		conn, err := guardexec.ConnFromStdin()
		if err != nil {
			return err
		}
		return guardexec.ServeConnection(ctx, conn, opts)
	case "trial":
		if *trialID == "" {
			return errors.New("--id is required")
		}
		return guardexec.RunTrial(ctx, opts, *trialID)
	case "layout-trial":
		if *trialID == "" {
			return errors.New("--id is required")
		}
		return guardexec.RunLayoutTrial(ctx, opts, layoutTrialOptions(opts), *trialID, describeLayoutBudget(opts))
	case "recover":
		return guardexec.Recover(opts)
	case "admission-check":
		decision, err := guardexec.CheckManifestAdmission(opts, *manifestPath, *bundlePath)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(decision)
	case "status":
		var status guardexec.Status
		if err := (&guardexec.Client{Socket: *socket}).CallInto(ctx, guardexec.OpStatus, nil, &status); err != nil {
			return fmt.Errorf("the privileged executor is not reachable: %w", err)
		}
		return json.NewEncoder(os.Stdout).Encode(status)
	default:
		return fmt.Errorf("unknown guard-executor command %q", args[0])
	}
}
