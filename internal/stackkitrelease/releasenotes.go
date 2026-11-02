package stackkitrelease

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// pinned-release-notes.json is rendered by scripts/stackkits-release-notes.mjs
// from the CHANGELOG.md of the pinned StackKits release whenever the repin
// moves the pin; Techstack proposes updates from it.
//
//go:embed pinned-release-notes.json
var pinnedReleaseNotes []byte

const releaseNotesSchema = "techstack.stackkits-release-notes/v1"

// Release note kinds. ReleaseNoteSecurity is the security marker: its scope
// names the affected app.
const (
	ReleaseNoteSecurity = "security"
	ReleaseNoteAdded    = "added"
	ReleaseNoteFixed    = "fixed"
	ReleaseNoteChanged  = "changed"
	ReleaseNoteReverted = "reverted"
)

// ReleaseNotes are the StackKits release notes up to the pinned release.
type ReleaseNotes struct {
	SchemaVersion string `json:"schemaVersion"`
	Source        struct {
		Version      string `json:"version"`
		Changelog    string `json:"changelog"`
		SourceCommit string `json:"sourceCommit"`
	} `json:"source"`
	ChangelogURL string        `json:"changelogUrl"`
	Releases     []ReleaseNote `json:"releases"`
}

// ReleaseNote is one released `## <version>` section of CHANGELOG.md.
type ReleaseNote struct {
	Version string             `json:"version"`
	Date    string             `json:"date,omitempty"`
	Entries []ReleaseNoteEntry `json:"entries"`
}

// ReleaseNoteEntry is one changelog line. Scope is the use-case slug, add-on
// or app the change touches; empty when the entry names none.
type ReleaseNoteEntry struct {
	Kind  string `json:"kind"`
	Scope string `json:"scope,omitempty"`
	Text  string `json:"text"`
}

var (
	releaseNotesOnce sync.Once
	releaseNotes     ReleaseNotes
	releaseNotesErr  error
)

// PinnedReleaseNotes returns the release notes embedded with the pin.
func PinnedReleaseNotes() (ReleaseNotes, error) {
	releaseNotesOnce.Do(func() {
		releaseNotes, releaseNotesErr = parseReleaseNotes(pinnedReleaseNotes)
	})
	return releaseNotes, releaseNotesErr
}

func parseReleaseNotes(raw []byte) (ReleaseNotes, error) {
	var notes ReleaseNotes
	if err := json.Unmarshal(raw, &notes); err != nil {
		return ReleaseNotes{}, fmt.Errorf("decode pinned StackKits release notes: %w", err)
	}
	if notes.SchemaVersion != releaseNotesSchema || !strings.HasPrefix(notes.ChangelogURL, "https://") {
		return ReleaseNotes{}, fmt.Errorf("pinned StackKits release notes have an unknown schema")
	}
	for _, release := range notes.Releases {
		if _, ok := ParseReleaseVersion(release.Version); !ok {
			return ReleaseNotes{}, fmt.Errorf("pinned StackKits release notes name an inexact release %q", release.Version)
		}
	}
	return notes, nil
}

// Between returns the releases after installed up to and including target,
// newest first: everything a node gains by moving from installed to target.
// An unknown installed release yields only the target's own notes.
func (notes ReleaseNotes) Between(installed, target string) []ReleaseNote {
	from, fromOK := ParseReleaseVersion(installed)
	to, toOK := ParseReleaseVersion(target)
	if !toOK {
		return nil
	}
	var out []ReleaseNote
	for _, release := range notes.Releases {
		version, _ := ParseReleaseVersion(release.Version)
		if CompareReleaseVersions(version, to) <= 0 && (fromOK && CompareReleaseVersions(version, from) > 0 || !fromOK && CompareReleaseVersions(version, to) == 0) {
			out = append(out, release)
		}
	}
	return out
}

// ParseReleaseVersion reads an exact `[v]X.Y.Z` release.
func ParseReleaseVersion(version string) ([3]int, bool) {
	var out [3]int
	parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(version), "v"), ".")
	if len(parts) != 3 {
		return out, false
	}
	for i, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil || value < 0 {
			return out, false
		}
		out[i] = value
	}
	return out, true
}

// CompareReleaseVersions orders two parsed releases.
func CompareReleaseVersions(a, b [3]int) int {
	for i := range a {
		if a[i] != b[i] {
			return a[i] - b[i]
		}
	}
	return 0
}
