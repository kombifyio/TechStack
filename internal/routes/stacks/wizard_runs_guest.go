package stacks

import (
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/internal/substrate"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/specv2"
)

// WizardSubstrateParams is provisioning intent, never part of the StackKit spec.
// Credentials, provider VM handles and image transport remain with the adapter.
type WizardSubstrateParams struct {
	ServerID  string                 `json:"server_id"`
	ProfileID string                 `json:"profile_id"`
	Storage   string                 `json:"storage"`
	Bridge    string                 `json:"bridge"`
	CPU       int                    `json:"cpu"`
	MemoryMiB int                    `json:"memory_mib"`
	DiskGiB   int                    `json:"disk_gib"`
	Appliance *WizardApplianceParams `json:"appliance,omitempty"`
	// Accelerator is the GPU on the substrate the guest receives, as the
	// node's readiness report found it.
	Accelerator *substrate.PCIPassthrough `json:"accelerator,omitempty"`
	// RequiresAccelerator is derived from the pinned release catalog for the
	// selected use cases and is never taken from the client.
	RequiresAccelerator bool `json:"-"`
}

type WizardApplianceParams struct {
	ProfileID string `json:"profile_id"`
	Storage   string `json:"storage"`
	Bridge    string `json:"bridge"`
	CPU       int    `json:"cpu"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
}

func wizardRequestsNewHAOS(request wizardRunRequest) bool {
	s := request.Intent.UseCaseSettings["smart-home"]
	return s["operating-form"] == "haos" && s["instance-origin"] != "existing"
}

// WizardGuestProvisioner resumes the canonical customer-substrate operation.
// Implementations must correlate TenantID/OwnerID/IdempotencyKey/StackID/NodeID
// durably BEFORE submitting a VM. RunID and short-lived pairing material may
// change on a failed HTTP retry; neither is allocation identity. Existing guest
// operations must be observed instead of resubmitted. The adapter uses existing
// job/enrollment authority and returns only a real job correlation.
type WizardGuestProvisioner interface {
	Execute(*httpx.Event, WizardGuestProvisionRequest) (*WizardGuestProvisionResult, error)
}

type WizardGuestProvisionerFunc func(*httpx.Event, WizardGuestProvisionRequest) (*WizardGuestProvisionResult, error)

func (execute WizardGuestProvisionerFunc) Execute(e *httpx.Event, request WizardGuestProvisionRequest) (*WizardGuestProvisionResult, error) {
	return execute(e, request)
}

type WizardGuestProvisionRequest struct {
	TenantID, OwnerID, RunID, IdempotencyKey  string
	StackID, NodeID, StackKit, ReleaseVersion string
	Guest                                     WizardSubstrateParams
	Pairing                                   trust.MintedStackPairing
	OwnerSpecBootstrap                        *jobs.OwnerSpecBootstrap
}

type WizardGuestProvisionResult struct {
	JobID, ServerID, OperationID string
	Appliance                    *jobs.CustomerGuestBinding
}

func (h wizardRunHandlers) validateWizardGuestRequest(e *httpx.Event, request wizardRunRequest) bool {
	if wizardRunTransport(request) != specv2.TransportHypervisor {
		if wizardRequestsNewHAOS(request) {
			_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "A new HAOS appliance requires a hypervisor placement", nil)
			return false
		}
		if request.Substrate == nil {
			return true
		}
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Substrate selection requires the hypervisor transport", nil)
		return false
	}
	g := request.Substrate
	if g == nil || !substrate.IsUbuntuProfile(g.ProfileID) || strings.TrimSpace(g.ServerID) == "" || strings.TrimSpace(g.Storage) == "" || strings.TrimSpace(g.Bridge) == "" || g.CPU < 2 || g.MemoryMiB < 2048 || g.DiskGiB < 32 || request.Remote != nil || request.Managed != nil {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Select a connected hypervisor and valid Ubuntu guest resources", nil)
		return false
	}
	if wizardRequestsNewHAOS(request) != (g.Appliance != nil) {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Select separate appliance resources for a new HAOS installation", nil)
		return false
	}
	if a := g.Appliance; a != nil {
		valid := false
		for _, profile := range substrate.Profiles() {
			if profile.ID == "haos" && a.ProfileID == profile.ID && a.CPU >= profile.MinCPU && a.MemoryMiB >= profile.MinMemoryMiB && a.DiskGiB >= profile.MinDiskGiB && strings.TrimSpace(a.Storage) != "" && strings.TrimSpace(a.Bridge) != "" {
				valid = true
			}
		}
		if !valid {
			_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Select valid HAOS appliance resources", nil)
			return false
		}
	}
	if h.cfg.GuestProvisioner == nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Customer guest provisioning is unavailable", nil)
		return false
	}
	return true
}

// admitWizardGuestAccelerator decides from the pinned release catalog whether
// the selected use cases need a GPU and refuses a guest placement that names
// none, before any stack, pairing or guest exists. It runs on the effective
// intent, so inherited settings and capabilities count too.
func admitWizardGuestAccelerator(e *httpx.Event, request *wizardRunRequest) bool {
	guest := request.Substrate
	if guest == nil {
		return true
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"The StackKits use-case catalog could not be read", wizardRunDetails(
				"wizard_use_case_catalog_unavailable", true, "Try again", err.Error()))
		return false
	}
	intent := request.Intent
	guest.RequiresAccelerator = catalog.RequiresAccelerator(intent.Goals, intent.UseCaseSettings, intent.UseCaseCapabilities)
	if guest.RequiresAccelerator && guest.Accelerator == nil {
		_ = httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation,
			"The selected use case needs a GPU on the hypervisor", wizardRunDetails(
				"wizard_accelerator_required", false, "Select a GPU",
				"Pass the hypervisor's GPU to the guest, or choose settings that run without one."))
		return false
	}
	return true
}

func (h wizardRunHandlers) provisionWizardGuest(e *httpx.Event, run *wizardRunState, projection *specv2.Projection, stackID string, pairing trust.MintedStackPairing, ownerAccess ownerSpecBootstrapAccess) (*WizardGuestProvisionResult, bool) {
	result, err := h.cfg.GuestProvisioner.Execute(e, WizardGuestProvisionRequest{
		TenantID: run.tenantID, OwnerID: run.ownerID, RunID: run.runID, IdempotencyKey: run.key,
		StackID: stackID, NodeID: projection.NodeID, StackKit: wizardRunKitSlug(run.request, projection), ReleaseVersion: h.cfg.ReleaseVersion,
		Guest: *run.request.Substrate, Pairing: pairing, OwnerSpecBootstrap: ownerSpecRuntimeBootstrap(ownerAccess),
	})
	if err != nil || result == nil || result.JobID == "" {
		h.recordWizardRunFailure(e.Request.Context(), run, controlplane.WizardRun{StackID: stackID, NodeID: projection.NodeID, PairingJobID: pairing.JobID}, "guest_provision_failed")
		_ = httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Guest provisioning did not return a durable operation", wizardRunDetails("wizard_guest_provision_failed", true, "Guest provisioning interrupted", "Retry with the same Idempotency-Key to observe or resume the existing guest operation."))
		return nil, true
	}
	return result, false
}
