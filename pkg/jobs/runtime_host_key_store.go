package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrRuntimeHostKeyChanged reports that a target presented a host key other
// than the one pinned for its address. The connection must not continue: the
// server that took the pin still holds the address as far as the control
// plane can prove, so a changed key is a re-keyed node or an interception
// attempt. A pin retires only when its managed server is proven gone at the
// provider (migration 176).
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
// diagnostics. A pin is keyed by host:port and bound to the managed lease
// whose bootstrap took it. While that lease's server may still hold the
// address, a different key at the address is refused. Providers recycle
// addresses, so once the lease's provider resources are proven absent the
// runtime database retires the pin (migration 176) and the next server the
// provider assigns the address starts a fresh pin.
//
// The store never fails open: a pin mismatch, an unreadable file or a
// persistence failure is returned to the caller and the SSH handshake is
// rejected. A pin this process holds (from the file) is dropped only when the
// database recorded its retirement.
type RuntimeHostKeyStore struct {
	path    string
	backend RuntimeHostKeyPinBackend

	mu      sync.Mutex
	pins    map[string]string
	loadErr error
	// backendErr keeps the last failed backend load; every verification
	// retries the load first and refuses while it keeps failing.
	backendErr error
	// conflicts holds addresses whose disk pin contradicts the database pin.
	// Only those addresses are refused until an operator retires one pin.
	conflicts map[string]string
}

// RuntimeHostKeyPinBackend is the durable pin authority every control-plane
// instance shares. PinFirst stores the fingerprint only when the address has
// no pin yet and returns the pin that holds afterwards, so two instances that
// observe a target concurrently converge on one pin. RetiredPins returns the
// candidate addresses whose given fingerprint the database retired.
type RuntimeHostKeyPinBackend interface {
	LoadPins(ctx context.Context) (map[string]string, error)
	LookupPin(ctx context.Context, address string) (string, bool, error)
	PinFirst(ctx context.Context, address, fingerprint string, binding RuntimeHostKeyBinding) (string, error)
	RetiredPins(ctx context.Context, candidates map[string]string) (map[string]bool, error)
}

// RuntimeHostKeyBinding names the managed lease whose bootstrap dials a
// target. A first-contact pin is bound to it, and the runtime database
// retires the pin once that lease's provider resources are proven absent.
// The database keeps the binding only for a lease of that tenant whose
// recorded address is the pinned host.
type RuntimeHostKeyBinding struct {
	TenantID string
	LeaseID  string
}

func (b RuntimeHostKeyBinding) normalized() RuntimeHostKeyBinding {
	b.TenantID, b.LeaseID = strings.TrimSpace(b.TenantID), strings.TrimSpace(b.LeaseID)
	if b.TenantID == "" || b.LeaseID == "" {
		return RuntimeHostKeyBinding{}
	}
	return b
}

type runtimeHostKeyBindingKey struct{}

// WithRuntimeHostKeyBinding carries the managed lease of a bootstrap to the
// SSH host-key callback of its dials.
func WithRuntimeHostKeyBinding(ctx context.Context, binding RuntimeHostKeyBinding) context.Context {
	return context.WithValue(ctx, runtimeHostKeyBindingKey{}, binding.normalized())
}

func runtimeHostKeyBindingFrom(ctx context.Context) RuntimeHostKeyBinding {
	if ctx == nil {
		return RuntimeHostKeyBinding{}
	}
	binding, _ := ctx.Value(runtimeHostKeyBindingKey{}).(RuntimeHostKeyBinding)
	return binding
}

const runtimeHostKeyBackendTimeout = 10 * time.Second

// ErrRuntimeHostKeyPinStorePending reports that the durable pin store does not
// exist yet (the binary runs before its schema). The store then keeps the
// file-backed behavior for this process.
var ErrRuntimeHostKeyPinStorePending = errors.New("runtime host key pin store is not rolled out yet")

// NewRuntimeHostKeyStore creates a store persisted at path. An empty path
// keeps the pins for the process lifetime only.
func NewRuntimeHostKeyStore(path string) *RuntimeHostKeyStore {
	return NewRuntimeHostKeyStoreWithBackend(path, nil)
}

// NewRuntimeHostKeyStoreWithBackend keeps the pins in the durable backend.
// Pins the file at path holds (an instance whose data disk predates the
// backend) are honored beside the backend's, and every new pin is also
// written to the file while one is configured, so a binary that reads only
// the file keeps verifying the same pins.
func NewRuntimeHostKeyStoreWithBackend(path string, backend RuntimeHostKeyPinBackend) *RuntimeHostKeyStore {
	return &RuntimeHostKeyStore{
		path:      strings.TrimSpace(path),
		backend:   backend,
		pins:      map[string]string{},
		conflicts: map[string]string{},
	}
}

