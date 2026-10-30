package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/hostidentity"
	"github.com/kombifyio/techstack/pkg/guardcompat"
	"github.com/kombifyio/techstack/pkg/privatechannel"
	"github.com/kombifyio/techstack/pkg/sdnotify"
)

const (
	guardStateDirEnv        = "TECHSTACK_GUARD_STATE_DIR"
	enrollCapabilityVersion = guardcompat.EnrollCapabilityVersion
	defaultEnrollmentFile   = "/etc/techstack/agent-enrollment.json"
	identityRetryCap        = 30 * time.Second
	identityHoldInterval    = 10 * time.Minute
)

// defaultGuardStateDir is where the identity key and enrollment state live.
func defaultGuardStateDir() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(envDefault("ProgramData", `C:\ProgramData`), "kombify", "guard")
	}
	return "/var/lib/techstack/guard"
}

func resolveGuardStateDir(configured string) string {
	return firstCLIValue(configured, os.Getenv(guardStateDirEnv), defaultGuardStateDir())
}

// isAgentEnroll reports whether the arguments after `agent` select enrollment.
func isAgentEnroll(args []string) bool { return len(args) > 0 && args[0] == "enroll" }

// runAgentEnroll implements `techstack agent enroll`: it redeems a one-time
// pairing capability with a host-generated key, persists the enrollment and
// confirms the binding. install.sh runs it as root after the Guard binary is in
// place; it is also safe to repeat, resuming an interrupted enrollment.
func runAgentEnroll(ctx context.Context, args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("techstack agent enroll", flag.ContinueOnError)
	check := flags.Bool("check", false, "print the enroll capability marker and exit")
	resumable := flags.Bool("resumable", false, "print whether an interrupted enrollment holds this capability, without redeeming it")
	server := flags.String("server", "", "control-plane base URL")
	tokenFile := flags.String("token-file", "", "file holding the one-time pairing capability (deleted after use)")
	enrollmentFile := flags.String("enrollment-file", defaultEnrollmentFile, "where to persist the enrollment response")
	stateDir := flags.String("state-dir", "", "Guard state directory (env "+guardStateDirEnv+")")
	hostname := flags.String("hostname", "", "hostname to register (default: this host)")
	substrate := flags.Bool("substrate", false, "enroll this Proxmox host as a substrate Guard")
	lanPin := flags.String("lan-pin", "", "key pin (sha256//...) of a private LAN bridge --server, from the install command")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *check {
		_, _ = fmt.Fprintln(stdout, enrollCapabilityVersion)
		return nil
	}
	if strings.TrimSpace(*server) == "" || strings.TrimSpace(*tokenFile) == "" {
		return errors.New("--server and --token-file are required")
	}
	token, err := readSecretFile(*tokenFile)
	if err != nil {
		return fmt.Errorf("read pairing capability: %w", err)
	}
	manager, err := hostidentity.Open(resolveGuardStateDir(*stateDir))
	if err != nil {
		return err
	}
	if *resumable {
		// The installer asks before it downloads anything: a capability the
		// control plane already claimed cannot authorize a download, but it can
		// still resume the redemption it was claimed for.
		_, _ = fmt.Fprintf(stdout, "GUARD_ENROLL_RESUMABLE=%t\n", manager.CanResume(*server, string(token)))
		return nil
	}
	name := strings.TrimSpace(*hostname)
	if name == "" {
		if name, _ = os.Hostname(); strings.TrimSpace(name) == "" {
			name = "worker"
		}
	}
	bootstrap := hostidentity.Bootstrap{
		Server: strings.TrimRight(strings.TrimSpace(*server), "/"), Token: strings.TrimSpace(string(token)),
		Hostname: name, OS: runtime.GOOS, Arch: unameArch(runtime.GOARCH),
	}
	if pin := strings.TrimSpace(*lanPin); pin != "" {
		if err := privatechannel.InstallProcessPin(bootstrap.Server, pin); err != nil {
			return fmt.Errorf("pin the private LAN bridge: %w", err)
		}
		bootstrap.LANPin = pin
	}
	if *substrate {
		bootstrap.Extra = map[string]any{"type": "substrate", "provider": "proxmox"}
	}
	if err := declarePriorCustody(manager, &bootstrap); err != nil {
		return err
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	result, err := hostidentity.Enroll(ctx, hostidentity.EnrollOptions{
		Manager: manager, Bootstrap: bootstrap, EnrollmentPath: *enrollmentFile, Logger: log,
	})
	if err != nil {
		if errors.Is(err, hostidentity.ErrRecoveryNeeded) || errors.Is(err, hostidentity.ErrRevoked) {
			// Definitively rejected, expired or refused: the capability can
			// never be redeemed again, so its file goes as well. Transient
			// failures keep it for the same attempt.
			_ = os.Remove(*tokenFile)
		}
		return err
	}
	// The capability is consumed: the file it came from must not outlive it.
	_ = os.Remove(*tokenFile)
	clearHandoffReceipts(manager)
	binding := "legacy_bearer"
	if result.Mode == hostidentity.ModeHostKey {
		binding = "host_key"
	}
	_, _ = fmt.Fprintf(stdout, "GUARD_ENROLLED accepted=%t identity_binding=%s enrollment_file=%s\n", result.Accepted, binding, result.EnrollmentPath)
	return nil
}

// unameArch keeps the architecture spelling the installer always reported.
func unameArch(goarch string) string {
	switch goarch {
	case "amd64":
		return "x86_64"
	case "arm64":
		return "aarch64"
	}
	return goarch
}

// resumeBootstrapEnrollment completes an enrollment that was started but not
// finished (a first boot that restarted mid-way, or a managed image that
// carries a capability for the instance). It is a no-op without a persisted
// capability.
func resumeBootstrapEnrollment(ctx context.Context, manager *hostidentity.Manager, enrollmentFile string, log *slog.Logger) {
	bootstrap, err := manager.LoadBootstrap()
	if err != nil {
		log.Warn("guard_bootstrap_capability_unreadable", "error", err.Error())
		return
	}
	if bootstrap == nil {
		return
	}
	record := manager.Record()
	located := hostidentity.LocateEnrollment(enrollmentFile, manager.Dir())
	if _, statErr := os.Stat(located); statErr == nil && record.State == hostidentity.StateBound {
		// Bound already: only the leftover capability needs to go.
		_ = manager.RemoveBootstrap()
		return
	}
	if bootstrap.LANPin != "" {
		if err := privatechannel.InstallProcessPin(bootstrap.Server, bootstrap.LANPin); err != nil {
			log.Error("guard_enrollment_resume_failed", "error", err.Error())
			return
		}
	}
	log.Info("guard_enrollment_resuming", "state", string(record.State))
	if err := declarePriorCustody(manager, bootstrap); err != nil {
		log.Error("guard_enrollment_resume_failed", "error", err.Error())
		return
	}
	if _, err := hostidentity.Enroll(ctx, hostidentity.EnrollOptions{
		Manager: manager, Bootstrap: *bootstrap, EnrollmentPath: enrollmentFile, Logger: log,
	}); err != nil {
		log.Error("guard_enrollment_resume_failed", "error", err.Error())
		return
	}
	clearHandoffReceipts(manager)
}

// guardIdentityRuntime is the identity the running Guard authenticates with.
type guardIdentityRuntime struct {
	client *hostidentity.Client
	// held is set when the identity is revoked or needs recovery: the Guard
	// executes nothing until it is paired again.
	held error
}

// establishGuardIdentity brings the host identity up before any work starts. A
// control plane without host identity, or a server whose state directory cannot
// be used, keeps the legacy bearer mode; revoked and recovery-needed identities
// hold the Guard idle instead of executing.
func establishGuardIdentity(
	ctx context.Context,
	manager *hostidentity.Manager,
	enrollment agentEnrollment,
	log *slog.Logger,
	progress func(),
) guardIdentityRuntime {
	if manager == nil {
		return guardIdentityRuntime{}
	}
	endpoints := hostidentity.Endpoints{
		TokenURL:  firstCLIValue(enrollment.IdentityTokenURL, enrollment.ChannelBootstrap.IdentityTokenURL),
		BindURL:   firstCLIValue(enrollment.IdentityBindURL, enrollment.ChannelBootstrap.IdentityBindURL),
		RotateURL: firstCLIValue(enrollment.IdentityRotateURL, enrollment.ChannelBootstrap.IdentityRotateURL),
	}
	advertised := endpoints.TokenURL != ""
	if !advertised {
		endpoints = hostidentity.DeriveEndpoints(enrollment.HeartbeatURL)
	}
	if endpoints.TokenURL == "" {
		return guardIdentityRuntime{}
	}
	client, err := hostidentity.NewClient(hostidentity.ClientConfig{
		Manager: manager, Endpoints: endpoints, AgentToken: enrollment.AgentToken,
		TenantID: firstCLIValue(enrollment.TenantID, enrollment.ChannelBootstrap.TenantID),
		Logger:   log, Probe: !advertised,
	})
	if err != nil {
		log.Warn("guard_identity_unavailable", "error", err.Error())
		return guardIdentityRuntime{}
	}
	delay := time.Second
	for {
		if progress != nil {
			progress()
		}
		mode, err := client.Establish(ctx)
		switch {
		case err == nil:
			log.Info("guard_identity_established", "mode", modeName(mode), "state", string(manager.Record().State))
			return guardIdentityRuntime{client: client}
		case errors.Is(err, hostidentity.ErrRevoked), errors.Is(err, hostidentity.ErrRecoveryNeeded):
			log.Error("guard_identity_not_usable", "state", string(manager.Record().State), "reason", manager.Record().Reason,
				"guidance", "this server no longer has a valid kombify Guard identity; pair it again from the kombify dashboard")
			return guardIdentityRuntime{client: client, held: err}
		case ctx.Err() != nil:
			return guardIdentityRuntime{client: client, held: ctx.Err()}
		}
		log.Warn("guard_identity_establish_failed", "error", err.Error(), "retry_in", delay.String())
		if !waitWithProgress(ctx, delay, progress) {
			return guardIdentityRuntime{client: client, held: ctx.Err()}
		}
		if delay *= 2; delay > identityRetryCap {
			delay = identityRetryCap
		}
	}
}

func modeName(mode hostidentity.Mode) string {
	if mode == hostidentity.ModeHostKey {
		return "host_key"
	}
	return "legacy_bearer"
}

// waitWithProgress sleeps for delay while reporting progress, so a Guard that
// is merely waiting for the control plane is not mistaken for a hung one.
func waitWithProgress(ctx context.Context, delay time.Duration, progress func()) bool {
	deadline := time.NewTimer(delay)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return true
		case <-tick.C:
			if progress != nil {
				progress()
			}
		}
	}
}

