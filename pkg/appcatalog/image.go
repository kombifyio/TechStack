package appcatalog

import (
	"regexp"
	"strconv"
	"strings"
)

// Reason codes explaining why a version or update fact is not known. They are
// the `version.reason_code` values of the service read model.
const (
	ReasonFloatingTag         = "floating_tag"
	ReasonUnparsedTag         = "unparsed_tag"
	ReasonDigestPinned        = "digest_pinned"
	ReasonRegistryUnreachable = "registry_unreachable"
	ReasonRegistryDenied      = "registry_denied"
	ReasonRegistryUnsupported = "registry_unsupported"
	ReasonNoComparableTags    = "no_comparable_tags"
	ReasonPending             = "pending"
)

// Image is a parsed container image reference.
type Image struct {
	// Registry is the normalized registry host; Docker Hub is "docker.io".
	Registry string
	// Repo is the repository path; Docker Hub official images carry "library/".
	Repo   string
	Tag    string
	Digest string
}

var imageIDPattern = regexp.MustCompile(`^(sha256:)?[0-9a-f]{12,64}$`)

// ParseImage splits an image reference. A bare image ID (docker reports one
// when an image lost its tag) yields an empty Repo: nothing is claimed.
func ParseImage(reference string) Image {
	reference = strings.TrimSpace(reference)
	if reference == "" || imageIDPattern.MatchString(reference) {
		return Image{}
	}
	var image Image
	if before, digest, found := strings.Cut(reference, "@"); found {
		reference, image.Digest = before, digest
	}
	if slash := strings.LastIndex(reference, "/"); strings.LastIndex(reference, ":") > slash {
		colon := strings.LastIndex(reference, ":")
		reference, image.Tag = reference[:colon], reference[colon+1:]
	}
	registry, path := "docker.io", reference
	if first, rest, found := strings.Cut(reference, "/"); found &&
		(strings.ContainsAny(first, ".:") || first == "localhost") {
		registry, path = strings.ToLower(first), rest
	}
	switch registry {
	case "index.docker.io", "registry-1.docker.io":
		registry = "docker.io"
	}
	if registry == "docker.io" && !strings.Contains(path, "/") {
		path = "library/" + path
	}
	image.Registry, image.Repo = registry, strings.ToLower(path)
	return image
}

var floatingTags = map[string]bool{
	"latest": true, "stable": true, "main": true, "master": true, "edge": true, "nightly": true,
	"dev": true, "develop": true, "development": true, "release": true, "rolling": true,
	"beta": true, "alpha": true, "testing": true, "unstable": true, "canary": true, "current": true,
}

var versionPattern = regexp.MustCompile(`^v?(\d+(?:\.\d+){0,3})(.*)$`)

// version is a tag split into its numeric core and a variant suffix such as
// "-alpine". Only tags with the same suffix are comparable.
type version struct {
	core   []int
	suffix string
}

func parseVersion(tag string) (version, bool) {
	match := versionPattern.FindStringSubmatch(strings.TrimSpace(tag))
	if match == nil {
		return version{}, false
	}
	suffix := match[2]
	if suffix != "" && !strings.ContainsAny(suffix[:1], "-_.+") {
		// "1.2rc1" style: treat the remainder as a variant, never as the core.
		suffix = "-" + suffix
	}
	var core []int
	for _, part := range strings.Split(match[1], ".") {
		number, err := strconv.Atoi(part)
		if err != nil || number < 0 {
			return version{}, false
		}
		core = append(core, number)
	}
	return version{core: core, suffix: strings.ToLower(suffix)}, true
}

// compare returns the ordering of two versions of equal core length.
func (v version) compare(other version) int {
	for index := range v.core {
		switch {
		case v.core[index] < other.core[index]:
			return -1
		case v.core[index] > other.core[index]:
			return 1
		}
	}
	return 0
}

// comparable reports whether a candidate is a fair update candidate for v:
// same variant suffix and the same precision, so the floating alias "1.25" is
// never reported as outdated by "1.25.3".
func (v version) comparable(candidate version) bool {
	if candidate.suffix != v.suffix {
		return false
	}
	// A date-like leading component ("2021.12.16") is a different versioning
	// scheme from a release number, not a major version above it.
	if candidate.core[0] >= 1900 && v.core[0] < 1900 {
		return false
	}
	if len(v.core) >= 3 {
		return len(candidate.core) >= 3 && len(candidate.core) == len(v.core)
	}
	return len(candidate.core) == len(v.core)
}

// CurrentVersion derives the running version of an image without inventing
// one. A full release tag wins over an ambiguous OCI label inherited from a
// base image. Moving aliases can use the measured label. The reason is set
// exactly when no version could be derived.
func CurrentVersion(reference, label string) (current, source, reason string) {
	image := ParseImage(reference)
	if tagged, ok := parseVersion(image.Tag); image.Repo != "" && ok && len(tagged.core) >= 3 {
		return strings.TrimPrefix(image.Tag, "v"), "image_tag", ""
	}
	if label = strings.TrimSpace(label); label != "" {
		if _, ok := parseVersion(label); ok {
			return strings.TrimPrefix(label, "v"), "image_label", ""
		}
	}
	switch {
	case image.Repo == "":
		return "", "", ""
	case image.Tag == "" && image.Digest != "":
		return "", "", ReasonDigestPinned
	case image.Tag == "" || floatingTags[strings.ToLower(image.Tag)]:
		return "", "", ReasonFloatingTag
	}
	if _, ok := parseVersion(image.Tag); !ok {
		return "", "", ReasonUnparsedTag
	}
	return strings.TrimPrefix(image.Tag, "v"), "image_tag", ""
}

// SelectLatest picks the highest comparable stable tag for the running
// version. newer is true only when that tag is strictly greater than current;
// latest equals current when the running version is already the newest, and ok
// is false when no comparable tag exists at all.
func SelectLatest(current string, tags []string) (latest string, newer, ok bool) {
	running, parsed := parseVersion(current)
	if !parsed {
		return "", false, false
	}
	var best version
	bestTag := ""
	for _, tag := range tags {
		candidate, valid := parseVersion(tag)
		if !valid || !running.comparable(candidate) {
			continue
		}
		if bestTag == "" || candidate.compare(best) > 0 {
			best, bestTag = candidate, tag
		}
	}
	if bestTag == "" {
		return "", false, false
	}
	if best.compare(running) > 0 {
		return strings.TrimPrefix(bestTag, "v"), true, true
	}
	return strings.TrimPrefix(current, "v"), false, true
}

// CompareVersions orders two plain versions ("5.1.0" vs "v5.2.0"). ok is false
// when either does not parse; missing trailing components count as zero.
func CompareVersions(a, b string) (result int, ok bool) {
	left, leftOK := parseVersion(a)
	right, rightOK := parseVersion(b)
	if !leftOK || !rightOK {
		return 0, false
	}
	for len(left.core) < len(right.core) {
		left.core = append(left.core, 0)
	}
	for len(right.core) < len(left.core) {
		right.core = append(right.core, 0)
	}
	return left.compare(right), true
}
