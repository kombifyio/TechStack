package guardexec

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/agent/httpguard"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"github.com/kombifyio/techstack/pkg/guardrelease"
)

// Options configure the executor process.
type Options struct {
	StateDir       string
	HandoffDir     string
	AgentPath      string
	OperationsPath string
	GuardUser      string
	// Version is the version of the running executable.
	Version string
	Log     *slog.Logger
	// Getenv reads the executor's own environment (unit EnvironmentFile); a
	// request never influences it.
	Getenv func(string) string
}

func (o *Options) defaults() {
	if o.StateDir == "" {
		o.StateDir = DefaultStateDir
	}
	if o.HandoffDir == "" {
		o.HandoffDir = DefaultHandoffDir
	}
	if o.AgentPath == "" {
		o.AgentPath = DefaultAgentPath
	}
	if o.OperationsPath == "" {
		o.OperationsPath = DefaultOperationsPath
	}
	if o.GuardUser == "" {
		o.GuardUser = GuardUser
	}
	if o.Getenv == nil {
		o.Getenv = os.Getenv
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
}

// NewUpdaterFromOptions builds the updater with the release verifier the
// executor's own environment selects.
func NewUpdaterFromOptions(opts Options) (*Updater, error) {
	opts.defaults()
	verifierOptions := guardrelease.Options{}
	if path := strings.TrimSpace(opts.Getenv("TECHSTACK_GUARD_RELEASE_TRUSTED_ROOT")); path != "" {
		raw, err := os.ReadFile(path) // #nosec G304 -- an operator-configured path in the executor's own environment.
		if err != nil {
			return nil, fmt.Errorf("read the configured release trusted root: %w", err)
		}
		verifierOptions.TrustedRootJSON = raw
	}
	issuer, san := strings.TrimSpace(opts.Getenv("TECHSTACK_GUARD_RELEASE_SIGNER_ISSUER")), strings.TrimSpace(opts.Getenv("TECHSTACK_GUARD_RELEASE_SIGNER_SAN_REGEXP"))
	if issuer != "" || san != "" {
		verifierOptions.Identity = guardrelease.Identity{Issuer: issuer, SANRegexp: san}
	}
	verifier, err := guardrelease.NewVerifier(verifierOptions)
	if err != nil {
		return nil, err
	}
	budget := DefaultTrialBudget
	if parsed, err := parseDuration(opts.Getenv("TECHSTACK_GUARD_TRIAL_BUDGET")); err == nil && parsed > 0 {
		budget = parsed
	}
	return NewUpdater(UpdaterConfig{
		StateDir: opts.StateDir, HandoffDir: opts.HandoffDir, AgentPath: opts.AgentPath, OperationsPath: opts.OperationsPath,
		CurrentVersion: opts.Version, Verifier: verifier, Systemd: ExecSystemd{}, TrialBudget: budget, Log: opts.Log,
		AllowUnsigned: envTrue(opts.Getenv("TECHSTACK_GUARD_ALLOW_UNSIGNED_UPDATES")),
	})
}

// ServeConnection runs the executor for one connection, the one the service
// manager passes as stdin. It is the body of the socket-activated unit.
func ServeConnection(ctx context.Context, conn net.Conn, opts Options) error {
	opts.defaults()
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		return err
	}
	manifests := append([]string{}, httpguard.DefaultAccessManifestFiles()...)
	for _, path := range strings.Split(opts.Getenv("TECHSTACK_ACCESS_MANIFEST"), ",") {
		if path = strings.TrimSpace(path); path != "" {
			manifests = append(manifests, path)
		}
	}
	host, err := NewHost(HostConfig{
		StackKit: agentpkg.NewStackKitExecutorFromConfig(opts.Getenv("TECHSTACK_STACKKIT_RELEASE_PIN"), opts.Getenv("TECHSTACK_STACKKIT_RELEASE_CACHE")), Updater: updater,
		StateDir: opts.StateDir, HandoffDir: opts.HandoffDir, ManifestPaths: manifests,
	})
	if err != nil {
		return err
	}
	server := &Server{
		Backend: host, Replay: &FileReplay{Dir: filepath.Join(opts.StateDir, "admitted")},
		AllowPeer: peerPolicy(opts.GuardUser), Log: opts.Log,
		Pins: func() ([]guardbinding.PinnedKey, error) {
			return guardbinding.ReadPins(filepath.Join(opts.StateDir, guardbinding.PinFile))
		},
	}
	return server.Serve(ctx, conn)
}

// peerPolicy admits root and the Guard's own user, nobody else.
func peerPolicy(guardUser string) func(uint32) bool {
	guardUID, found := uint32(0), false
	if account, err := user.Lookup(guardUser); err == nil {
		if parsed, err := strconv.ParseUint(account.Uid, 10, 32); err == nil {
			guardUID, found = uint32(parsed), true
		}
	}
	return func(uid uint32) bool { return uid == 0 || (found && uid == guardUID) }
}

// ConnFromStdin returns the connection a socket-activated unit received.
func ConnFromStdin() (net.Conn, error) {
	conn, err := net.FileConn(os.Stdin)
	if err != nil {
		return nil, fmt.Errorf("the executor must be started by its socket unit: %w", err)
	}
	return conn, nil
}

// RunTrial is the update trial supervisor.
func RunTrial(ctx context.Context, opts Options, trialID string) error {
	opts.defaults()
	if os.Geteuid() != 0 {
		return errors.New("the update trial supervisor must run as root")
	}
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		return err
	}
	return updater.RunTrial(ctx, trialID)
}

// Recover clears the recovery flag after an operator restored a verified Guard.
func Recover(opts Options) error {
	opts.defaults()
	if os.Geteuid() != 0 {
		return errors.New("recovery must run as root")
	}
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		return err
	}
	return updater.Recover()
}

// CheckManifestAdmission is the root-only, read-only admission check of the
// operator command: it reports whether this host would admit the release a
// signed manifest and its bundle describe, using the executor's own verifier,
// its manifest floor and the running Guard's version. It stages and records
// nothing, and does not touch the floor.
func CheckManifestAdmission(opts Options, manifestPath, bundlePath string) (ManifestAdmission, error) {
	opts.defaults()
	if os.Geteuid() != 0 {
		return ManifestAdmission{}, errors.New("the admission check must run as root")
	}
	if manifestPath == "" || bundlePath == "" {
		return ManifestAdmission{}, errors.New("--manifest and --bundle are required")
	}
	// A missing state directory is not a Guard host; creating it would be a write.
	if info, err := os.Stat(opts.StateDir); err != nil || !info.IsDir() {
		return ManifestAdmission{}, fmt.Errorf("no executor state at %s: this is not an installed Guard", opts.StateDir)
	}
	updater, err := NewUpdaterFromOptions(opts)
	if err != nil {
		return ManifestAdmission{}, err
	}
	return updater.CheckManifest(manifestPath, bundlePath)
}

func envTrue(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

func parseDuration(value string) (time.Duration, error) {
	return time.ParseDuration(strings.TrimSpace(value))
}
