// Package enrollment models the link between a local TechStack instance and
// the wider kombify ecosystem (notably kombify Cloud).
//
// An enrollment record sits one layer above pkg/instance:
//
//   - pkg/instance answers "which deployment unit am I?".
//   - pkg/enrollment answers "what is this instance enrolled for?".
//
// The two records are persisted side-by-side in the data directory:
//
//	<data-dir>/instance.json     -- stable identity (pkg/instance)
//	<data-dir>/enrollment.json   -- enrollment status and cloud link
//
// On first start an instance is enrolled in mode `standalone`. Cloud-linking,
// tenant assignment, and revocation are higher-level operations that update
// this record; the actual handshake against kombify Cloud is a later slice
// and is intentionally out of scope here.
//
// Pillar 1 (Unifier) consumes this record for the wizard's auth-mode
// selection. See docs/ARCHITECTURE.md and docs/pillars/01-unifier.md.
package enrollment

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// fileName is the canonical enrollment file relative to the data dir.
const fileName = "enrollment.json"

// Mode describes how a TechStack instance is enrolled.
type Mode string

const (
	// ModeStandalone is the default for a fresh local install. The instance is
	// not linked to kombify Cloud and operates entirely on its own identity.
	ModeStandalone Mode = "standalone"

	// ModeCloudLinked indicates the instance is linked to a kombify Cloud
	// tenant. Linking is performed by an explicit operator action; this
	// package only stores the resulting state.
	ModeCloudLinked Mode = "cloud_linked"
)

// Valid reports whether m is a known enrollment mode.
func (m Mode) Valid() bool {
	switch m {
	case ModeStandalone, ModeCloudLinked:
		return true
	default:
		return false
	}
}

// Enrollment is the persisted enrollment state for a single instance.
//
// InstanceID MUST match the pkg/instance identity for the same data directory;
// the resolver verifies this on every load to defend against accidentally
// pointing two instances at the same data dir.
type Enrollment struct {
	InstanceID    string     `json:"instance_id"`
	Mode          Mode       `json:"mode"`
	CloudTenantID string     `json:"cloud_tenant_id,omitempty"`
	LinkedAt      *time.Time `json:"linked_at,omitempty"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Resolver loads or initializes the enrollment record for a given instance.
//
// Load is idempotent and safe to call concurrently. Save serializes writes via
// an internal mutex.
type Resolver struct {
	dataDir    string
	instanceID string
	mu         sync.Mutex
}

// NewResolver returns a Resolver bound to a data directory and an instance ID.
//
// The data directory is expected to exist; it is the caller's responsibility
// (PocketBase / runtime startup) to create it. The instance ID must be
// non-empty -- enrollment without a stable instance identity is meaningless.
func NewResolver(dataDir, instanceID string) *Resolver {
	return &Resolver{dataDir: dataDir, instanceID: instanceID}
}

// Path returns the absolute path of the enrollment file.
func (r *Resolver) Path() string {
	return filepath.Join(r.dataDir, fileName)
}

// Load returns the persisted enrollment record, generating a default
// `standalone` record on first call.
//
// Load returns an error if the data directory is empty, the file exists but is
// unreadable, or the persisted instance_id does not match the resolver's
// expected instance ID.
func (r *Resolver) Load() (Enrollment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dataDir == "" {
		return Enrollment{}, errors.New("enrollment: data directory is empty")
	}
	if r.instanceID == "" {
		return Enrollment{}, errors.New("enrollment: instance id is empty")
	}

	if err := os.MkdirAll(r.dataDir, 0o755); err != nil {
		return Enrollment{}, fmt.Errorf("enrollment: ensure data dir: %w", err)
	}

	path := r.Path()
	raw, err := os.ReadFile(path)
	if err == nil {
		var existing Enrollment
		if jsonErr := json.Unmarshal(raw, &existing); jsonErr != nil {
			return Enrollment{}, fmt.Errorf("enrollment: parse %s: %w", path, jsonErr)
		}
		if existing.InstanceID == "" {
			return Enrollment{}, fmt.Errorf("enrollment: %s has empty instance_id", path)
		}
		if existing.InstanceID != r.instanceID {
			return Enrollment{}, fmt.Errorf(
				"enrollment: %s belongs to instance %q but current instance is %q",
				path, existing.InstanceID, r.instanceID,
			)
		}
		if !existing.Mode.Valid() {
			return Enrollment{}, fmt.Errorf("enrollment: %s has invalid mode %q", path, existing.Mode)
		}
		return existing, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return Enrollment{}, fmt.Errorf("enrollment: read %s: %w", path, err)
	}

	created := Enrollment{
		InstanceID: r.instanceID,
		Mode:       ModeStandalone,
		UpdatedAt:  time.Now().UTC().Truncate(time.Second),
	}
	if writeErr := writeAtomic(path, created); writeErr != nil {
		return Enrollment{}, writeErr
	}
	return created, nil
}

// Save persists the given enrollment record. The InstanceID field is
// overwritten with the resolver's expected instance ID and UpdatedAt is set to
// now (UTC, second precision). Mode must be a known value.
//
// Save is intentionally narrow: cloud-linking flows that need additional
// guarantees (atomic compare-and-swap, audit log, etc.) build on top.
func (r *Resolver) Save(e Enrollment) (Enrollment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if r.dataDir == "" {
		return Enrollment{}, errors.New("enrollment: data directory is empty")
	}
	if r.instanceID == "" {
		return Enrollment{}, errors.New("enrollment: instance id is empty")
	}
	if !e.Mode.Valid() {
		return Enrollment{}, fmt.Errorf("enrollment: invalid mode %q", e.Mode)
	}
	if e.Mode == ModeCloudLinked && e.CloudTenantID == "" {
		return Enrollment{}, errors.New("enrollment: cloud_linked requires cloud_tenant_id")
	}

	e.InstanceID = r.instanceID
	e.UpdatedAt = time.Now().UTC().Truncate(time.Second)

	if err := writeAtomic(r.Path(), e); err != nil {
		return Enrollment{}, err
	}
	return e, nil
}

// writeAtomic writes the enrollment record via a temp file + rename.
func writeAtomic(path string, e Enrollment) error {
	data, err := json.MarshalIndent(e, "", "  ")
	if err != nil {
		return fmt.Errorf("enrollment: marshal: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".enrollment-*.json")
	if err != nil {
		return fmt.Errorf("enrollment: create temp: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("enrollment: write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("enrollment: close temp: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("enrollment: rename: %w", err)
	}
	return nil
}
