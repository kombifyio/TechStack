package nodeproof

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
)

const ProfileAuthorityFile = "ril-applied-profile.json"

var profileDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

type profileEvidence struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}
type profileAuthority struct {
	Schema     string            `json:"schema"`
	Profile    string            `json:"profile"`
	Workspace  string            `json:"workspace"`
	PlanHash   string            `json:"planHash"`
	ResultHash string            `json:"resultHash"`
	ExpiresAt  string            `json:"expiresAt,omitempty"`
	Evidence   []profileEvidence `json:"evidence"`
}

// openTrustedDirectory validates every ancestor through descriptor-relative,
// non-symlink traversal. It does not create an authority or workspace.
func openTrustedDirectory(name string) (*os.Root, error) {
	if !filepath.IsAbs(name) || filepath.Clean(name) != name {
		return nil, errors.New("invalid RIL directory")
	}
	root, err := os.OpenRoot(string(filepath.Separator))
	if err != nil {
		return nil, err
	}
	if err = privatefs.CheckRoot(root); err != nil {
		root.Close()
		return nil, err
	}
	for _, part := range strings.Split(strings.TrimPrefix(name, string(filepath.Separator)), string(filepath.Separator)) {
		if part == "" {
			continue
		}
		before, e := root.Lstat(part)
		if e != nil || !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			root.Close()
			return nil, errors.New("unsafe RIL directory")
		}
		next, e := root.OpenRoot(part)
		if e != nil {
			root.Close()
			return nil, e
		}
		after, e := next.Stat(".")
		if e != nil || !os.SameFile(before, after) {
			next.Close()
			root.Close()
			return nil, errors.New("RIL directory changed")
		}
		if e = privatefs.CheckRoot(next); e != nil {
			next.Close()
			root.Close()
			return nil, e
		}
		root.Close()
		root = next
	}
	return root, nil
}

func admitHostedProfile(dir string) error {
	if os.Geteuid() != 0 {
		return errors.New("RIL applied hosted profile requires root")
	}
	root, err := openTrustedDirectory(dir)
	if err != nil {
		return err
	}
	raw, err := privatefs.ReadFile(root, ProfileAuthorityFile)
	root.Close()
	if err != nil {
		return err
	}
	var profile profileAuthority
	if strict(raw, &profile) != nil || profile.Schema != "kombify.ril-applied-profile.v2" || profile.Profile != "standard" || !profileDigest.MatchString(profile.PlanHash) || !profileDigest.MatchString(profile.ResultHash) || (len(profile.Evidence) != 7 && len(profile.Evidence) != 8) || !filepath.IsAbs(profile.Workspace) || filepath.Clean(profile.Workspace) != profile.Workspace {
		return errors.New("RIL applied hosted profile unavailable")
	}
	if len(profile.Evidence) == 8 {
		expiry, err := time.Parse(time.RFC3339Nano, profile.ExpiresAt)
		if err != nil || !time.Now().Before(expiry) {
			return errors.New("RIL member authority expired")
		}
	} else if profile.ExpiresAt != "" {
		return errors.New("invalid RIL authority expiry")
	}
	seen := make(map[string]bool)
	for _, e := range profile.Evidence {
		rel, err := filepath.Rel(profile.Workspace, e.Path)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) || !filepath.IsAbs(e.Path) || filepath.Clean(e.Path) != e.Path || seen[e.Path] || !profileDigest.MatchString(e.Digest) {
			return errors.New("invalid RIL evidence binding")
		}
		seen[e.Path] = true
		parent, err := openTrustedDirectory(filepath.Dir(e.Path))
		if err != nil {
			return err
		}
		data, err := privatefs.ReadFile(parent, filepath.Base(e.Path))
		parent.Close()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		if "sha256:"+hex.EncodeToString(sum[:]) != e.Digest {
			return errors.New("RIL applied evidence changed")
		}
	}
	return nil
}
