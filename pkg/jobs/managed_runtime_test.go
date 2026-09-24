package jobs

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

type managedRuntimeTargetResolverStub struct {
	calls  int
	target *ManagedRuntimeTarget
	err    error
}

func (r *managedRuntimeTargetResolverStub) ResolveManagedRuntimeTarget(context.Context, ManagedRuntimeTargetRequest) (*ManagedRuntimeTarget, error) {
	r.calls++
	if r.err != nil {
		return nil, r.err
	}
	return cloneManagedRuntimeTarget(r.target), nil
}

func TestResolveManagedRuntimeTargetUsesCredentialedJobFallbackWithoutLeaseResolver(t *testing.T) {
	tests := []struct {
		name string
		job  *Job
		want ManagedRuntimeTarget
	}{
		{
			name: "flat job result fields",
			job: &Job{TargetID: "stack-result", Result: map[string]interface{}{
				leaseIDField:                "lease-result",
				metadataKeyRuntimeSSHHost:   " 203.0.113.10 ",
				metadataKeyRuntimePublicIP:  "203.0.113.11",
				metadataKeyRuntimePrivateIP: "10.0.0.11",
				metadataKeyRuntimeSSHUser:   " ubuntu ",
				metadataKeyRuntimeSSHPort:   float64(2222),
				"runtime_docker_host":       " tcp://runtime.example.test:2375 ",
			}},
			want: ManagedRuntimeTarget{
				Host: "203.0.113.10", PublicIP: "203.0.113.11", PrivateIP: "10.0.0.11",
				SSHUser: "ubuntu", SSHPort: 2222, DockerHost: "tcp://runtime.example.test:2375", Source: "job-result",
			},
		},
		{
			name: "nested job payload target",
			job: &Job{TargetID: "stack-payload", Payload: map[string]interface{}{
				leaseIDField: "lease-payload",
				"runtime_target": map[string]interface{}{
					"ssh_host": "203.0.113.20", "public_ip": "203.0.113.21", "private_ip": "10.0.0.21",
					"ssh_user": "debian", "ssh_port": 2200, "client_private_key": "runtime-client-key",
				},
			}},
			want: ManagedRuntimeTarget{
				Host: "203.0.113.20", PublicIP: "203.0.113.21", PrivateIP: "10.0.0.21",
				SSHUser: "debian", SSHPort: 2200, SSHClientPrivateKey: "runtime-client-key", Source: "job-payload",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			target, err := resolveManagedRuntimeTarget(context.Background(), &ProvisionConfig{}, tt.job, nil)
			if err != nil {
				t.Fatalf("resolveManagedRuntimeTarget: %v", err)
			}
			if target == nil || target.Host != tt.want.Host || target.PublicIP != tt.want.PublicIP ||
				target.PrivateIP != tt.want.PrivateIP || target.SSHUser != tt.want.SSHUser || target.SSHPort != tt.want.SSHPort ||
				target.SSHClientPrivateKey != tt.want.SSHClientPrivateKey || target.DockerHost != tt.want.DockerHost || target.Source != tt.want.Source {
				t.Fatalf("resolved target = %+v, want mapped job fallback %+v", target, tt.want)
			}
		})
	}
}

func TestManagedRuntimeTargetDefaultsEmptyUserOntoExecutionChannel(t *testing.T) {
	empty := normalizeManagedRuntimeTarget(&ManagedRuntimeTarget{Host: "203.0.113.10"})
	if empty == nil || empty.SSHUser != guardbootstrap.ExecutionChannelUser {
		t.Fatalf("empty SSH user was not filled with the execution channel: %+v", empty)
	}
	kept := normalizeManagedRuntimeTarget(&ManagedRuntimeTarget{Host: "203.0.113.10", SSHUser: "debian"})
	if kept == nil || kept.SSHUser != "debian" {
		t.Fatalf("explicit non-root SSH user was rewritten: %+v", kept)
	}
	root := normalizeManagedRuntimeTarget(&ManagedRuntimeTarget{Host: "203.0.113.10", SSHUser: "root"})
	if root == nil || root.SSHUser != "root" {
		t.Fatalf("explicit root SSH user must remain so bootstrap can fall back after hardening: %+v", root)
	}
}

func TestResolveManagedRuntimeTargetRefreshesLeaseAuthorityBeforeFallback(t *testing.T) {
	refreshErr := errors.New("lease refresh failed")
	tests := []struct {
		name     string
		resolver *managedRuntimeTargetResolverStub
		wantErr  error
	}{
		{"current lease target wins", &managedRuntimeTargetResolverStub{target: &ManagedRuntimeTarget{
			Host: "203.0.113.31", SSHUser: "root", SSHPort: 22, SSHPrivateKey: "current-lease-key", Source: "lease-metadata",
		}}, nil},
		{"refresh failure blocks unbound fallback", &managedRuntimeTargetResolverStub{err: refreshErr}, refreshErr},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			job := &Job{
				ID: "job-1", Type: JobTypeDeploy, TargetID: "stack-1", TargetName: "stack-one",
				Payload: map[string]interface{}{leaseIDField: "lease-1", tenantIDField: "org-1"},
				Result: map[string]interface{}{
					metadataKeyRuntimeSSHHost: "203.0.113.30",
					"runtime_docker_host":     "tcp://runtime.example.test:2375",
				},
			}
			target, err := resolveManagedRuntimeTarget(context.Background(), &ProvisionConfig{
				RuntimeActions: RuntimeActions{RuntimeTargetResolver: tt.resolver},
			}, job, nil)
			if tt.resolver.calls != 1 {
				t.Fatalf("resolver calls = %d, want 1", tt.resolver.calls)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("resolveManagedRuntimeTarget error = %v, want lease refresh failure", err)
				}
				return
			}
			if err != nil || target == nil || target.Host != "203.0.113.31" || target.SSHPrivateKey != "current-lease-key" {
				t.Fatalf("target = %+v err = %v, want current lease resolver target", target, err)
			}
		})
	}
}

func TestManagedRuntimeTargetTerminalErrorTreatsRuntimeVMGoneAsTerminal(t *testing.T) {
	// The resolver wraps runtime errors (fmt.Errorf %w chains); errors.Is must
	// still classify a ghost lease as terminal so the 5-minute enrollment wait
	// loop stops immediately instead of polling a VM that no longer exists.
	err := fmt.Errorf("managed runtime lease address is not available yet for lease %q: %w",
		"lease-ghost", fmt.Errorf("%w for lease %q: managed runtime ssh_info returned 404", monthlyruntime.ErrRuntimeVMGone, "lease-ghost"))
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
