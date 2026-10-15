// Package hostidentity is the agent side of kombify Guard's host-bound
// identity: it owns the Ed25519 identity key and the local enrollment state
// machine on the server, redeems a pairing capability, and proves possession
// of the key on every request to the control plane.
//
// Local enrollment states:
//
//	unbound          no binding with the control plane is known
//	redeeming        a redemption attempt started; credential not yet confirmed
//	bound            the control plane confirmed the credential with a valid proof
//	recovery-needed  the binding cannot be completed without a new capability
//	revoked          the control plane revoked this identity; the Guard stops executing
package hostidentity

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/guardidentity"
)

// State is the local enrollment state.
type State string

const (
	StateUnbound        State = "unbound"
	StateRedeeming      State = "redeeming"
	StateBound          State = "bound"
	StateRecoveryNeeded State = "recovery-needed"
	StateRevoked        State = "revoked"
)

const (
	recordSchema   = "kombify.guard.enrollment-state/v1"
	keyFileName    = "identity.key"
	nextKeyName    = "identity.key.next"
	recordFileName = "enrollment-state.json"
	// EnrollmentFileName is the state-directory copy of the enrollment
	// response used when the configured enrollment path is read-only.
	EnrollmentFileName = "agent-enrollment.json"
	bootstrapFileName  = "bootstrap.json"
	// DefaultRotateAfter is the key age after which the Guard rotates its key.
	DefaultRotateAfter = 90 * 24 * time.Hour
)

// ErrKeyLost means the state says the identity was bound but its key is gone.
var ErrKeyLost = errors.New("kombify Guard identity key is missing; the server must be paired again")

// Record is the non-secret local enrollment state persisted beside the key.
type Record struct {
	Schema        string    `json:"schema"`
	State         State     `json:"state"`
	AttemptID     string    `json:"attempt_id,omitempty"`
	KeyThumbprint string    `json:"key_thumbprint,omitempty"`
	KeyCreatedAt  time.Time `json:"key_created_at,omitempty"`
	// EnrollmentAttemptID names the attempt whose enrollment response is
	// persisted, so a crash after persistence resumes at confirmation.
	EnrollmentAttemptID string    `json:"enrollment_attempt_id,omitempty"`
	Reason              string    `json:"reason,omitempty"`
	UpdatedAt           time.Time `json:"updated_at"`
}

// Manager owns the identity key and the enrollment record in one directory.
type Manager struct {
	dir  string
	root *os.Root
	now  func() time.Time

	mu  sync.Mutex
	rec Record
	key ed25519.PrivateKey
}

// Open prepares the state directory and loads the enrollment record. A missing
// record is the unbound state; an unreadable one is set aside and treated as
// unbound, because the control plane remains the authority on the binding.
func Open(dir string) (*Manager, error) {
	if dir == "" {
		return nil, errors.New("kombify Guard state directory is not configured")
	}
	root, err := privatefs.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("admit kombify Guard state directory: %w", err)
	}
	m := &Manager{dir: dir, root: root, now: func() time.Time { return time.Now().UTC() }}
	m.rec = Record{Schema: recordSchema, State: StateUnbound}
	data, err := privatefs.ReadFile(m.root, recordFileName)
	switch {
	case errors.Is(err, os.ErrNotExist):
	case err != nil:
		_ = root.Close()
		return nil, fmt.Errorf("read kombify Guard enrollment state: %w", err)
	default:
		var loaded Record
		if json.Unmarshal(data, &loaded) == nil && loaded.Schema == recordSchema && validState(loaded.State) {
			m.rec = loaded
		} else {
			_ = m.root.Rename(recordFileName, recordFileName+".corrupt")
		}
	}
	return m, nil
}

func validState(state State) bool {
	switch state {
	case StateUnbound, StateRedeeming, StateBound, StateRecoveryNeeded, StateRevoked:
		return true
	}
	return false
}

// Dir returns the state directory.
func (m *Manager) Dir() string { return m.dir }

// OpenCustodyRoot gives a lifecycle consumer its own descriptor for this
// admitted directory. The consumer closes it; no pathname is reopened.
func (m *Manager) OpenCustodyRoot() (*os.Root, error) { return m.root.OpenRoot(".") }

// Record returns a copy of the current enrollment record.
func (m *Manager) Record() Record {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rec
}

func (m *Manager) saveLocked() error {
	m.rec.Schema = recordSchema
	m.rec.UpdatedAt = m.now()
	data, err := json.MarshalIndent(m.rec, "", "  ")
	if err != nil {
		return err
	}
	return privatefs.Replace(m.root, recordFileName, append(data, '\n'), 0o600)
}

// SetState persists a state transition with a non-secret reason.
func (m *Manager) SetState(state State, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.State, m.rec.Reason = state, reason
	return m.saveLocked()
}

// ReasonDecommissioned is recorded when a revoked Guard destroyed its secrets.
const ReasonDecommissioned = "decommissioned"

// Decommission destroys everything that could authenticate this server to the
// control plane after the control plane confirmed the identity's revocation: the
// identity key (and a staged rotation key), a persisted pairing capability and
// the enrollment responses, which carry the worker credential. Only the
// non-secret record stays, in the revoked state, so a restart idles instead of
// starting a new enrollment on its own. A new pairing capability starts over.
// It is safe to repeat.
func (m *Manager) Decommission(enrollmentPaths ...string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var failure error
	for _, name := range []string{keyFileName, nextKeyName, bootstrapFileName, EnrollmentFileName} {
		if err := wipeRootedSecretFile(m.root, name); err != nil {
			failure = errors.Join(failure, err)
		}
	}
	for _, path := range enrollmentPaths {
		if path == "" {
			continue
		}
		var err error
		if leaf, inside := m.custodyLeaf(path); inside {
			err = wipeRootedSecretFile(m.root, leaf)
		} else {
			err = wipeSecretFile(path)
		}
		if err != nil {
			failure = errors.Join(failure, err)
		}
	}
	m.key = nil
	m.rec.State, m.rec.Reason = StateRevoked, ReasonDecommissioned
	m.rec.KeyThumbprint, m.rec.AttemptID, m.rec.EnrollmentAttemptID = "", "", ""
	return errors.Join(failure, m.saveLocked())
}

