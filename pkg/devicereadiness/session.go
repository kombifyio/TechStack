package devicereadiness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kombifyio/techstack/pkg/secrets"
)

// A session is one executor's connection to one unenrolled device.
//
// The transport lives here rather than in the control-plane job package so that
// the operator-side CLI can hold exactly the same authority without dragging in
// the control plane. Everything an executor may do to a device goes through
// this file, which makes it the one place to look when asking what we can do to
// a machine that has not yet agreed to anything.

// Default budgets. A readiness run happens while someone is waiting, so it is
// bounded everywhere: an unreachable device must fail in seconds, not minutes.
const (
	DefaultDialTimeout    = 15 * time.Second
	DefaultProbeTimeout   = 90 * time.Second
	DefaultCommandTimeout = 5 * time.Minute
	// maxCapturedOutput bounds what a receipt keeps from any one command. It
	// matches the bootstrap receipt cap so evidence stays uniform.
	maxCapturedOutput = 12 * 1024
)

// Target names the device and how to reach it.
type Target struct {
	Host string
	Port int
	User string
}

func (t Target) address() string {
	port := t.Port
	if port == 0 {
		port = 22
	}
	return net.JoinHostPort(t.Host, strconv.Itoa(port))
}

// Auth is how the executor proves who it is. Exactly one of the two is used;
// a password is accepted because a device that was just installed from
// removable media commonly has nothing else yet.
type Auth struct {
	PrivateKeyPEM []byte
	Passphrase    []byte
	Password      string
}

func (a Auth) methods() ([]ssh.AuthMethod, error) {
	methods := make([]ssh.AuthMethod, 0, 2)
	if len(a.PrivateKeyPEM) > 0 {
		var signer ssh.Signer
		var err error
		if len(a.Passphrase) > 0 {
			signer, err = ssh.ParsePrivateKeyWithPassphrase(a.PrivateKeyPEM, a.Passphrase)
		} else {
			signer, err = ssh.ParsePrivateKey(a.PrivateKeyPEM)
		}
		if err != nil {
			return nil, fmt.Errorf("device readiness: private key unusable: %w", err)
		}
		methods = append(methods, ssh.PublicKeys(signer))
	}
	if a.Password != "" {
		methods = append(methods, ssh.Password(a.Password))
	}
	if len(methods) == 0 {
		return nil, errors.New("device readiness: no authentication was provided")
	}
	return methods, nil
}

// HostKeyPolicy decides what to do with the key the device presents.
//
// The device is not enrolled, so there is no trust store to consult, and the
// executor is about to change files on it and possibly open a way out for it.
// Accepting whatever answers would make that authority available to whatever
// answers. So the caller either already knows the fingerprint, or it is
// surfaced to the operator, who is standing next to the machine.
type HostKeyPolicy struct {
	// ExpectedFingerprint pins the key. When set, nothing else is accepted.
	ExpectedFingerprint string
	// Observe is called with the fingerprint actually presented. A caller that
	// pins nothing must show this to the operator; returning an error refuses
	// the connection.
	Observe func(fingerprint string) error
}

// Fingerprint renders a host key the way OpenSSH does, so an operator can
// compare it with what the device itself reports.
func Fingerprint(key ssh.PublicKey) string {
	sum := sha256.Sum256(key.Marshal())
	return "SHA256:" + strings.TrimRight(base64.StdEncoding.EncodeToString(sum[:]), "=")
}

func (p HostKeyPolicy) callback(observed *string) ssh.HostKeyCallback {
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		fingerprint := Fingerprint(key)
		*observed = fingerprint
		if p.ExpectedFingerprint != "" {
			if fingerprint != p.ExpectedFingerprint {
				return fmt.Errorf("device readiness: host key %s does not match the pinned key", fingerprint)
			}
			return nil
		}
		if p.Observe == nil {
			return errors.New("device readiness: refusing an unverified host key with no way to show it to the operator")
		}
		return p.Observe(fingerprint)
	}
}

// Session is a connected executor.
type Session struct {
	client *ssh.Client
	target Target

	// hostKeyFingerprint is what the device presented, kept so a receipt can
	// record which machine was changed.
	hostKeyFingerprint string

	// egress is the temporary way out, when one was opened.
	egress *Egress
}

