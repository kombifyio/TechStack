package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"golang.org/x/crypto/ssh"
)

// ErrRuntimeHostKeyChanged reports that a target presented a host key other
// than the one pinned for its address. The connection must not continue: a
// changed key on a managed target is either a re-provisioned node (delete the
// stale pin deliberately) or an interception attempt.
var ErrRuntimeHostKeyChanged = errors.New("runtime target host key changed")

// ErrRuntimeHostKeyUnverifiable reports that a presented host key could not be
// checked against the pin store. The handshake is rejected instead of trusting
// the key.
var ErrRuntimeHostKeyUnverifiable = errors.New("runtime target host key cannot be verified")

func runtimeHostKeyUnverifiable(address, reason string) error {
	return fmt.Errorf("%w: %s: %s", ErrRuntimeHostKeyUnverifiable, strings.TrimSpace(address), reason)
}

// RuntimeHostKeyStore pins the first observed SSH host key per managed target
// address and verifies every later bootstrap connection against it.
//
// Trust state belongs to the node lifecycle, not to a single dial: a managed
// node is provisioned once and then re-addressed by rollout, retry and
// diagnostics. Pinning per host:port means a fresh lease at a reused address
// requires a deliberate pin reset, while an in-place node keeps its identity.
//
// The store never fails open: a pin mismatch, an unreadable file or a
// persistence failure is returned to the caller and the SSH handshake is
// rejected.
type RuntimeHostKeyStore struct {
	path string

	mu      sync.Mutex
	pins    map[string]string
	loadErr error
}

// NewRuntimeHostKeyStore creates a store persisted at path. An empty path
// keeps the pins for the process lifetime only.
func NewRuntimeHostKeyStore(path string) *RuntimeHostKeyStore {
	return &RuntimeHostKeyStore{
		path: strings.TrimSpace(path),
		pins: map[string]string{},
	}
}

// Load reads the persisted pins. A missing file is a valid empty store; a
// malformed file is remembered and returned by every verification, so a
// corrupt trust file can never silently degrade to trust-on-first-use.
func (s *RuntimeHostKeyStore) Load() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path == "" {
		return nil
	}
	data, err := os.ReadFile(s.path)
	if errors.Is(err, os.ErrNotExist) {
		s.loadErr = nil
		return nil
	}
	if err != nil {
		s.loadErr = fmt.Errorf("read runtime host key store: %w", err)
		return s.loadErr
	}
	pins := map[string]string{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &pins); err != nil {
			s.loadErr = fmt.Errorf("parse runtime host key store: %w", err)
			return s.loadErr
		}
	}
	for address, fingerprint := range pins {
		address = normalizeRuntimeHostKeyAddress(address)
		fingerprint = strings.TrimSpace(fingerprint)
		if address == "" || fingerprint == "" {
			continue
		}
		s.pins[address] = fingerprint
	}
	s.loadErr = nil
	return nil
}

// VerifyReadOnly checks a key against the pin without writing trust state.
// Diagnostics use it so a failure investigation never pins a node; an
// unpinned address passes.
func (s *RuntimeHostKeyStore) VerifyReadOnly(address string, key ssh.PublicKey) error {
	if s == nil {
		return runtimeHostKeyUnverifiable(address, "no host key store is configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	normalized := normalizeRuntimeHostKeyAddress(address)
	if normalized == "" || key == nil {
		return runtimeHostKeyUnverifiable(address, "missing address or host key")
	}
	address = normalized
	pinned, ok := s.pins[address]
	if !ok {
		return nil
	}
	return verifyRuntimeHostKeyFingerprint(address, pinned, key)
}

// VerifyAndPin verifies a key and pins it on first contact. Bootstrap uses it
// so the node observed during enrollment becomes the identity later dials must
// present.
func (s *RuntimeHostKeyStore) VerifyAndPin(address string, key ssh.PublicKey) error {
	if s == nil {
		return runtimeHostKeyUnverifiable(address, "no host key store is configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.loadErr != nil {
		return s.loadErr
	}
	normalized := normalizeRuntimeHostKeyAddress(address)
	if normalized == "" || key == nil {
		return runtimeHostKeyUnverifiable(address, "missing address or host key")
	}
	address = normalized
	if pinned, ok := s.pins[address]; ok {
		return verifyRuntimeHostKeyFingerprint(address, pinned, key)
	}
	fingerprint := ssh.FingerprintSHA256(key)
	s.pins[address] = fingerprint
	if err := s.persistLocked(); err != nil {
		delete(s.pins, address)
		return err
	}
	return nil
}

// persistLocked writes the pins atomically. The caller holds s.mu.
func (s *RuntimeHostKeyStore) persistLocked() error {
	if s.path == "" {
		return nil
	}
	addresses := make([]string, 0, len(s.pins))
	for address := range s.pins {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	serialized := make(map[string]string, len(addresses))
	for _, address := range addresses {
		serialized[address] = s.pins[address]
	}
	data, err := json.MarshalIndent(serialized, "", "  ")
	if err != nil {
		return fmt.Errorf("encode runtime host key store: %w", err)
	}
	if dir := filepath.Dir(s.path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return fmt.Errorf("create runtime host key store directory: %w", err)
		}
	}
	temp, err := os.CreateTemp(filepath.Dir(s.path), ".runtime-host-keys-*.tmp")
	if err != nil {
		return fmt.Errorf("create runtime host key store temp file: %w", err)
	}
	tempName := temp.Name()
	defer func() { _ = os.Remove(tempName) }()
	if err := temp.Chmod(0o600); err != nil {
		_ = temp.Close()
		return fmt.Errorf("set runtime host key store permissions: %w", err)
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return fmt.Errorf("write runtime host key store: %w", err)
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return fmt.Errorf("sync runtime host key store: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close runtime host key store: %w", err)
	}
	if err := os.Rename(tempName, s.path); err != nil {
		return fmt.Errorf("replace runtime host key store: %w", err)
	}
	return nil
}

func verifyRuntimeHostKeyFingerprint(address, pinned string, key ssh.PublicKey) error {
	presented := ssh.FingerprintSHA256(key)
	if presented == pinned {
		return nil
	}
	return fmt.Errorf("%w: %s presented %s, pinned %s", ErrRuntimeHostKeyChanged, address, presented, pinned)
}

func normalizeRuntimeHostKeyAddress(address string) string {
	address = strings.ToLower(strings.TrimSpace(address))
	host, port, ok := strings.Cut(address, ":")
	if !ok || host == "" {
		return ""
	}
	return host + ":" + port
}
