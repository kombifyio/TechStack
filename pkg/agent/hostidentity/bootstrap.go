package hostidentity

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/privatefs"
)

// Bootstrap is the one-time pairing capability persisted before redemption so
// an interrupted first boot resumes instead of losing it. It is deleted as
// soon as the binding is confirmed.
type Bootstrap struct {
	Schema   string         `json:"schema"`
	Server   string         `json:"server"`
	Token    string         `json:"token"`
	Hostname string         `json:"hostname,omitempty"`
	OS       string         `json:"os,omitempty"`
	Arch     string         `json:"arch,omitempty"`
	Extra    map[string]any `json:"extra,omitempty"`
	// LANPin is the private-LAN bridge key pin from the install command. A
	// resumed enrollment pins the same key again.
	LANPin string `json:"lan_pin,omitempty"`
}

const bootstrapSchema = "kombify.guard.bootstrap/v1"

// SaveBootstrap persists the capability with owner-only permissions.
func (m *Manager) SaveBootstrap(b Bootstrap) error {
	b.Schema = bootstrapSchema
	data, err := json.Marshal(b)
	if err != nil {
		return err
	}
	return privatefs.Replace(m.root, bootstrapFileName, data, 0o600)
}

// LoadBootstrap returns the persisted capability, or nil when there is none.
func (m *Manager) LoadBootstrap() (*Bootstrap, error) {
	data, err := privatefs.ReadFile(m.root, bootstrapFileName)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if len(data) == 0 {
		return nil, nil
	}
	var b Bootstrap
	if json.Unmarshal(data, &b) != nil || b.Schema != bootstrapSchema || strings.TrimSpace(b.Token) == "" || strings.TrimSpace(b.Server) == "" {
		return nil, errors.New("kombify Guard bootstrap capability file is unreadable")
	}
	return &b, nil
}

// CanResume reports whether an enrollment interrupted mid-redemption holds
// exactly this capability for this control plane: the local state is redeeming
// with an attempt, and the persisted capability is the one offered. Redeeming
// it again is a resume (same attempt, same host key), which the control plane
// answers even though it already claimed the capability. Any other capability
// starts a new attempt, and this one never matches it.
func (m *Manager) CanResume(server, token string) bool {
	record := m.Record()
	if record.State != StateRedeeming || record.AttemptID == "" {
		return false
	}
	persisted, err := m.LoadBootstrap()
	if err != nil || persisted == nil {
		return false
	}
	sameServer := strings.TrimRight(strings.TrimSpace(persisted.Server), "/") == strings.TrimRight(strings.TrimSpace(server), "/")
	sameToken := subtle.ConstantTimeCompare([]byte(strings.TrimSpace(persisted.Token)), []byte(strings.TrimSpace(token))) == 1
	return sameServer && sameToken
}

// RemoveBootstrap deletes the persisted capability.
func (m *Manager) RemoveBootstrap() error { return wipeRootedSecretFile(m.root, bootstrapFileName) }

// discardBootstrapCapability clears only the inode holding this capability.
// SaveBootstrap atomically replaces its path: a concurrent enrollment can
// therefore publish a replacement without an older request erasing it. Leave
// an empty file rather than unlinking a potentially replaced pathname.
func (m *Manager) discardBootstrapCapability(expected Bootstrap) error {
	before, err := m.root.Lstat(bootstrapFileName)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !before.Mode().IsRegular() {
		return errors.New("bootstrap capability is not a regular file")
	}
	file, err := privatefs.OpenFile(m.root, bootstrapFileName, os.O_RDWR, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(before, opened) {
		return nil // A replacement won before this handle was opened.
	}
	data, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil || len(data) == 0 {
		return err
	}
	var current Bootstrap
	if err := json.Unmarshal(data, &current); err != nil {
		return err
	}
	if strings.TrimRight(strings.TrimSpace(current.Server), "/") != strings.TrimRight(strings.TrimSpace(expected.Server), "/") ||
		subtle.ConstantTimeCompare([]byte(current.Token), []byte(expected.Token)) != 1 {
		return nil
	}
	if _, err := file.WriteAt(make([]byte, len(data)), 0); err != nil {
		return err
	}
	if err := file.Truncate(0); err != nil {
		return err
	}
	return file.Sync()
}

// CloudInitResiduePaths are the places cloud-init keeps the rendered first-boot
// command, which contains the pairing capability.
var CloudInitResiduePaths = []string{
	"/var/lib/cloud/instance/scripts/runcmd",
	// The first-boot script that carries the capability; it removes itself when
	// its run ends, so this only matters after an interrupted run.
	"/usr/local/lib/kombify/guard-bootstrap",
	"/var/lib/cloud/instance/user-data.txt",
	"/var/lib/cloud/instance/user-data.txt.i",
	"/var/lib/cloud/instance/cloud-config.txt",
	"/var/lib/cloud/instance/cloud-config.txt.i",
}

// RedactBootstrapResidue replaces the consumed pairing capability inside files
// that cloud-init wrote, each by atomic replacement so a shell still executing
// the old file keeps reading its own inode. Files without the capability and
// missing files are left alone. It returns how many files were rewritten.
func RedactBootstrapResidue(token string, paths []string) int {
	token = strings.TrimSpace(token)
	if len(token) < 16 {
		return 0
	}
	redacted := 0
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 4<<20 {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil || !bytes.Contains(data, []byte(token)) {
			continue
		}
		replaced := bytes.ReplaceAll(data, []byte(token), []byte("[redacted]"))
		if WriteFileAtomic(path, replaced, info.Mode().Perm()) == nil {
			redacted++
		}
	}
	return redacted
}
