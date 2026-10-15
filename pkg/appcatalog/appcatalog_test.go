package appcatalog

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestGroupFollowsTheApplicationRule(t *testing.T) {
	for name, tc := range map[string]struct {
		subject Subject
		key     string
	}{
		"compose project wins over a catalog match":    {Subject{Name: "stack-hermes-webui-1", Image: "ghcr.io/x/hermes-webui:1", ComposeProject: "MyStack"}, "mystack"},
		"compose project named after its app joins it": {Subject{Name: "hermes-webui", Image: "ghcr.io/nesquena/hermes-webui:latest", ComposeProject: "hermes-webui"}, "hermes"},
		"image recognition":                            {Subject{Name: "web", Image: "ghcr.io/nesquena/hermes-webui:v1.2"}, "hermes"},
		"container name recognition":                   {Subject{Name: "hermes-webui", Platform: "docker"}, "hermes"},
		"systemd unit recognition":                     {Subject{Name: "ollama.service", Platform: "systemd"}, "ollama"},
		"unknown container is its own application":     {Subject{Name: "/My Notes", Image: "someone/notes:latest"}, "my-notes"},
		"infra container stays in system":              {Subject{Name: "proxy", Image: "traefik:v3"}, SystemKey},
		"unknown systemd unit stays in system":         {Subject{Name: "custom-backup.service", Platform: "systemd"}, SystemKey},
		"database container is an application":         {Subject{Name: "pg", Image: "postgres:16"}, "postgres"},
		// A StackKit platform project bundles the whole kit; its name is never
		// an application, so each of its services stands for itself.
		"platform project app is its catalog app": {Subject{Name: "stackkit-cloud-core-pocket-id-1", Image: "ghcr.io/pocket-id/pocket-id:v1", ComposeProject: "stackkit-cloud-core", ComposeService: "pocket-id"}, "pocket-id"},
		"platform project infra stays in system":  {Subject{Name: "stackkit-cloud-core-traefik-1", Image: "traefik:v3", ComposeProject: "stackkit-cloud-core", ComposeService: "traefik"}, SystemKey},
		"unknown platform service is its own app": {Subject{Name: "stackkit-cloud-core-notes-1", Image: "someone/notes:1", ComposeProject: "stackkit-cloud-core", ComposeService: "notes"}, "notes"},
	} {
		if got := Group(tc.subject).Key; got != tc.key {
			t.Errorf("%s: key = %q, want %q", name, got, tc.key)
		}
	}
}

// An application image can inherit the distro's version label. That label
// cannot replace its full release tag, while a moving alias still needs the
// measured application label to report a precise runtime version.
func TestCurrentVersionPreservesApplicationReleaseProvenance(t *testing.T) {
	for _, fixture := range []struct{ image, label, current, source string }{
		{"docker.io/kopia/kopia:0.18.2@sha256:b6cb1f09a5fa832a320ee06d7803e82cdd7f69ac6f61d76a0d55fbbf1495c043", "22.04", "0.18.2", "image_tag"},
		{"ghcr.io/traefik/traefik:v3@sha256:652929a140a32d7cafafb13c6cdfab5376cfeff800f51397b87b524501ed02a8", "3.7.9", "3.7.9", "image_label"},
		{"ghcr.io/org/app:1.4.2-alpine", "22.04", "1.4.2-alpine", "image_tag"},
		{"ghcr.io/org/app:latest", "1.4.2", "1.4.2", "image_label"},
	} {
		current, source, _ := CurrentVersion(fixture.image, fixture.label)
		if current != fixture.current || source != fixture.source {
			t.Fatalf("application version provenance for %s: %s / %s", fixture.image, current, source)
		}
	}
}

func TestSelectLatestComparesOnlyLikeForLike(t *testing.T) {
	tags := []string{"latest", "1.4.2", "1.5.0", "1.5.1-rc1", "1.5.0-alpine", "2.0.0-beta.2", "1.10", "17", "18"}
	if latest, newer, ok := SelectLatest("1.4.2", tags); !ok || !newer || latest != "1.5.0" {
		t.Fatalf("1.4.2 -> %q newer=%v ok=%v, want 1.5.0: pre-releases, variants and aliases are not candidates", latest, newer, ok)
	}
	if latest, newer, ok := SelectLatest("1.5.0", tags); !ok || newer || latest != "1.5.0" {
		t.Fatalf("1.5.0 is current: %q newer=%v ok=%v", latest, newer, ok)
	}
	if latest, newer, _ := SelectLatest("1.5.0-alpine", tags); newer || latest != "1.5.0-alpine" {
		t.Fatalf("the alpine variant compares only with alpine tags: %q newer=%v", latest, newer)
	}
	if latest, newer, ok := SelectLatest("17", tags); !ok || !newer || latest != "18" {
		t.Fatalf("a major-only tag compares with major-only tags: %q newer=%v ok=%v", latest, newer, ok)
	}
	if current, _, reason := CurrentVersion("ghcr.io/org/app:latest", ""); current != "" || reason != ReasonFloatingTag {
		t.Fatalf("a floating tag is never a version: %q %q", current, reason)
	}
	if current, source, _ := CurrentVersion("ghcr.io/org/app:latest", "1.4.2"); current != "1.4.2" || source != "image_label" {
		t.Fatalf("the OCI label identifies a floating tag: %q %q", current, source)
	}
}

// The checker answers from the registry's anonymous token flow without ever
// blocking the caller: the first answer is pending, a later one is real.
func TestUpdateCheckerReadsTagsThroughTheAnonymousTokenFlow(t *testing.T) {
	registry := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			_ = json.NewEncoder(w).Encode(map[string]string{"token": "anon"})
		case r.URL.Path == "/v2/org/app/tags/list" && r.Header.Get("Authorization") == "Bearer anon":
			_ = json.NewEncoder(w).Encode(map[string]any{"tags": []string{"1.4.2", "1.5.0", "2.0.0-rc1", "latest"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer registry.Close()

	checker := NewUpdateChecker()
	checker.endpoints = func(string) (registryEndpoints, bool) {
		return registryEndpoints{api: registry.URL, tokenURL: registry.URL + "/token?scope=repository:%s:pull"}, true
	}
	if first := checker.Check("org/app:1.4.2", "1.4.2"); first.Latest != "" || first.ReasonCode != ReasonPending {
		t.Fatalf("first answer must be pending, got %#v", first)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		result := checker.Check("org/app:1.4.2", "1.4.2")
		if result.Latest == "" {
			time.Sleep(10 * time.Millisecond)
			continue
		}
		if !result.Newer || result.Latest != "1.5.0" || result.CheckedAt.IsZero() {
			t.Fatalf("result = %#v", result)
		}
		return
	}
	t.Fatalf("no registry answer arrived: %#v", checker.Check("org/app:1.4.2", "1.4.2"))
}

func TestUpdateCheckerRefusesRegistriesItCannotReach(t *testing.T) {
	checker := NewUpdateChecker()
	if result := checker.Check("registry.example.com/org/app:1.0.0", "1.0.0"); result.Latest != "" || !strings.Contains(result.ReasonCode, "unsupported") {
		t.Fatalf("a private registry is unsupported, never guessed: %#v", result)
	}
}
