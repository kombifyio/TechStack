package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestStackKitsCoolifyBootstrapReadinessRaceClassification(t *testing.T) {
	retryable := []error{
		errors.New("runtime action stackkit_rollout request failed: opentofu_apply_failed: StackKit Coolify bootstrap could not find a Coolify server"),
		errors.New("StackKit Coolify bootstrap: Coolify API token bootstrap did not return platform config JSON: missing email_notification_settings"),
		errors.New("runtime action stackkit_rollout returned 502: {\"error\":{\"error_code\":\"opentofu_apply_failed\",\"details\":{\"stderr\":\"Coolify proxy compose failed; replacing with StackKit-managed proxy fallback\"}}}"),
		errors.New("runtime action stackkit_rollout returned 502: {\"error\":{\"error_code\":\"opentofu_apply_failed\",\"details\":{\"stderr\":\"Step 4/9: Checking Docker configuration - Docker daemon restarted successfully\\nStep 7/9: Checking and updating environment variables\"}}}"),
	}
	for _, err := range retryable {
		if !isStackKitsCoolifyBootstrapReadinessRace(err) {
			t.Fatalf("expected retryable Coolify bootstrap readiness race for %q", err.Error())
		}
	}

	notRetryable := []error{
		nil,
		errors.New("opentofu_apply_failed: docker daemon unavailable"),
		errors.New("StackKit Dokploy bootstrap could not find a project"),
		errors.New("StackKit Coolify bootstrap returned invalid admin credentials"),
	}
	for _, err := range notRetryable {
		if isStackKitsCoolifyBootstrapReadinessRace(err) {
			t.Fatalf("did not expect retryable Coolify bootstrap readiness race for %v", err)
		}
	}
}

func TestRestoreStackKitsRetryLocalArtifactsCreatesOnlyMissingFiles(t *testing.T) {
	dir := t.TempDir()
	existingServices := "existing-services: true\n"
	if err := os.WriteFile(filepath.Join(dir, ".homepage-services.yaml"), []byte(existingServices), 0600); err != nil {
		t.Fatalf("write existing services: %v", err)
	}

	restored, err := restoreStackKitsRetryLocalArtifacts(dir)
	if err != nil {
		t.Fatalf("restoreStackKitsRetryLocalArtifacts: %v", err)
	}
	for _, name := range []string{".homepage-settings.yaml", ".homepage-widgets.yaml", ".homepage-docker.yaml"} {
		if !slices.Contains(restored, name) {
			t.Fatalf("restored = %v, want %s", restored, name)
		}
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("expected %s to exist: %v", name, err)
		}
	}
	if slices.Contains(restored, ".homepage-services.yaml") {
		t.Fatalf("restored = %v, did not expect existing services file to be overwritten", restored)
	}
	services, err := os.ReadFile(filepath.Join(dir, ".homepage-services.yaml"))
	if err != nil {
		t.Fatalf("read existing services: %v", err)
	}
	if string(services) != existingServices {
		t.Fatalf("services file = %q, want existing content", string(services))
	}
}

func TestTypedStackKitsRolloutDoesNotInjectLegacyArtifacts(t *testing.T) {
	dir := t.TempDir()
	restored, err := restoreLegacyStackKitsRetryLocalArtifacts(true, dir)
	if err != nil {
		t.Fatalf("restoreLegacyStackKitsRetryLocalArtifacts: %v", err)
	}
	if len(restored) != 0 {
		t.Fatalf("restored = %v, want no undeclared files for typed v2 rollout", restored)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read output dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("output entries = %v, want a closed untouched tree", entries)
	}
}

func TestStackKitsLocalFileArtifactMissingClassification(t *testing.T) {
	err := errors.New(`opentofu_apply_failed: Unable to create container: Error response from daemon: invalid mount config for type "bind": bind source path does not exist: /data/stacks/6316/tofu/.homepage-services.yaml`)
	if !isStackKitsRolloutRetryable(err) {
		t.Fatalf("expected missing StackKits local_file artifact to be retryable")
	}

	notRetryable := errors.New(`opentofu_apply_failed: Unable to create container: invalid mount config for type "bind": bind source path does not exist: /etc/passwd`)
	if isStackKitsRolloutRetryable(notRetryable) {
		t.Fatalf("did not expect unrelated missing bind source to be retryable")
	}
}

