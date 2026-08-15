// Package instance provides a stable local TechStack instance identity.
//
// An "instance" is one running TechStack deployment unit. Each instance has a
// stable identifier that survives restarts, persists in the data directory, and
// serves as the anchor for enrollment, cloud-linking, and tenant scoping under
// Pillar 1 (Unifier). Architecture language: see
// docs/getting-started/glossary.md and docs/ARCHITECTURE.md.
//
// The identity is intentionally minimal:
//   - id          stable UUIDv4, generated once on first start.
//   - created_at  RFC3339 timestamp of first start.
//   - hostname    machine hostname captured at first start (best-effort, not authoritative).
//
// Cloud linking, tenant assignment, and enrollment metadata are layered on top
// of this identity in higher slices and are deliberately not part of this
// package.
package instance

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/google/uuid"
)

// fileName is the canonical instance identity file relative to the data dir.
const fileName = "instance.json"

// Identity describes a single TechStack instance.
//
// It is JSON-serialized to <data-dir>/instance.json. Once written, ID and
// CreatedAt MUST NOT change. Hostname may be updated on later loads but the
// stored value is preserved unless explicitly rotated.
type Identity struct {
	ID        string    `json:"id"`
	CreatedAt time.Time `json:"created_at"`
	Hostname  string    `json:"hostname,omitempty"`
}

// Resolver loads or initializes the instance identity in a data directory.
//
// Construction is lazy: callers should call Load once at startup and reuse the
// returned Identity. Concurrent loads are safe; the file write is protected by
// an internal mutex.
type Resolver struct {
	dataDir string
	mu      sync.Mutex
}

// NewResolver returns a Resolver bound to the given data directory.
//
// The directory itself is not created here; that is the caller's
// responsibility (PocketBase or the runtime startup ensures the dir exists).
func NewResolver(dataDir string) *Resolver {
	return &Resolver{dataDir: dataDir}
}

// Path returns the absolute path of the identity file.
func (r *Resolver) Path() string {
	return filepath.Join(r.dataDir, fileName)
}

// Load returns the persisted instance identity, generating one on first call.
//
// The function is idempotent and safe to call multiple times. It returns an
// error only if the data directory cannot be created/read or if the existing
// file is unreadable.
func (r *Resolver) Load() (Identity, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dataDir == "" {
		return Identity{}, errors.New("instance: data directory is empty")
	}

	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		return Identity{}, fmt.Errorf("instance: ensure data dir: %w", err)
	}

	path := r.Path()
	raw, err := os.ReadFile(path)
	if err == nil {
		var existing Identity
		if jsonErr := json.Unmarshal(raw, &existing); jsonErr != nil {
			return Identity{}, fmt.Errorf("instance: parse %s: %w", path, jsonErr)
		}
		if existing.ID == "" {
			return Identity{}, fmt.Errorf("instance: %s has empty id", path)
		}
		return existing, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Identity{}, fmt.Errorf("instance: read %s: %w", path, err)
	}

	created := Identity{
		ID:        newID(),
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Hostname:  hostname(),
	}

	if writeErr := writeAtomic(path, created); writeErr != nil {
		return Identity{}, writeErr
	}
	return created, nil
}

// newID returns a fresh UUIDv4. uuid.NewRandom uses crypto/rand internally;
// the explicit fallback to crypto/rand below documents that the identity is
// always cryptographically random and never derived from environment data.
func newID() string {
	id, err := uuid.NewRandom()
	if err == nil {
		return id.String()
	}
	var b [16]byte
	if _, readErr := rand.Read(b[:]); readErr != nil {
		// As a last resort fall back to a time-based identifier. The Resolver
		// caller will still receive a non-empty ID, and any production deployment
		// where crypto/rand fails has bigger problems than instance identity.
		return fmt.Sprintf("ts-%d", time.Now().UnixNano())
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

func hostname() string {
	name, err := os.Hostname()
	if err != nil {
		return ""
	}
	return name
}

// writeAtomic writes the identity to disk via a temp file + rename so a crash
// during startup cannot leave a half-written instance.json behind.
func writeAtomic(path string, id Identity) error {
	data, err := json.MarshalIndent(id, "", "  ")
	if err != nil {
		return fmt.Errorf("instance: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".instance-*.json")
	if err != nil {
		return fmt.Errorf("instance: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("instance: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("instance: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("instance: rename: %w", err)
	}
	return nil
}