// holdUntilStopped keeps a revoked or unrecoverable Guard alive but idle.
func holdUntilStopped(ctx context.Context, cause error, log *slog.Logger, progress func()) error {
	sdnotify.Status("identity " + strings.TrimPrefix(cause.Error(), "kombify Guard ") + "; waiting for a new pairing")
	for waitWithProgress(ctx, identityHoldInterval, progress) {
		log.Warn("guard_execution_stopped", "reason", cause.Error())
	}
	return nil
}

// identityHTTPClient returns an HTTP client whose requests to the control plane
// carry the host-key proofs when the identity is key-bound.
func (r guardIdentityRuntime) httpClient(timeout time.Duration) *http.Client {
	client := &http.Client{
		Timeout:       timeout,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	if r.client != nil {
		client.Transport = r.client.Transport(http.DefaultTransport)
	}
	return client
}

// guardIdentityRevoked reports whether the control plane revoked this identity.
func guardIdentityRevoked(manager *hostidentity.Manager) bool {
	return manager != nil && manager.Record().State == hostidentity.StateRevoked
}

// watchIdentityRevocation cancels the Guard's work once the identity transport
// observes that the control plane revoked it.
func watchIdentityRevocation(ctx context.Context, manager *hostidentity.Manager, stop context.CancelFunc) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if guardIdentityRevoked(manager) {
				stop()
				return
			}
		}
	}
}