func TestStackKitsPlatformAppReadinessTransientClassification(t *testing.T) {
	err := errors.New(`runtime action stackkit_rollout returned 502: {"error":{"error_code":"platform_apps_deploy_failed","details":{"error":"deploy platform system app \"stackkit-hub\": coolify service create \"stackkit-hub\": platform API request POST /api/v1/services failed: Post \"http://127.0.0.1:8000/api/v1/services\": dial tcp 127.0.0.1:8000: connect: connection refused"}}}`)
	if !isStackKitsRolloutRetryable(err) {
		t.Fatalf("expected Coolify platform app readiness failure to be retryable")
	}

	readinessErr := errors.New(`runtime action stackkit_rollout returned 502: {"error":{"error_code":"platform_apps_deploy_failed","details":{"error":"wait for platform API readiness: coolify API did not become ready after 2m0s: platform API GET /api/v1/health returned status 503"}}}`)
	if !isStackKitsRolloutRetryable(readinessErr) {
		t.Fatalf("expected Coolify platform API readiness timeout to be retryable")
	}

	notRetryable := errors.New(`platform_apps_deploy_failed: deploy platform system app "stackkit-hub": invalid api token`)
	if isStackKitsRolloutRetryable(notRetryable) {
		t.Fatalf("did not expect non-readiness platform app failure to be retryable")
	}
}

func TestStackKitsDockerImagePullTransientClassification(t *testing.T) {
	retryable := []error{
		errors.New(`runtime action stackkit_rollout returned 502: {"error":{"error_code":"opentofu_apply_failed","details":{"stderr":"unable to read Docker Image into resource: unable to find or pull image ghcr.io/steveiliop56/tinyauth:v5.0.7: failed to copy: failed to send write: EOF"}}}`),
		errors.New(`opentofu_apply_failed: unable to read Docker Image into resource: unable to find or pull image ghcr.io/pocket-id/pocket-id:v2.7.0: net/http: TLS handshake timeout`),
		errors.New(`opentofu_apply_failed: unable to pull image ghcr.io/gethomepage/homepage:latest: connection reset by peer`),
		errors.New(`opentofu_apply_failed: unable to pull image ghcr.io/pocket-id/pocket-id:v2.7.0: error pulling image ghcr.io/pocket-id/pocket-id:v2.7.0: Error response from daemon: rpc error: code = Canceled desc = grpc: the client connection is closing: context canceled`),
	}
	for _, err := range retryable {
		if !isStackKitsRolloutRetryable(err) {
			t.Fatalf("expected retryable StackKits image pull transient for %q", err.Error())
		}
	}

	notRetryable := []error{
		nil,
		errors.New("opentofu_apply_failed: docker daemon unavailable"),
		errors.New("opentofu_apply_failed: unable to find or pull image ghcr.io/gethomepage/homepage:missing-tag: manifest unknown"),
		errors.New("opentofu_apply_failed: invalid registry credentials"),
	}
	for _, err := range notRetryable {
		if isStackKitsRolloutRetryable(err) {
			t.Fatalf("did not expect retryable StackKits image pull transient for %v", err)
		}
	}
}

func TestRedactedStackKitsRolloutRetryError(t *testing.T) {
	err := errors.New("runtime action failed: token=abcdefghijklmnopqrstuvwxyz0123456789abcdef and " + strings.Repeat("x", 2200))

	got := redactedStackKitsRolloutRetryError(err)

	if strings.Contains(got, "abcdefghijklmnopqrstuvwxyz0123456789abcdef") {
		t.Fatalf("retry error leaked token: %s", got)
	}
	if !strings.Contains(got, "[REDACTED]") {
		t.Fatalf("retry error did not include redaction marker: %s", got)
	}
	if !strings.Contains(got, "(truncated)") {
		t.Fatalf("retry error should be truncated, got length %d", len(got))
	}
}
