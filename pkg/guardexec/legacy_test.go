package guardexec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentpkg "github.com/kombifyio/techstack/pkg/agent"
	"github.com/kombifyio/techstack/pkg/guardrelease"
)

// A root Guard whose control plane serves a binary without signed release
// evidence keeps its executable: the control plane's own digest header is not
// enough to replace a root process.
func TestLegacyRootRefusesUnsignedSelfUpdate(t *testing.T) {
	root := t.TempDir()
	agentPath := filepath.Join(root, "bin", "techstack")
	if err := os.MkdirAll(filepath.Dir(agentPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(agentPath, []byte("installed-guard"), 0o755); err != nil {
		t.Fatal(err)
	}
	served := []byte("control-plane-binary")
	digest := sha256.Sum256(served)
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/release-evidence/") {
			http.NotFound(response, request)
			return
		}
		response.Header().Set("X-Kombify-Artifact-SHA256", hex.EncodeToString(digest[:]))
		response.Header().Set("X-Kombify-Artifact-Version", "9.9.9")
		_, _ = response.Write(served)
	}))
	defer server.Close()

	legacy := NewLegacyRoot(&agentpkg.StackKitExecutor{}, Options{Version: "0.50.0", StateDir: filepath.Join(root, "state"), Getenv: func(string) string { return "" }})
	_, err := legacy.EnsureTechstackRuntime(t.Context(), agentpkg.TechstackRuntimeConvergenceConfig{
		URL: server.URL + "/api/v1/agent/binary/linux/amd64", AgentToken: "token", RuntimeAgentID: "agent-1", TenantID: "tenant-1",
		AgentPath: agentPath, OperationsPath: filepath.Join(root, "libexec", "techstack-stackkit-operations"),
	})
	var denied *UpdateDeniedError
	if !errors.As(err, &denied) || denied.Code != guardrelease.CodeEvidenceMissing {
		t.Fatalf("an unsigned candidate was not refused as missing evidence: %v", err)
	}
	if installed, _ := os.ReadFile(agentPath); string(installed) != "installed-guard" {
		t.Fatalf("the root Guard replaced its executable with an unsigned binary: %q", installed)
	}
}
