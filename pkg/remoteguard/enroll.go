package remoteguard

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/discovery"
	"github.com/kombifyio/techstack/pkg/pairingtoken"
	"github.com/kombifyio/techstack/pkg/secrets"
	"golang.org/x/crypto/ssh"
)

const (
	StepConnectSSH       = "remote_ssh_connect"
	StepRunInstaller     = "remote_ssh_install"
	StepAwaitRegistration = "remote_ssh_await_registration"

	ReasonSSHNotReady = "remote_ssh_not_ready"
	ReasonSSHAuth     = "remote_ssh_auth_failed"
	ReasonInstall     = "remote_ssh_install_failed"
	ReasonTimeout     = "remote_ssh_timeout"
	ReasonCanceled    = "remote_ssh_canceled"
)

type Credentials struct {
	Host       string
	Port       int
	User       string
	Password   string
	PrivateKey string
	UseSudo    bool
}

type Request struct {
	Credentials
	ControlPlaneURL string
	PairingToken    string
}

type Progress func(step, message string, progress int)

type Enroller struct {
	timeout  time.Duration
	hostKeys *discovery.HostKeyStore
	workers  controlplane.WorkerStore
	dial     func(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error)
}

type Config struct {
	Timeout  time.Duration
	HostKeys *discovery.HostKeyStore
	Workers  controlplane.WorkerStore
	Dial     func(network, addr string, config *ssh.ClientConfig) (*ssh.Client, error)
}

func NewEnroller(cfg Config) *Enroller {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	dial := cfg.Dial
	if dial == nil {
		dial = ssh.Dial
	}
	hostKeys := cfg.HostKeys
	if hostKeys == nil {
		hostKeys = discovery.NewHostKeyStore("")
	}
	return &Enroller{
		timeout:  timeout,
		hostKeys: hostKeys,
		workers:  cfg.Workers,
		dial:     dial,
	}
}

func (e *Enroller) Enroll(ctx context.Context, tenantID string, req Request, progress Progress) error {
	if e == nil {
		return errors.New("remote guard enroller is not configured")
	}
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	emit := func(step, message string, pct int) {
		if progress != nil {
			progress(step, message, pct)
		}
	}

	host := strings.TrimSpace(req.Host)
	user := strings.TrimSpace(req.User)
	if host == "" || user == "" {
		return errors.New("remote SSH host and user are required")
	}
	port := req.Port
	if port <= 0 {
		port = 22
	}

	emit(StepConnectSSH, "Connecting to your Node over SSH…", 10)

	authMethods, err := sshAuthMethods(req.Credentials)
	if err != nil {
		return err
	}
	if len(authMethods) == 0 {
		return errors.New("remote SSH credentials are required")
	}

	hostAddr := fmt.Sprintf("%s:%d", host, port)
	clientConfig := &ssh.ClientConfig{
		User: user,
		Auth: authMethods,
		// Pin the host key per tenant: one tenant's trust decision never
		// blocks another tenant at a reused address.
		HostKeyCallback: e.hostKeys.GetScopedCallback(tenantID, hostAddr),
		Timeout:         30 * time.Second,
	}

	client, err := e.dial("tcp", hostAddr, clientConfig)
	if err != nil {
		return fmt.Errorf("%w: %v", errSSHNotReady, err)
	}
	defer client.Close()

	installCommand, err := guardbootstrap.RenderInstallerOneLiner(req.ControlPlaneURL, req.PairingToken)
	if err != nil {
		return err
	}
	remoteCommand := wrapRemoteCommand(installCommand, req.UseSudo, user)

	emit(StepRunInstaller, "Installing and enrolling the Guard over SSH…", 35)

	output, runErr := runSSHCommand(ctx, client, remoteCommand)
	if runErr != nil {
		return fmt.Errorf("%w: %v\n%s", errInstallFailed, runErr, secrets.Redact(output))
	}

	emit(StepAwaitRegistration, "Waiting for Guard registration…", 75)

	parsed, parseErr := pairingtoken.Parse(req.PairingToken)
	if parseErr != nil {
		return parseErr
	}
	if err := e.waitForTokenRedemption(ctx, tenantID, parsed.TokenHash); err != nil {
		return err
	}

	emit("remote_ssh_enrolled", "Guard enrolled over SSH", 100)
	return nil
}

