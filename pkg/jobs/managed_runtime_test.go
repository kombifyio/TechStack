package jobs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

type failingManagedRuntimeTargetResolver struct {
	calls int
}

func (r *failingManagedRuntimeTargetResolver) ResolveManagedRuntimeTarget(context.Context, ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	r.calls++
	return nil, errors.New("resolver should not be called")
}

type recordingManagedRuntimeTargetResolver struct {
	calls  int
	target *ManagedRuntimeTarget
}

func (r *recordingManagedRuntimeTargetResolver) ResolveManagedRuntimeTarget(context.Context, ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	r.calls++
	return cloneManagedRuntimeTarget(r.target), nil
}

func TestManagedRuntimeTargetFromRuntimeMapUsesJobResultFields(t *testing.T) {
	result := map[string]interface{}{
		leaseIDField:                "lease-result",
		metadataKeyRuntimeSSHHost:   " 203.0.113.10 ",
		metadataKeyRuntimePublicIP:  "203.0.113.11",
		metadataKeyRuntimePrivateIP: "10.0.0.11",
		metadataKeyRuntimeSSHUser:   " ubuntu ",
		metadataKeyRuntimeSSHPort:   float64(2222),
		"runtime_docker_host":       " tcp://runtime.example.test:2375 ",
	}

	target := managedRuntimeTargetFromRuntimeMap(result, "job-result")
	if target == nil {
		t.Fatal("managedRuntimeTargetFromRuntimeMap returned nil")
	}
	if target.Host != "203.0.113.10" {
		t.Fatalf("Host = %q, want SSH host (trimmed)", target.Host)
	}
	if target.PublicIP != "203.0.113.11" {
		t.Fatalf("PublicIP = %q, want runtime_public_ip", target.PublicIP)
	}
	if target.PrivateIP != "10.0.0.11" {
		t.Fatalf("PrivateIP = %q, want runtime_private_ip", target.PrivateIP)
	}
	if target.SSHUser != "ubuntu" {
		t.Fatalf("SSHUser = %q, want trimmed runtime_ssh_user", target.SSHUser)
	}
	if target.SSHPort != 2222 {
		t.Fatalf("SSHPort = %d, want runtime_ssh_port", target.SSHPort)
	}
	if target.DockerHost != "tcp://runtime.example.test:2375" {
		t.Fatalf("DockerHost = %q, want trimmed runtime_docker_host", target.DockerHost)
	}
	if target.Source != "job-result" {
		t.Fatalf("Source = %q, want job-result", target.Source)
	}
}

func TestManagedRuntimeTargetFromRuntimeMapUsesNestedRuntimeTarget(t *testing.T) {
	payload := map[string]interface{}{
		leaseIDField: "lease-payload",
		"runtime_target": map[string]interface{}{
			"ssh_host":           "203.0.113.20",
			"public_ip":          "203.0.113.21",
			"private_ip":         "10.0.0.21",
			"ssh_user":           "debian",
			"ssh_port":           2200,
			"client_private_key": "runtime-client-key",
		},
	}

	target := managedRuntimeTargetFromRuntimeMap(payload, "job-payload")
	if target == nil {
		t.Fatal("managedRuntimeTargetFromRuntimeMap returned nil")
	}
	if target.Host != "203.0.113.20" {
		t.Fatalf("Host = %q, want nested ssh_host", target.Host)
	}
	if target.PublicIP != "203.0.113.21" {
		t.Fatalf("PublicIP = %q, want nested public_ip", target.PublicIP)
	}
	if target.PrivateIP != "10.0.0.21" {
		t.Fatalf("PrivateIP = %q, want nested private_ip", target.PrivateIP)
	}
	if target.SSHUser != "debian" {
		t.Fatalf("SSHUser = %q, want nested ssh_user", target.SSHUser)
	}
	if target.SSHPort != 2200 {
		t.Fatalf("SSHPort = %d, want nested ssh_port", target.SSHPort)
	}
	if target.SSHClientPrivateKey != "runtime-client-key" {
		t.Fatalf("SSHClientPrivateKey = %q, want nested client_private_key", target.SSHClientPrivateKey)
	}
	if target.Source != "job-payload" {
		t.Fatalf("Source = %q, want job-payload", target.Source)
	}
}

