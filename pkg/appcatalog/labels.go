package appcatalog

import (
	"net/url"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Recognition labels are the container metadata layer 1 reads. Only these
// keys leave the host: the agent reports the allow-listed subset, never the
// whole label set, which can carry environment details or secrets.
const (
	labelOCITitle    = "org.opencontainers.image.title"
	labelOCISource   = "org.opencontainers.image.source"
	labelOCIURL      = "org.opencontainers.image.url"
	labelOCIVendor   = "org.opencontainers.image.vendor"
	labelHomepageKey = "homepage."
	labelGlanceKey   = "glance."
	labelTraefikKey  = "traefik."

	maxRecognitionLabels     = 32
	maxRecognitionLabelKey   = 128
	maxRecognitionLabelValue = 512
)

var recognitionLabelKeys = map[string]bool{
	labelOCITitle: true, labelOCISource: true, labelOCIURL: true, labelOCIVendor: true,
	"homepage.name": true, "homepage.icon": true, "homepage.href": true, "homepage.group": true,
	"glance.name": true, "glance.icon": true, "glance.url": true,
}

// traefikRecognitionLabel matches the router and service keys that say where
// a container's web UI is: its host rule, TLS and entrypoints, and the port
// and scheme the load balancer forwards to.
var traefikRecognitionLabel = regexp.MustCompile(
	`^traefik\.http\.(routers\.[A-Za-z0-9_.-]+\.(rule|tls|entrypoints)|services\.[A-Za-z0-9_.-]+\.loadbalancer\.server\.(port|scheme))$`)

// IsRecognitionLabel reports whether a container label key is one the
// recognition layer reads.
func IsRecognitionLabel(key string) bool {
	if recognitionLabelKeys[key] {
		return true
	}
	return strings.HasPrefix(key, labelTraefikKey) && traefikRecognitionLabel.MatchString(key)
}

// RecognitionLabels keeps only the allow-listed labels, bounded in count and
// size. It returns nil when none remain.
func RecognitionLabels(labels map[string]string) map[string]string {
	keys := make([]string, 0, len(labels))
	for key, value := range labels {
		key = strings.TrimSpace(key)
		if len(key) > maxRecognitionLabelKey || len(strings.TrimSpace(value)) == 0 ||
			len(value) > maxRecognitionLabelValue || !IsRecognitionLabel(key) {
			continue
		}
		keys = append(keys, key)
	}
	if len(keys) == 0 {
		return nil
	}
	sort.Strings(keys)
	if len(keys) > maxRecognitionLabels {
		keys = keys[:maxRecognitionLabels]
	}
	kept := make(map[string]string, len(keys))
	for _, key := range keys {
		kept[key] = strings.TrimSpace(labels[key])
	}
	return kept
}

// baseImageTitles are OCI titles a container inherits from its base image.
// They name the operating system or language runtime, never the application.
var baseImageTitles = map[string]bool{
	"alpine": true, "almalinux": true, "busybox": true, "centos": true, "debian": true,
	"distroless": true, "docker-image": true, "dotnet": true, "eclipse-temurin": true,
	"fedora": true, "golang": true, "go": true, "java": true, "nginx": true, "node": true,
	"nodejs": true, "openjdk": true, "php": true, "python": true, "red-hat-universal-base-image": true,
	"rockylinux": true, "ruby": true, "rust": true, "ubi": true, "ubuntu": true,
}

// labelRecognition resolves layer 1: the container's own metadata. An icon or
// name label the operator set (gethomepage, Glance) names the application
// directly; the image's OCI title and source repository name it as published.
// A label that names an unknown application or a base image claims nothing.
func labelRecognition(labels map[string]string) *Entry {
	if len(labels) == 0 {
		return nil
	}
	for _, key := range []string{"homepage.icon", "glance.icon"} {
		if entry := lookupKnown(iconLabelSlug(labels[key]), true); entry != nil {
			return entry
		}
	}
	for _, key := range []string{"homepage.name", "glance.name", labelOCITitle} {
		slug := slugify(labels[key])
		if baseImageTitles[slug] {
			continue
		}
		if entry := lookupKnown(slug, key != labelOCITitle); entry != nil {
			return entry
		}
	}
	for _, key := range []string{labelOCISource, labelOCIURL} {
		if entry := generatedBySourceRepo(sourceRepo(labels[key])); entry != nil {
			return entry
		}
	}
	return nil
}

// LabelDisplayName is the name the operator gave an unrecognized container in
// its dashboard labels, or its image's own title. Empty when neither exists.
func LabelDisplayName(labels map[string]string) string {
	for _, key := range []string{"homepage.name", "glance.name", labelOCITitle} {
		if name := strings.TrimSpace(labels[key]); name != "" && len(name) <= 64 && !baseImageTitles[slugify(name)] {
			return name
		}
	}
	return ""
}

// iconLabelSlug reads a gethomepage or Glance icon reference: "sonarr.png",
// "sh-sonarr", "di:sonarr", "si:sonarr" or a URL to a dashboard-icons file.
// Material Design and other generic icon sets name no application.
func iconLabelSlug(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" || strings.HasPrefix(value, "mdi-") || strings.HasPrefix(value, "mdi:") {
		return ""
	}
	if parsed, err := url.Parse(value); err == nil && parsed.Host != "" {
		value = path.Base(parsed.Path)
	}
	for _, prefix := range []string{"di:", "si:", "sh:", "sh-", "si-"} {
		value = strings.TrimPrefix(value, prefix)
	}
	value = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(value, ".png"), ".svg"), ".webp")
	value = strings.TrimSuffix(strings.TrimSuffix(value, "-light"), "-dark")
	return slugify(value)
}

var githubRepoPath = regexp.MustCompile(`^/([a-z0-9_.-]+)/([a-z0-9_.-]+)`)

// sourceRepo extracts "owner/repo" from a GitHub source URL. LinuxServer
// packages its images from "docker-<app>" repositories, which name the
// upstream application, not a different project.
func sourceRepo(value string) string {
	parsed, err := url.Parse(strings.ToLower(strings.TrimSpace(value)))
	if err != nil || (parsed.Host != "github.com" && parsed.Host != "www.github.com") {
		return ""
	}
	match := githubRepoPath.FindStringSubmatch(parsed.Path)
	if match == nil {
		return ""
	}
	return match[1] + "/" + strings.TrimSuffix(match[2], ".git")
}

// LabelWebPort is the container port a Traefik service label forwards to and
// the scheme it uses there. ok is false without such a label or when two
// services disagree.
func LabelWebPort(labels map[string]string) (port int, scheme string, ok bool) {
	for key, value := range labels {
		if !strings.HasPrefix(key, "traefik.http.services.") || !strings.HasSuffix(key, ".loadbalancer.server.port") {
			continue
		}
		parsed, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || parsed < 1 || parsed > 65535 || (port != 0 && port != parsed) {
			return 0, "", false
		}
		port = parsed
		prefix := strings.TrimSuffix(key, ".port")
		scheme = strings.ToLower(strings.TrimSpace(labels[prefix+".scheme"]))
	}
	if scheme != "https" {
		scheme = "http"
	}
	return port, scheme, port != 0
}

var slugUnsafe = regexp.MustCompile(`[^a-z0-9]+`)

// slugify turns a display name or file name into a catalog id candidate.
func slugify(value string) string {
	return strings.Trim(slugUnsafe.ReplaceAllString(strings.ToLower(strings.TrimSpace(value)), "-"), "-")
}
