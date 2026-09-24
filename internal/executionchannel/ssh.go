// Package executionchannel runs bounded Day-2 commands on a managed node over
// the control plane's host-key-pinned SSH execution channel.
//
// # Authority
//
// The channel is Kombify's management login on a managed node. It never
// touches a provider API and never creates, replaces or deletes a provider
// resource; provider effects stay in provider control. Every connection pins
// the host key recorded in provider/runtime custody or Guard inventory and
// fails closed without it.
//
// # Secrets
//
// Target credentials are used in memory for one connection and are never
// logged, returned or persisted by this package.
package executionchannel

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	dialTimeout    = 15 * time.Second
	commandTimeout = 45 * time.Second
	maxOutputBytes = 64 << 10
)

// PinnedHostKey parses host-key custody (an authorized-key line, a
// known_hosts-style line or a SHA256 fingerprint) and returns the exact
// fingerprint, the key algorithm when known and a callback that accepts only
// that key.
func PinnedHostKey(raw string) (string, string, ssh.HostKeyCallback, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", nil, errors.New("host key missing")
	}
	expected := raw
	algorithm := ""
	if !strings.HasPrefix(expected, "SHA256:") {
		var key ssh.PublicKey
		if parsed, _, _, _, err := ssh.ParseAuthorizedKey([]byte(raw)); err == nil {
			key = parsed
		} else {
			fields := strings.Fields(raw)
			for index := 0; index+1 < len(fields); index++ {
				if strings.HasPrefix(fields[index], "ssh-") || strings.HasPrefix(fields[index], "ecdsa-") || strings.HasPrefix(fields[index], "sk-") {
					parsed, _, _, _, parseErr := ssh.ParseAuthorizedKey([]byte(fields[index] + " " + fields[index+1]))
					if parseErr == nil {
						key = parsed
					}
					break
				}
			}
		}
		if key == nil {
			return "", "", nil, errors.New("invalid host key custody")
		}
		expected = ssh.FingerprintSHA256(key)
		algorithm = key.Type()
	}
	callback := func(_ string, _ net.Addr, key ssh.PublicKey) error {
		if ssh.FingerprintSHA256(key) != expected {
			return errors.New("ssh host key mismatch")
		}
		return nil
	}
	return expected, algorithm, callback, nil
}

// HasCredential reports whether the target carries any login credential.
func HasCredential(target *jobs.ManagedRuntimeTarget) bool {
	if target == nil {
		return false
	}
	for _, value := range []string{target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHProviderPrivateKey, target.SSHPassword} {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

// Dial opens one SSH client to the target, accepting only the host key whose
// fingerprint equals the one the caller already trusted.
func Dial(ctx context.Context, target *jobs.ManagedRuntimeTarget, fingerprint string) (*ssh.Client, error) {
	if target == nil {
		return nil, errors.New("managed runtime target missing")
	}
	trustedFingerprint, algorithm, callback, err := PinnedHostKey(target.SSHHostKey)
	if err != nil || trustedFingerprint != fingerprint {
		if err == nil {
			err = errors.New("ssh host key custody changed")
		}
		return nil, err
	}
	auth := make([]ssh.AuthMethod, 0, 4)
	for _, signer := range targetSigners(target) {
		auth = append(auth, ssh.PublicKeys(signer))
	}
	if target.SSHPassword != "" {
		auth = append(auth, ssh.Password(target.SSHPassword))
	}
	if len(auth) == 0 {
		return nil, errors.New("ssh credential unavailable")
	}
	address := net.JoinHostPort(target.Host, strconv.Itoa(target.SSHPort))
	dialer := net.Dialer{Timeout: dialTimeout}
	network, err := dialer.DialContext(ctx, "tcp", address)
	if err != nil {
		return nil, err
	}
	config := &ssh.ClientConfig{User: target.SSHUser, Auth: auth, HostKeyCallback: callback, Timeout: dialTimeout}
	if algorithm != "" {
		config.HostKeyAlgorithms = []string{algorithm}
	}
	connection, channels, requests, err := ssh.NewClientConn(network, address, config)
	if err != nil {
		network.Close()
		return nil, err
	}
	return ssh.NewClient(connection, channels, requests), nil
}

// ChannelKeyBlobs returns the base64 key blobs of every credential the
// control plane holds for the target. These keys ARE the execution channel,
// so owner-key revocation must never remove them.
func ChannelKeyBlobs(target *jobs.ManagedRuntimeTarget) []string {
	signers := targetSigners(target)
	blobs := make([]string, 0, len(signers))
	for _, signer := range signers {
		fields := strings.Fields(string(ssh.MarshalAuthorizedKey(signer.PublicKey())))
		if len(fields) >= 2 {
			blobs = append(blobs, fields[1])
		}
	}
	return blobs
}

func targetSigners(target *jobs.ManagedRuntimeTarget) []ssh.Signer {
	if target == nil {
		return nil
	}
	seen := map[string]bool{}
	signers := make([]ssh.Signer, 0, 3)
	for _, raw := range []string{target.SSHClientPrivateKey, target.SSHPrivateKey, target.SSHProviderPrivateKey} {
		raw = strings.TrimSpace(raw)
		if raw == "" || seen[raw] {
			continue
		}
		seen[raw] = true
		if signer, err := ssh.ParsePrivateKey([]byte(raw)); err == nil {
			signers = append(signers, signer)
		}
	}
	return signers
}

// Run executes exactly one bounded command with optional stdin and returns
// its combined, size-bounded output.
func Run(ctx context.Context, target *jobs.ManagedRuntimeTarget, fingerprint, command, stdin string) (string, error) {
	runCtx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()
	client, err := Dial(runCtx, target, fingerprint)
	if err != nil {
		return "", err
	}
	defer client.Close()
	session, err := client.NewSession()
	if err != nil {
		return "", err
	}
	defer session.Close()
	if stdin != "" {
		session.Stdin = strings.NewReader(stdin)
	}
	output := &boundedBuffer{limit: maxOutputBytes}
	session.Stdout, session.Stderr = output, output
	done := make(chan error, 1)
	go func() { done <- session.Run(command) }()
	select {
	case <-runCtx.Done():
		_ = session.Close()
		return output.String(), runCtx.Err()
	case err := <-done:
		return output.String(), err
	}
}

type boundedBuffer struct {
	limit int
	data  []byte
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if remaining := b.limit - len(b.data); remaining > 0 {
		if len(p) > remaining {
			b.data = append(b.data, p[:remaining]...)
		} else {
			b.data = append(b.data, p...)
		}
	}
	return len(p), nil
}

func (b *boundedBuffer) String() string { return string(b.data) }

var _ io.Writer = (*boundedBuffer)(nil)
