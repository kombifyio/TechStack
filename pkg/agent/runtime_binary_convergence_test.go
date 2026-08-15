package agent

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestEnsureTechstackRuntimeUpdatesAgentAndOperations(t *testing.T) {
	root := t.TempDir()
	agentPath := filepath.Join(root, "bin", "techstack")
	operationsPath := filepath.Join(root, "libexec", "techstack-stackkit-operations")
	writeRuntimeFixture(t, agentPath, "old")
	wanted := []byte("exact-current-runtime")
	digest := sha256.Sum256(wanted)
	expected := hex.EncodeToString(digest[:])
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer token" || request.Header.Get("X-Kombify-Runtime-Agent-ID") != "agent-1" {
			t.Fatalf("authenticated runtime headers missing")
		}
		response.Header().Set("X-Kombify-Artifact-SHA256", expected)
		_, _ = response.Write(wanted)
	}))
	defer server.Close()

	result, err := (&StackKitExecutor{}).EnsureTechstackRuntime(t.Context(), TechstackRuntimeConvergenceConfig{
		URL: server.URL, AgentToken: "token", RuntimeAgentID: "agent-1", TenantID: "tenant-1",
		AgentPath: agentPath, OperationsPath: operationsPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AgentUpdated || !result.OperationsUpdated || result.SHA256 != expected {
		t.Fatalf("result = %+v", result)
	}
	for _, path := range []string{agentPath, operationsPath} {
		data, readErr := os.ReadFile(path)
		if readErr != nil || string(data) != string(wanted) {
			t.Fatalf("runtime %s = %q, err=%v", path, data, readErr)
		}
	}
}

func TestEnsureTechstackRuntimeNotModifiedRepairsOperationsOnly(t *testing.T) {
	root := t.TempDir()
	agentPath := filepath.Join(root, "bin", "techstack")
	operationsPath := filepath.Join(root, "libexec", "techstack-stackkit-operations")
	writeRuntimeFixture(t, agentPath, "exact-current-runtime")
	wantedDigest, err := digestRuntimeExecutable(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Header.Get("If-None-Match") != `"`+wantedDigest+`"` {
			t.Fatalf("If-None-Match = %q", request.Header.Get("If-None-Match"))
		}
		response.WriteHeader(http.StatusNotModified)
	}))
	defer server.Close()

	result, err := (&StackKitExecutor{}).EnsureTechstackRuntime(t.Context(), TechstackRuntimeConvergenceConfig{
		URL: server.URL, AgentToken: "token", RuntimeAgentID: "agent-1", TenantID: "tenant-1",
		AgentPath: agentPath, OperationsPath: operationsPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentUpdated || !result.OperationsUpdated || result.SHA256 != wantedDigest {
		t.Fatalf("result = %+v", result)
	}
	if digest, digestErr := digestRuntimeExecutable(operationsPath); digestErr != nil || digest != wantedDigest {
		t.Fatalf("operations digest = %q, err=%v", digest, digestErr)
	}
}

func writeRuntimeFixture(t *testing.T, path, value string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(value), 0755); err != nil {
		t.Fatal(err)
	}
}

// The control plane's platform proxy resets a single large response part-way,
// so the artifact is pulled in slices. This drives a server that serves only
// Range requests and, like the real proxy, cuts the first attempt at every
// offset short exactly once.
func TestEnsureTechstackRuntimeResumesSlicedTransferAcrossResets(t *testing.T) {
	root := t.TempDir()
	agentPath := filepath.Join(root, "bin", "techstack")
	operationsPath := filepath.Join(root, "libexec", "techstack-stackkit-operations")
	writeRuntimeFixture(t, agentPath, "old")

	wanted := make([]byte, int(runtimeBinarySliceBytes)*3+1237)
	for i := range wanted {
		wanted[i] = byte(i % 251)
	}
	digest := sha256.Sum256(wanted)
	expected := hex.EncodeToString(digest[:])

	var mu sync.Mutex
	cutOffsets := map[int64]bool{}
	var servedRequests int

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		rangeHeader := request.Header.Get("Range")
		if rangeHeader == "" {
			t.Fatalf("every slice must carry a Range header")
		}
		var start, end int64
		if _, err := fmt.Sscanf(rangeHeader, "bytes=%d-%d", &start, &end); err != nil {
			t.Fatalf("unparseable range %q: %v", rangeHeader, err)
		}
		if end >= int64(len(wanted)) {
			end = int64(len(wanted)) - 1
		}
		mu.Lock()
		servedRequests++
		firstTouch := !cutOffsets[start]
		cutOffsets[start] = true
		mu.Unlock()

		response.Header().Set("X-Kombify-Artifact-SHA256", expected)
		response.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, len(wanted)))
		response.WriteHeader(http.StatusPartialContent)
		body := wanted[start : end+1]
		if firstTouch {
			// Deliver a short body once per offset, the way a reset connection
			// does. The Agent must retry the same offset, not advance past it.
			body = body[:len(body)/2]
		}
		_, _ = response.Write(body)
	}))
	defer server.Close()

	result, err := (&StackKitExecutor{}).EnsureTechstackRuntime(t.Context(), TechstackRuntimeConvergenceConfig{
		URL: server.URL, AgentToken: "token", RuntimeAgentID: "agent-1", TenantID: "tenant-1",
		AgentPath: agentPath, OperationsPath: operationsPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !result.AgentUpdated || result.SHA256 != expected {
		t.Fatalf("result = %+v", result)
	}
	if servedRequests < 8 {
		t.Fatalf("served requests = %d, want the artifact fetched in slices", servedRequests)
	}
	got, err := os.ReadFile(agentPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, wanted) {
		t.Fatalf("converged binary = %d bytes, want %d identical bytes", len(got), len(wanted))
	}
}