// ReadRuntimeHostKeyPinFile parses a pin file. A missing file is an empty set.
func ReadRuntimeHostKeyPinFile(path string) (map[string]string, error) {
	pins := map[string]string{}
	if strings.TrimSpace(path) == "" {
		return pins, nil
	}
	data, err := os.ReadFile(path) // #nosec G304 -- operator-configured data directory.
	if errors.Is(err, os.ErrNotExist) {
		return pins, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read runtime host key store: %w", err)
	}
	raw := map[string]string{}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("parse runtime host key store: %w", err)
		}
	}
	for address, fingerprint := range raw {
		address = NormalizeRuntimeHostKeyAddress(address)
		fingerprint = strings.TrimSpace(fingerprint)
		if address == "" || fingerprint == "" {
			continue
		}
		pins[address] = fingerprint
	}
	return pins, nil
}

// Load reads the persisted pins. A missing file is a valid empty store. A
// malformed file is remembered and refuses every verification; an
// unreachable backend refuses verifications until a retried load succeeds;
// a file pin that contradicts the backend refuses that address. Trust state
// never degrades to trust-on-first-use.
func (s *RuntimeHostKeyStore) Load() error {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	pins, err := ReadRuntimeHostKeyPinFile(s.path)
	if err != nil {
		s.loadErr = err
		return s.loadErr
	}
	s.loadErr = nil
	for address, fingerprint := range pins {
		s.pins[address] = fingerprint
	}
	return s.loadBackendLocked()
}

// loadBackendLocked merges the backend's pins. The caller holds s.mu.
func (s *RuntimeHostKeyStore) loadBackendLocked() error {
	if s.backend == nil {
		s.backendErr = nil
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeHostKeyBackendTimeout)
	durable, err := s.backend.LoadPins(ctx)
	cancel()
	if errors.Is(err, ErrRuntimeHostKeyPinStorePending) {
		s.backend = nil
		s.backendErr = nil
		return nil
	}
	if err != nil {
		s.backendErr = fmt.Errorf("%w: load runtime host key pins: %v", ErrRuntimeHostKeyUnverifiable, err)
		return s.backendErr
	}
	// A file pin the database lacks or contradicts is dropped only when the
	// database retired it; any other one keeps holding (or conflicts).
	stale := map[string]string{}
	for address, local := range s.pins {
		if fingerprint, ok := durable[address]; !ok || fingerprint != local {
			stale[address] = local
		}
	}
	if len(stale) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), runtimeHostKeyBackendTimeout)
		retired, retiredErr := s.backend.RetiredPins(ctx, stale)
		cancel()
		if retiredErr != nil {
			s.backendErr = fmt.Errorf("%w: load retired runtime host key pins: %v", ErrRuntimeHostKeyUnverifiable, retiredErr)
			return s.backendErr
		}
		for address := range retired {
			delete(s.pins, address)
		}
		if len(retired) > 0 {
			_ = s.persistLocked()
		}
	}
	s.backendErr = nil
	var conflicts []string
	for address, fingerprint := range durable {
		if local, ok := s.pins[address]; ok && local != fingerprint {
			s.conflicts[address] = fmt.Sprintf("pinned to %s in the database and %s on disk", fingerprint, local)
			conflicts = append(conflicts, address)
			continue
		}
		s.pins[address] = fingerprint
	}
	if len(conflicts) > 0 {
		sort.Strings(conflicts)
		return fmt.Errorf("%w: conflicting pins refused for %s", ErrRuntimeHostKeyChanged, strings.Join(conflicts, ", "))
	}
	return nil
}

// admitLocked returns the error that refuses this address before any pin is
// read: a corrupt file, a backend that still cannot be loaded, or a conflict.
func (s *RuntimeHostKeyStore) admitLocked(address string) error {
	if s.loadErr != nil {
		return s.loadErr
	}
	if s.backendErr != nil {
		if err := s.loadBackendLocked(); err != nil && s.backendErr != nil {
			return s.backendErr
		}
	}
	if reason, ok := s.conflicts[address]; ok {
		return fmt.Errorf("%w: %s %s", ErrRuntimeHostKeyChanged, address, reason)
	}
	return nil
}