var (
	errSSHNotReady     = errors.New(ReasonSSHNotReady)
	errInstallFailed   = errors.New(ReasonInstall)
	errAwaitTimeout    = errors.New(ReasonTimeout)
	errAwaitCanceled   = errors.New(ReasonCanceled)
)

func ClassifyEnrollmentError(err error) (reasonCode, message string, retryable bool) {
	if err == nil {
		return "", "", false
	}
	switch {
	case errors.Is(err, errSSHNotReady):
		return ReasonSSHNotReady, "Could not reach the server over SSH. Check host, port, firewall, and that SSH is running.", true
	case errors.Is(err, errInstallFailed):
		return ReasonInstall, "The remote installer did not complete successfully. Review SSH access and try again.", true
	case errors.Is(err, errAwaitTimeout):
		return ReasonTimeout, "The installer finished but Guard registration did not complete in time.", true
	case errors.Is(err, errAwaitCanceled):
		return ReasonCanceled, "Remote enrollment was canceled.", false
	}
	msg := strings.ToLower(err.Error())
	switch {
	case strings.Contains(msg, "unable to authenticate") || strings.Contains(msg, "permission denied"):
		return ReasonSSHAuth, "SSH authentication failed. Check the username, password, or SSH key.", true
	case strings.Contains(msg, "host key"):
		return ReasonSSHAuth, err.Error(), true
	default:
		return ReasonInstall, err.Error(), true
	}
}

func sshAuthMethods(creds Credentials) ([]ssh.AuthMethod, error) {
	methods := []ssh.AuthMethod{}
	if key := strings.TrimSpace(creds.PrivateKey); key != "" {
		signer, err := ssh.ParsePrivateKey([]byte(key))
		if err != nil {
			return nil, fmt.Errorf("parse SSH private key: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if password := strings.TrimSpace(creds.Password); password != "" {
		methods = append(methods, ssh.Password(password))
	}
	return methods, nil
}

func wrapRemoteCommand(installCommand string, useSudo bool, user string) string {
	installCommand = strings.TrimSpace(installCommand)
	if !useSudo || user == "root" {
		return installCommand
	}
	escaped := strings.ReplaceAll(installCommand, `'`, `'\''`)
	return fmt.Sprintf(
		`if [ "$(id -u)" = 0 ]; then %s; elif command -v sudo >/dev/null 2>&1; then sudo -n bash -lc '%s' || sudo bash -lc '%s'; else echo 'sudo is required for this user'; exit 1; fi`,
		installCommand, escaped, escaped,
	)
}

func runSSHCommand(ctx context.Context, client *ssh.Client, command string) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()

	type result struct {
		output []byte
		err    error
	}
	done := make(chan result, 1)
	go func() {
		output, runErr := session.CombinedOutput(command)
		done <- result{output: output, err: runErr}
	}()

	select {
	case <-ctx.Done():
		_ = session.Close()
		return "", ctx.Err()
	case res := <-done:
		return string(res.output), res.err
	}
}

func (e *Enroller) waitForTokenRedemption(ctx context.Context, tenantID, tokenHash string) error {
	if e.workers == nil {
		return nil
	}
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		token, err := e.workers.GetPairingTokenByHash(ctx, tenantID, tokenHash)
		if err == nil && token != nil && strings.EqualFold(strings.TrimSpace(token.Status), "used") {
			return nil
		}
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return errAwaitTimeout
			}
			return errAwaitCanceled
		case <-ticker.C:
		}
	}
}