// BeginAttempt starts a redemption attempt: a fresh non-secret attempt id is
// persisted with the redeeming state before any network call is made.
func (m *Manager) BeginAttempt() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("create enrollment attempt id: %w", err)
	}
	attempt := base64.RawURLEncoding.EncodeToString(raw[:])
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.State, m.rec.AttemptID, m.rec.Reason, m.rec.EnrollmentAttemptID = StateRedeeming, attempt, "", ""
	return attempt, m.saveLocked()
}

// MarkEnrollmentPersisted records that the response of attempt is on disk.
func (m *Manager) MarkEnrollmentPersisted(attempt string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rec.EnrollmentAttemptID = attempt
	return m.saveLocked()
}

// EnsureKey loads the identity key or creates it on first use. A missing key
// for an identity that was bound is reported as ErrKeyLost (and the state
// becomes recovery-needed): a new key cannot replace a confirmed binding.
func (m *Manager) EnsureKey() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.key != nil {
		return nil
	}
	data, err := privatefs.ReadFile(m.root, keyFileName)
	switch {
	case err == nil:
		key, decodeErr := guardidentity.DecodePrivateKeyPEM(data)
		if decodeErr != nil {
			return decodeErr
		}
		m.key = key
		if m.rec.KeyThumbprint == "" {
			return m.recordKeyLocked(key, m.fileModTime(keyFileName))
		}
		return nil
	case !errors.Is(err, os.ErrNotExist):
		return fmt.Errorf("read kombify Guard identity key: %w", err)
	}
	lost := m.rec.State == StateBound && m.rec.KeyThumbprint != ""
	key, err := guardidentity.GenerateKey()
	if err != nil {
		return err
	}
	pemBytes, err := guardidentity.EncodePrivateKeyPEM(key)
	if err != nil {
		return err
	}
	if err := privatefs.Replace(m.root, keyFileName, pemBytes, 0o600); err != nil {
		return err
	}
	m.key = key
	if err := m.recordKeyLocked(key, m.now()); err != nil {
		return err
	}
	if lost {
		m.rec.State, m.rec.Reason = StateRecoveryNeeded, "identity_key_lost"
		if saveErr := m.saveLocked(); saveErr != nil {
			return saveErr
		}
		return ErrKeyLost
	}
	return nil
}

func (m *Manager) fileModTime(name string) time.Time {
	if info, err := m.root.Stat(name); err == nil {
		return info.ModTime().UTC()
	}
	return m.now()
}

func (m *Manager) recordKeyLocked(key ed25519.PrivateKey, created time.Time) error {
	thumbprint, err := guardidentity.PublicJWK(key).Thumbprint()
	if err != nil {
		return err
	}
	m.rec.KeyThumbprint, m.rec.KeyCreatedAt = thumbprint, created.UTC()
	return m.saveLocked()
}

// Key returns the loaded identity key; EnsureKey must have succeeded.
func (m *Manager) Key() ed25519.PrivateKey {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.key
}

// KeyAge reports how long the current key has existed.
func (m *Manager) KeyAge() time.Duration {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rec.KeyCreatedAt.IsZero() {
		return 0
	}
	return m.now().Sub(m.rec.KeyCreatedAt)
}

// StageRotation creates the next key and persists it beside the current one,
// so a crash between the control plane accepting the rotation and the local
// switch can be completed on the next start.
func (m *Manager) StageRotation() (ed25519.PrivateKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if data, err := privatefs.ReadFile(m.root, nextKeyName); err == nil {
		if key, decodeErr := guardidentity.DecodePrivateKeyPEM(data); decodeErr == nil {
			return key, nil
		}
	}
	key, err := guardidentity.GenerateKey()
	if err != nil {
		return nil, err
	}
	pemBytes, err := guardidentity.EncodePrivateKeyPEM(key)
	if err != nil {
		return nil, err
	}
	return key, privatefs.Replace(m.root, nextKeyName, pemBytes, 0o600)
}

// PendingRotation returns a staged key that has not been committed.
func (m *Manager) PendingRotation() ed25519.PrivateKey {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := privatefs.ReadFile(m.root, nextKeyName)
	if err != nil {
		return nil
	}
	key, err := guardidentity.DecodePrivateKeyPEM(data)
	if err != nil {
		return nil
	}
	return key
}

// CommitRotation makes the staged key the identity key.
func (m *Manager) CommitRotation() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	data, err := privatefs.ReadFile(m.root, nextKeyName)
	if err != nil {
		return fmt.Errorf("read staged identity key: %w", err)
	}
	key, err := guardidentity.DecodePrivateKeyPEM(data)
	if err != nil {
		return err
	}
	if err := privatefs.Replace(m.root, keyFileName, data, 0o600); err != nil {
		return err
	}
	_ = wipeRootedSecretFile(m.root, nextKeyName)
	m.key = key
	return m.recordKeyLocked(key, m.now())
}

// DiscardRotation drops a staged key the control plane did not accept.
func (m *Manager) DiscardRotation() { _ = wipeRootedSecretFile(m.root, nextKeyName) }