// Connect opens a session to the device.
func Connect(ctx context.Context, target Target, auth Auth, policy HostKeyPolicy) (*Session, error) {
	methods, err := auth.methods()
	if err != nil {
		return nil, err
	}
	var fingerprint string
	config := &ssh.ClientConfig{
		User:            target.User,
		Auth:            methods,
		HostKeyCallback: policy.callback(&fingerprint),
		Timeout:         DefaultDialTimeout,
	}

	type dialResult struct {
		client *ssh.Client
		err    error
	}
	done := make(chan dialResult, 1)
	go func() {
		client, dialErr := ssh.Dial("tcp", target.address(), config)
		done <- dialResult{client: client, err: dialErr}
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-done:
		if result.err != nil {
			return nil, fmt.Errorf("device readiness: cannot reach %s@%s: %w", target.User, target.address(), result.err)
		}
		return &Session{client: result.client, target: target, hostKeyFingerprint: fingerprint}, nil
	}
}

// Close releases the connection and anything opened through it.
func (s *Session) Close() error {
	if s.egress != nil {
		_ = s.egress.Close()
		s.egress = nil
	}
	return s.client.Close()
}

// HostKeyFingerprint is the key the device presented on this connection.
func (s *Session) HostKeyFingerprint() string { return s.hostKeyFingerprint }

// Probe runs the readiness observation and returns what it measured.
func (s *Session) Probe(ctx context.Context, env ProbeEnvironment) (Facts, error) {
	result, err := s.run(ctx, env.Command(), "readiness probe", []byte(ProbeScript), DefaultProbeTimeout)
	if err != nil {
		return Facts{}, err
	}
	// The probe reports what it could not measure inside its own output, so a
	// non-zero exit is a failure of the run itself and not of the device.
	if result.ExitCode != 0 && strings.TrimSpace(result.Stdout) == "" {
		return Facts{}, fmt.Errorf("device readiness: the probe did not run: %s", firstLine(result.Stderr))
	}
	return DecodeFacts([]byte(result.Stdout), time.Now())
}

// commandResult is one command's outcome, already bounded and redacted.
type commandResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exitCode"`
	Stdout   string `json:"stdout,omitempty"`
	Stderr   string `json:"stderr,omitempty"`
}

// run executes command on the device. label is what the receipt records: the
// command as the catalog states it, not the elevation wrapper around it, so
// evidence stays readable by whoever has to understand what was done.
func (s *Session) run(ctx context.Context, command, label string, stdin []byte, budget time.Duration) (commandResult, error) {
	if label == "" {
		label = command
	}
	session, err := s.client.NewSession()
	if err != nil {
		return commandResult{}, fmt.Errorf("device readiness: cannot open a shell on the device: %w", err)
	}
	defer func() { _ = session.Close() }()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr
	if stdin != nil {
		session.Stdin = bytes.NewReader(stdin)
	}

	runCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()

	select {
	case <-runCtx.Done():
		// Closing the session is what actually interrupts the remote command;
		// letting it run on would leave the device half-changed with nobody
		// watching.
		_ = session.Signal(ssh.SIGKILL)
		_ = session.Close()
		return commandResult{}, fmt.Errorf("device readiness: %q did not finish within %s", label, budget)
	case runErr := <-done:
		result := commandResult{
			Command: label,
			Stdout:  capture(stdout.String()),
			Stderr:  capture(stderr.String()),
		}
		var exitErr *ssh.ExitError
		switch {
		case runErr == nil:
			result.ExitCode = 0
		case errors.As(runErr, &exitErr):
			result.ExitCode = exitErr.ExitStatus()
		default:
			return result, fmt.Errorf("device readiness: %q failed to run: %w", label, runErr)
		}
		return result, nil
	}
}

// capture bounds and redacts anything a device sends back. Output from an
// unenrolled device is untrusted input that ends up in a stored receipt, so it
// is truncated before it is kept and never interpreted.
func capture(value string) string {
	value = secrets.Redact(value)
	if len(value) <= maxCapturedOutput {
		return value
	}
	return value[:maxCapturedOutput] + "\n... truncated ..."
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if idx := strings.IndexByte(value, '\n'); idx >= 0 {
		return value[:idx]
	}
	if value == "" {
		return "no output"
	}
	return value
}
