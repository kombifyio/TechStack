package jobs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseStackKitCLIProgressRedactsSecrets(t *testing.T) {
	output := []byte(strings.Join([]string{
		`{"phase":"apt_wait","status":"failed","message":"token sk-AbCdEfGhIjKlMnOpQrStUvWxYz blocked","failure_class":"apt_lock_timeout","attributes":{"Authorization":"Bearer eyJhbGciOiJIUzI1NiIsInR5cCI6IkpXVCJ9"}}`,
		"human output",
	}, "\n"))

	events := parseStackKitCLIProgress(output)
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Phase != "apt_wait" || event.FailureClass != "apt_lock_timeout" {
		t.Fatalf("event = %+v", event)
	}
	raw := event.Message + event.Attributes["Authorization"]
	if strings.Contains(raw, "sk-AbCdEfGhIjKlMnOpQrStUvWxYz") || strings.Contains(raw, "eyJhbGci") {
		t.Fatalf("progress event leaked secret material: %+v", event)
	}
	if got := stackKitPrepReasonFromEvent(event); got != RuntimeTargetBootstrapTimeout {
		t.Fatalf("reason = %q, want %q", got, RuntimeTargetBootstrapTimeout)
	}
}

func TestParseStackKitCLIProgressKeepsLegacyFailureClass(t *testing.T) {
	events := parseStackKitCLIProgress([]byte(`{"phase":"docker","status":"failed","failureClass":"docker_install_failed"}`))
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	if events[0].FailureClass != "docker_install_failed" {
		t.Fatalf("failure class = %q", events[0].FailureClass)
	}
	if events[0].FailureClassLegacy != "" {
		t.Fatalf("legacy failure class should be normalized away: %+v", events[0])
	}
}

func TestStackKitPrepFailedCommandDoesNotReportReadyReason(t *testing.T) {
	if got := stackKitPrepFailureReason(RuntimeTargetBootstrapReady, []byte("Error: unknown flag: --progress-jsonl\n")); got != RuntimeTargetBootstrapUnknown {
		t.Fatalf("reason = %q, want %q", got, RuntimeTargetBootstrapUnknown)
	}
	if got := stackKitPrepFailureReason(RuntimeTargetBootstrapDockerFailed, []byte("Error: docker failed\n")); got != RuntimeTargetBootstrapDockerFailed {
		t.Fatalf("reason = %q, want existing classified reason", got)
	}
}

func TestStackKitPrepareArgsMatchPinnedCLI(t *testing.T) {
	args := stackKitPrepareArgs("/work", "stack-spec.yaml", &RuntimeActionTarget{
		Host: "203.0.113.20",
		User: "root",
		Port: 2022,
	}, "/tmp/id_stackkit")
	text := strings.Join(args, " ")

	for _, required := range []string{"--quiet", "--progress-jsonl -", "--chdir /work", "--spec stack-spec.yaml", "prepare", "--host 203.0.113.20", "--user root", "--non-interactive", "--port 2022", "--key /tmp/id_stackkit"} {
		if !strings.Contains(text, required) {
			t.Fatalf("args = %q missing %s", text, required)
		}
	}
}

func TestStackKitPrepTechStackEnvInjectsEnrollment(t *testing.T) {
	env := stackKitPrepTechStackEnv(RuntimeActionRequest{
		TechStackEnrollment: &TechStackEnrollment{
			TenantID:       "tenant-1",
			OwnerID:        "owner-1",
			StackID:        "stack-1",
			ServerURL:      "https://techstack.example",
			ServerID:       "server-1",
			RuntimeAgentID: "runtime-1",
			AgentToken:     "runtime-token",
			HeartbeatURL:   "https://techstack.example/api/v1/workers/runtime-1/heartbeat",
			InventoryURL:   "https://techstack.example/api/v1/workers/runtime-1/inventory",
			ChannelBootstrap: map[string]any{
				"websocket_url": "wss://techstack.example/api/v1/workers/runtime-1/control/ws",
			},
		},
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"TECHSTACK_MANAGED=true",
		"TECHSTACK_SERVER_URL=https://techstack.example",
		"TECHSTACK_SERVER_ID=server-1",
		"TECHSTACK_RUNTIME_AGENT_ID=runtime-1",
		"TECHSTACK_AGENT_TOKEN=runtime-token",
		"TECHSTACK_HEARTBEAT_URL=https://techstack.example/api/v1/workers/runtime-1/heartbeat",
		"TECHSTACK_INVENTORY_URL=https://techstack.example/api/v1/workers/runtime-1/inventory",
		"TECHSTACK_TENANT_ID=tenant-1",
		"TECHSTACK_OWNER_ID=owner-1",
		"TECHSTACK_STACK_ID=stack-1",
		"TECHSTACK_CHANNEL_BOOTSTRAP=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %q in:\n%s", want, joined)
		}
	}
}

func TestStackKitPrepTechStackEnvPreservesPartialEnrollmentForPrepareValidation(t *testing.T) {
	env := stackKitPrepTechStackEnv(RuntimeActionRequest{
		TechStackEnrollment: &TechStackEnrollment{
			ServerURL: "https://techstack.example",
			ServerID:  "server-1",
		},
	})
	joined := strings.Join(env, "\n")
	for _, want := range []string{
		"TECHSTACK_MANAGED=true",
		"TECHSTACK_SERVER_URL=https://techstack.example",
		"TECHSTACK_SERVER_ID=server-1",
		"TECHSTACK_RUNTIME_AGENT_ID=",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("env missing %q in:\n%s", want, joined)
		}
	}
}

func TestStackKitPrepSSHFilesWritesKnownHostsAndKey(t *testing.T) {
	originalScanner := stackKitPrepKnownHostScanner
	stackKitPrepKnownHostScanner = func(_ context.Context, target *RuntimeActionTarget) (string, error) {
		if target.Host != "203.0.113.20" {
			t.Fatalf("scan host = %q, want 203.0.113.20", target.Host)
		}
		return "203.0.113.20 ssh-ed25519 AAAATEST\n", nil
	}
	defer func() { stackKitPrepKnownHostScanner = originalScanner }()

	files, err := stackKitPrepSSHFiles(context.Background(), &RuntimeActionTarget{
		Host:       "203.0.113.20",
		User:       "root",
		Port:       22,
		PrivateKey: "test-private-key",
	})
	if err != nil {
		t.Fatalf("stackKitPrepSSHFiles: %v", err)
	}
	defer files.cleanup()

	if !strings.HasPrefix(files.homeDir, os.TempDir()) {
		t.Fatalf("homeDir = %q, want temp dir", files.homeDir)
	}
	keyBytes, err := os.ReadFile(files.keyPath)
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if string(keyBytes) != "test-private-key" {
		t.Fatalf("key = %q, want written private key", string(keyBytes))
	}
	knownHostsPath := filepath.Join(files.homeDir, ".ssh", "known_hosts")
	knownHosts, err := os.ReadFile(knownHostsPath)
	if err != nil {
		t.Fatalf("read known_hosts: %v", err)
	}
	if !strings.Contains(string(knownHosts), "ssh-ed25519") {
		t.Fatalf("known_hosts = %q, want scanned host key", string(knownHosts))
	}
}