// resolvePinLocked returns the pin that holds for an address whose presented
// key this process cannot accept from its own copy: the address is new to the
// process, or another instance may have pinned it, or the database may have
// retired the copy since. The database pin wins over a copy it retired; a
// copy it lacks but never retired keeps holding, and a copy it contradicts
// without a retirement refuses the address. Unless readOnly, the process copy
// and the file follow the database. The caller holds s.mu.
func (s *RuntimeHostKeyStore) resolvePinLocked(address string, readOnly bool) (string, bool, error) {
	cached, cachedOK := s.pins[address]
	if s.backend == nil {
		return cached, cachedOK, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), runtimeHostKeyBackendTimeout)
	defer cancel()
	durable, found, err := s.backend.LookupPin(ctx, address)
	if err != nil {
		return "", false, runtimeHostKeyUnverifiable(address, "the pin store is unavailable: "+err.Error())
	}
	if cachedOK && (!found || durable != cached) {
		retired, retiredErr := s.backend.RetiredPins(ctx, map[string]string{address: cached})
		if retiredErr != nil {
			return "", false, runtimeHostKeyUnverifiable(address, "the pin retirements are unavailable: "+retiredErr.Error())
		}
		if !retired[address] {
			if found {
				return "", false, fmt.Errorf("%w: %s pinned to %s in the database and %s on disk", ErrRuntimeHostKeyChanged, address, durable, cached)
			}
			return cached, true, nil
		}
		if !readOnly {
			delete(s.pins, address)
			_ = s.persistLocked()
		}
	}
	if !found {
		return "", false, nil
	}
	if !readOnly && (!cachedOK || cached != durable) {
		s.pins[address] = durable
		_ = s.persistLocked()
	}
	return durable, true, nil
}

// VerifyReadOnly checks a key against the pin without writing trust state.
// Diagnostics use it so a failure investigation never pins a node; an
// unpinned address (or one whose pin the database retired) passes.
func (s *RuntimeHostKeyStore) VerifyReadOnly(address string, key ssh.PublicKey) error {
	if s == nil {
		return runtimeHostKeyUnverifiable(address, "no host key store is configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized := normalizeRuntimeHostKeyAddress(address)
	if normalized == "" || key == nil {
		return runtimeHostKeyUnverifiable(address, "missing address or host key")
	}
	address = normalized
	if err := s.admitLocked(address); err != nil {
		return err
	}
	if pinned, ok := s.pins[address]; ok && pinned == ssh.FingerprintSHA256(key) {
		return nil
	}
	pinned, ok, err := s.resolvePinLocked(address, true)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	return verifyRuntimeHostKeyFingerprint(address, pinned, key)
}

// VerifyAndPin verifies a key and pins it on first contact. Bootstrap uses it
// so the node observed during enrollment becomes the identity later dials must
// present. The binding names the managed lease of the dial; the pin follows
// that lease's lifecycle.
func (s *RuntimeHostKeyStore) VerifyAndPin(address string, key ssh.PublicKey, binding RuntimeHostKeyBinding) error {
	if s == nil {
		return runtimeHostKeyUnverifiable(address, "no host key store is configured")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	normalized := normalizeRuntimeHostKeyAddress(address)
	if normalized == "" || key == nil {
		return runtimeHostKeyUnverifiable(address, "missing address or host key")
	}
	address = normalized
	if err := s.admitLocked(address); err != nil {
		return err
	}
	fingerprint := ssh.FingerprintSHA256(key)
	if pinned, ok := s.pins[address]; ok && pinned == fingerprint {
		return nil
	}
	if pinned, ok, err := s.resolvePinLocked(address, false); err != nil {
		return err
	} else if ok {
		return verifyRuntimeHostKeyFingerprint(address, pinned, key)
	}
	if s.backend != nil {
		ctx, cancel := context.WithTimeout(context.Background(), runtimeHostKeyBackendTimeout)
		pinned, err := s.backend.PinFirst(ctx, address, fingerprint, binding.normalized())
		cancel()
		if err != nil {
			return runtimeHostKeyUnverifiable(address, "the pin could not be stored: "+err.Error())
		}
		s.pins[address] = pinned
		// The database holds the pin; the file copy keeps a file-only binary
		// (a rollback) verifying it. A failed copy does not undo the pin.
		_ = s.persistLocked()
		return verifyRuntimeHostKeyFingerprint(address, pinned, key)
	}
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

// NormalizeRuntimeHostKeyAddress is the canonical pin key (lower-case host:port).
func NormalizeRuntimeHostKeyAddress(address string) string {
	return normalizeRuntimeHostKeyAddress(address)
}
