package stackkitcommand

import (
	"regexp"
	"strconv"
	"strings"
)

var releaseVersionPattern = regexp.MustCompile(`^v([0-9]+)\.([0-9]+)\.([0-9]+)(?:-(?:beta|edge)\.[0-9]+)?$`)

// ReleaseAtLeast compares release versions by their numeric core; a
// beta or edge pre-release of the minimum does not qualify.
func ReleaseAtLeast(version, minimum string) bool {
	have, haveOK := parseReleaseVersion(version)
	want, wantOK := parseReleaseVersion(minimum)
	if !haveOK || !wantOK {
		return false
	}
	for i := range have {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return !strings.Contains(strings.TrimSpace(version), "-")
}

func parseReleaseVersion(version string) ([3]int, bool) {
	match := releaseVersionPattern.FindStringSubmatch(strings.TrimSpace(version))
	if match == nil {
		return [3]int{}, false
	}
	var parts [3]int
	for i := range parts {
		value, err := strconv.Atoi(match[i+1])
		if err != nil {
			return [3]int{}, false
		}
		parts[i] = value
	}
	return parts, true
}