func TestResolveManagedRuntimeTargetRefreshesCredentialedJobFallbackFromLeaseResolver(t *testing.T) {
	resolver := &recordingManagedRuntimeTargetResolver{target: &ManagedRuntimeTarget{
		Host:          "203.0.113.31",
		SSHUser:       "root",
		SSHPort:       22,
		SSHPrivateKey: "current-lease-key",
		Source:        "lease-metadata",
	}}
	job := &Job{
		ID:         "job-1",
		Type:       JobTypeDeploy,
		TargetID:   "stack-1",
		TargetName: "stack-one",
		Payload: map[string]interface{}{
			leaseIDField:  "lease-1",
			tenantIDField: "org-1",
		},
		Result: map[string]interface{}{
			metadataKeyRuntimeSSHHost: "203.0.113.30",
			"runtime_docker_host":     "tcp://runtime.example.test:2375",
		},
	}

	target, err := resolveManagedRuntimeTarget(context.Background(), &ProvisionConfig{
		RuntimeActions: RuntimeActions{RuntimeTargetResolver: resolver},
	}, job, nil)
	if err != nil {
		t.Fatalf("resolveManagedRuntimeTarget: %v", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
	if target == nil || target.Host != "203.0.113.31" || target.SSHPrivateKey != "current-lease-key" {
		t.Fatalf("target = %+v, want current lease resolver target", target)
	}
}

func TestResolveManagedRuntimeTargetDoesNotUseUnboundFallbackWhenLeaseRefreshFails(t *testing.T) {
	resolver := &failingManagedRuntimeTargetResolver{}
	job := &Job{
		TargetID: "stack-1",
		Payload: map[string]interface{}{
			leaseIDField:  "lease-1",
			tenantIDField: "org-1",
		},
		Result: map[string]interface{}{
			metadataKeyRuntimeSSHHost: "203.0.113.30",
			"runtime_docker_host":     "tcp://runtime.example.test:2375",
		},
	}

	_, err := resolveManagedRuntimeTarget(context.Background(), &ProvisionConfig{
		RuntimeActions: RuntimeActions{RuntimeTargetResolver: resolver},
	}, job, nil)
	if err == nil || !strings.Contains(err.Error(), "resolver should not be called") {
		t.Fatalf("resolveManagedRuntimeTarget error = %v, want current lease resolver failure", err)
	}
	if resolver.calls != 1 {
		t.Fatalf("resolver calls = %d, want 1", resolver.calls)
	}
}

func TestResolveManagedRuntimeTargetUsesCredentialedFallbackWithoutLeaseResolver(t *testing.T) {
	job := &Job{
		TargetID: "stack-1",
		Result: map[string]interface{}{
			metadataKeyRuntimeSSHHost: "203.0.113.30",
			"runtime_docker_host":     "tcp://runtime.example.test:2375",
		},
	}

	target, err := resolveManagedRuntimeTarget(context.Background(), &ProvisionConfig{}, job, nil)
	if err != nil {
		t.Fatalf("resolveManagedRuntimeTarget: %v", err)
	}
	if target == nil || target.Host != "203.0.113.30" {
		t.Fatalf("target = %+v, want local credentialed fallback", target)
	}
}

func TestManagedRuntimeTargetTerminalErrorTreatsRuntimeVMGoneAsTerminal(t *testing.T) {
	// The resolver wraps runtime errors (fmt.Errorf %w chains); errors.Is must
	// still classify a ghost lease as terminal so the 5-minute enrollment wait
	// loop stops immediately instead of polling a VM that no longer exists.
	err := fmt.Errorf("managed runtime lease address is not available yet for lease %q: %w",
		"lease-ghost", fmt.Errorf("%w for lease %q: simulate runtime ssh_info returned 404", monthlyruntime.ErrRuntimeVMGone, "lease-ghost"))
	if !managedRuntimeTargetTerminalError(err) {
		t.Fatalf("managedRuntimeTargetTerminalError(%v) = false, want true", err)
	}
}

func TestDeployRuntimeActionModeFollowsSpecInsteadOfHardcodingAdvanced(t *testing.T) {
	specWith := func(mode string) *deployPreparation {
		metadata := map[string]string{}
		if mode != "" {
			metadata["mode"] = mode
		}
		return &deployPreparation{
			managedRuntime: true,
			kombSpec:       &core.KombinationSpec{Metadata: metadata},
		}
	}
	cases := []struct {
		name string
		prep *deployPreparation
		want string
	}{
		{"managed empty spec defaults bootstrapped", specWith(""), "bootstrapped"},
		{"managed wizard mode folds to bootstrapped", specWith("easy"), "bootstrapped"},
		{"managed legacy simple folds to bootstrapped", specWith("simple"), "bootstrapped"},
		{"managed explicit advanced is preserved", specWith("advanced"), "advanced"},
		{"managed legacy terramate folds to advanced", specWith("terramate"), "advanced"},
		{"managed bare is preserved", specWith("bare"), "bare"},
	}
	for _, tc := range cases {
		if got := deployRuntimeActionMode(&Job{}, tc.prep); got != tc.want {
			t.Fatalf("%s: deployRuntimeActionMode = %q, want %q", tc.name, got, tc.want)
		}
	}

	selfHosted := &deployPreparation{
		managedRuntime: false,
		kombSpec:       &core.KombinationSpec{Metadata: map[string]string{"mode": "easy"}},
	}
	if got := deployRuntimeActionMode(&Job{}, selfHosted); got != "easy" {
		t.Fatalf("self-hosted mode pass-through = %q, want easy (unchanged behavior)", got)
	}
}
