package stacks

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// Provider-control invariant: an existing instance must never allocate a
// replacement, and accepted appliance receipts survive idempotent replay.
func TestWizardHAOSAppliancePreservesExistingAndReplaysReceipt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.json")
	if err := os.WriteFile(path, []byte(`{"schemaVersion":"stackkits-use-case-catalog/v1","catalog":{"useCases":[{"id":"smart-home","title":"Smart Home","settings":[{"id":"operating-form","kind":"choice","options":[{"id":"haos"},{"id":"container"}]},{"id":"instance-origin","kind":"choice","options":[{"id":"new"},{"id":"existing"}]}]}]}}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TECHSTACK_STACKKIT_USE_CASE_CATALOG", path)
	store := controlplane.NewMemoryStore()
	h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
	h.cfg.Projector = specv2.NewReleaseProjector(wizardRunReleaseGoalAuthor{supported: map[string]bool{"smart-home": true}})
	intent := wizardRunTestIntent(specv2.RunKindFirstRun)
	intent.Goals = []string{"smart-home"}
	intent.Server.Transport = specv2.TransportHypervisor
	intent.UseCaseSettings = map[string]map[string]any{"smart-home": {"operating-form": "haos", "instance-origin": "existing"}}
	request := wizardRunRequest{Intent: intent, Substrate: &WizardSubstrateParams{ServerID: "substrate-owned", ProfileID: "ubuntu-24.04", Storage: "local", Bridge: "vmbr0", CPU: 2, MemoryMiB: 4096, DiskGiB: 64, Appliance: &WizardApplianceParams{ProfileID: "haos", Storage: "local", Bridge: "vmbr1", CPU: 2, MemoryMiB: 4096, DiskGiB: 32}}}
	calls := 0
	h.cfg.GuestProvisioner = WizardGuestProvisionerFunc(func(_ *httpx.Event, r WizardGuestProvisionRequest) (*WizardGuestProvisionResult, error) {
		calls++
		if r.Guest.ProfileID != "ubuntu-24.04" || r.Guest.Appliance == nil || r.Guest.Appliance.ProfileID != "haos" || r.Guest.Appliance.Bridge != "vmbr1" {
			t.Fatal("appliance replaced Core placement")
		}
		return &WizardGuestProvisionResult{JobID: "core-rollout", ServerID: "core-server", OperationID: "core-op", Appliance: &jobs.CustomerGuestBinding{LeaseID: "ha-lease", ServerID: "ha-server", OperationID: "ha-op"}}, nil
	})
	e, rec := wizardRunTestEvent(t, request, "existing-appliance")
	if err := h.createWizardRun(e); err != nil || rec.Code != http.StatusBadRequest || calls != 0 {
		t.Fatalf("existing replacement accepted: %v %s", err, rec.Body.String())
	}
	request.Intent.UseCaseSettings["smart-home"]["instance-origin"] = "new"
	for range 2 {
		e, rec = wizardRunTestEvent(t, request, "new-appliance")
		if err := h.createWizardRun(e); err != nil || rec.Code != http.StatusAccepted {
			t.Fatalf("new appliance failed: %v %s", err, rec.Body.String())
		}
		data := decodeWizardRunSuccess(t, rec)
		appliance, _ := data["appliance"].(map[string]any)
		if data["job_id"] != "core-rollout" || appliance["operation_id"] != "ha-op" || appliance["server_id"] != "ha-server" {
			t.Fatal("separate appliance receipt lost")
		}
	}
	if calls != 1 {
		t.Fatal("replay allocated another appliance")
	}
}

// Sensitive boundary: allocation retries must reuse authority while keeping
// provider intent and enrollment secrets outside the canonical StackKit spec.
func TestWizardHypervisorFoundAndJoinResume(t *testing.T) {
	for _, join := range []bool{false, true} {
		t.Run(map[bool]string{false: "found", true: "join"}[join], func(t *testing.T) {
			store := controlplane.NewMemoryStore()
			h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
			intent := wizardRunTestIntent(specv2.RunKindFirstRun)
			if join {
				wizardRunSeedV2Stack(t, store, "stack-v2", "auth0|user-1", map[string]any{stackConfigKeySpecV2: wizardRunTestSeed(specv2.KitSlugBasement)})
				intent = wizardRunJoinIntent("stack-v2")
			}
			intent.Server.Transport = specv2.TransportHypervisor
			request := wizardRunRequest{Intent: intent, Substrate: &WizardSubstrateParams{ServerID: "substrate-owned", ProfileID: "ubuntu-24.04", Storage: "vm-import", Bridge: "vmbr0", CPU: 2, MemoryMiB: 4096, DiskGiB: 64}}
			var initial WizardGuestProvisionRequest
			calls := 0
			h.cfg.GuestProvisioner = WizardGuestProvisionerFunc(func(_ *httpx.Event, got WizardGuestProvisionRequest) (*WizardGuestProvisionResult, error) {
				calls++
				if got.TenantID != "tenant-1" || got.OwnerID != "auth0|user-1" || got.Pairing.Token == "" || got.ReleaseVersion != h.cfg.ReleaseVersion || got.Guest != *request.Substrate {
					t.Fatal("guest adapter lost authenticated custody or provisioning intent")
				}
				if calls == 1 {
					initial = got
					return nil, errors.New("connection lost after submit")
				}
				if got.IdempotencyKey != initial.IdempotencyKey || got.StackID != initial.StackID || got.NodeID != initial.NodeID {
					t.Fatal("retry changed allocation identity")
				}
				return &WizardGuestProvisionResult{JobID: "durable-guest-job", OperationID: "operation-existing", ServerID: "guest-owned"}, nil
			})
			for attempt, status := range []int{http.StatusServiceUnavailable, http.StatusAccepted, http.StatusAccepted} {
				e, rec := wizardRunTestEvent(t, request, "hypervisor-key")
				if err := h.createWizardRun(e); err != nil || rec.Code != status {
					t.Fatalf("attempt %d: err=%v status=%d body=%s", attempt, err, rec.Code, rec.Body.String())
				}
				if strings.Contains(rec.Body.String(), initial.Pairing.Token) && initial.Pairing.Token != "" {
					t.Fatal("pairing secret leaked into run response")
				}
				if attempt > 0 {
					data := decodeWizardRunSuccess(t, rec)
					if data["job_id"] != "durable-guest-job" || data["operation_id"] != "operation-existing" || data["state"] != "provisioning" {
						t.Fatal("accepted run lost pending guest correlation")
					}
				}
				if attempt == 0 {
					changed := request
					guest := *request.Substrate
					guest.CPU++
					changed.Substrate = &guest
					e, conflict := wizardRunTestEvent(t, changed, "hypervisor-key")
					if err := h.createWizardRun(e); err != nil || conflict.Code != http.StatusConflict {
						t.Fatal("changed guest intent reused an interrupted operation")
					}
				}
			}
			if calls != 2 {
				t.Fatal("completed replay dispatched guest again")
			}
			stack, err := store.GetStack(t.Context(), "tenant-1", initial.StackID)
			if err != nil {
				t.Fatal(err)
			}
			spec, _ := json.Marshal(stack.Config[stackConfigKeySpecV2])
			if strings.Contains(string(spec), "substrate-owned") || strings.Contains(string(spec), "vm-import") || strings.Contains(string(spec), initial.Pairing.Token) {
				t.Fatal("provider or pairing details entered StackKit spec")
			}
		})
	}
}

func TestWizardHypervisorRejectsUnavailableOrApplianceCore(t *testing.T) {
	for _, profile := range []string{"ubuntu-24.04", "haos"} {
		store := controlplane.NewMemoryStore()
		h := newWizardRunTestHandlers(store, &wizardRunFakeValidator{})
		intent := wizardRunTestIntent(specv2.RunKindFirstRun)
		intent.Server.Transport = specv2.TransportHypervisor
		e, rec := wizardRunTestEvent(t, wizardRunRequest{Intent: intent, Substrate: &WizardSubstrateParams{ServerID: "substrate", ProfileID: profile, Storage: "local", Bridge: "vmbr0", CPU: 2, MemoryMiB: 4096, DiskGiB: 64}}, "reject-key")
		if err := h.createWizardRun(e); err != nil {
			t.Fatal(err)
		}
		if rec.Code != http.StatusBadRequest && rec.Code != http.StatusServiceUnavailable {
			t.Fatal("unsupported guest provisioning was accepted")
		}
		if _, err := store.GetHomelabByOwner(t.Context(), "tenant-1", "auth0|user-1"); err == nil {
			t.Fatal("rejected provisioning created a homelab")
		}
	}
}
