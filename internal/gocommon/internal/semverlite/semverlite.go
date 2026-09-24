// Package semverlite implements the minimal SemVer 2.0.0 parsing and
// precedence comparison the native-client packages need (update monotonicity,
// staleness guards, data-directory downgrade refusal). It intentionally avoids
// an external dependency; build metadata is parsed but ignored for precedence,
// exactly as SemVer specifies.
package semverlite

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

// ErrInvalidVersion is returned when a version string is not valid SemVer.
var ErrInvalidVersion = errors.New("semverlite: invalid version")

var versionPattern = regexp.MustCompile(
	`^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)(?:-([0-9A-Za-z.-]+))?(?:\+([0-9A-Za-z.-]+))?$`,
)

// Version is a parsed SemVer version.
type Version struct {
	Major, Minor, Patch uint64
	Prerelease          string
	Build               string
}

// Parse parses a strict SemVer string (no leading "v").
func Parse(raw string) (Version, error) {
	m := versionPattern.FindStringSubmatch(strings.TrimSpace(raw))
	if m == nil {
		return Version{}, fmt.Errorf("%w: %q", ErrInvalidVersion, raw)
	}
	major, _ := strconv.ParseUint(m[1], 10, 64)
	minor, _ := strconv.ParseUint(m[2], 10, 64)
	patch, _ := strconv.ParseUint(m[3], 10, 64)
	return Version{Major: major, Minor: minor, Patch: patch, Prerelease: m[4], Build: m[5]}, nil
}

// Compare returns -1, 0, or 1 when a is lower than, equal to, or higher than b
// by SemVer precedence. Build metadata is ignored.
func Compare(a, b Version) int {
	if c := compareUint(a.Major, b.Major); c != 0 {
		return c
	}
	if c := compareUint(a.Minor, b.Minor); c != 0 {
		return c
	}
	if c := compareUint(a.Patch, b.Patch); c != 0 {
		return c
	}
	return comparePrerelease(a.Prerelease, b.Prerelease)
}

// CompareStrings parses both inputs and compares them.
func CompareStrings(a, b string) (int, error) {
	av, err := Parse(a)
	if err != nil {
		return 0, err
	}
	bv, err := Parse(b)
	if err != nil {
		return 0, err
	}
	return Compare(av, bv), nil
}

func compareUint(a, b uint64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	default:
		return 0
	}
}

// comparePrerelease implements SemVer 2.0.0 section 11: a version without a
// prerelease outranks one with a prerelease; identifiers compare numerically
// when both are numeric, lexically otherwise, and numeric ranks below
// alphanumeric.
func comparePrerelease(a, b string) int {
	if a == "" && b == "" {
		return 0
	}
	if a == "" {
		return 1
	}
	if b == "" {
		return -1
	}
	as := strings.Split(a, ".")
	bs := strings.Split(b, ".")
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareIdentifier(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return compareUint(uint64(len(as)), uint64(len(bs)))
}

func compareIdentifier(a, b string) int {
	an, aNum := strconv.ParseUint(a, 10, 64)
	bn, bNum := strconv.ParseUint(b, 10, 64)
	aIsNum := aNum == nil
	bIsNum := bNum == nil
	switch {
	case aIsNum && bIsNum:
		return compareUint(an, bn)
	case aIsNum:
		return -1
	case bIsNum:
		return 1
	default:
		return strings.Compare(a, b)
	}
}
