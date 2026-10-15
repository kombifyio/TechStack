package jobs

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

const (
	StackKitLifecycleApplicationInspect = "application_inspect"
	StackKitLifecycleApplicationAdopt   = "application_adopt"
	StackKitLifecycleApplicationVerify  = "application_verify"
	StackKitLifecycleApplicationRelease = "application_release"
	StackKitLifecycleApplicationControl = "application_control"
)

type ApplicationLifecycleBinding struct {
	ReleasePin              *agentpb.StackKitReleasePin          `json:"release_pin"`
	ContainerID             string                               `json:"container_id"`
	ServerID                string                               `json:"server_id"`
	ExpectedServiceRevision int64                                `json:"expected_service_revision"`
	ExpectedIntentRevision  int64                                `json:"expected_intent_revision"`
	ExpectedManagementState string                               `json:"expected_management_state"`
	ExpectedDesiredState    string                               `json:"expected_desired_state"`
	ExpectedAdoptionJobID   string                               `json:"expected_adoption_job_id,omitempty"`
	PlanJobID               string                               `json:"plan_job_id,omitempty"`
	ReviewedPlan            *stackkitcommand.ApplicationEvidence `json:"reviewed_plan,omitempty"`
	Action                  string                               `json:"action,omitempty"`
}

func applicationAgentOperation(op string) (agentpb.StackKitOperation, bool) {
	m := map[string]agentpb.StackKitOperation{StackKitLifecycleApplicationInspect: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_INSPECT, StackKitLifecycleApplicationAdopt: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_ADOPT, StackKitLifecycleApplicationVerify: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_VERIFY, StackKitLifecycleApplicationRelease: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_RELEASE, StackKitLifecycleApplicationControl: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONTROL}
	v, ok := m[op]
	return v, ok
}
func IsApplicationLifecycleOperation(op string) bool {
	_, ok := applicationAgentOperation(op)
	return ok
}
func validateApplicationLifecycleRequest(req *StackKitLifecycleRequest) error {
	if req.Application == nil || req.Application.ServerID == "" || req.Application.ExpectedServiceRevision <= 0 || req.ServiceID == "" || req.ServiceKey != "smart-home" {
		return fmt.Errorf("application lifecycle requires a retained exact service/source binding")
	}
	if req.Operation == StackKitLifecycleApplicationAdopt && (req.Application.ReviewedPlan == nil || req.Application.PlanJobID == "") {
		return fmt.Errorf("application adopt requires the retained reviewed plan")
	}
	if req.Operation != StackKitLifecycleApplicationInspect && req.Operation != StackKitLifecycleApplicationVerify && !req.OwnerApproved {
		return fmt.Errorf("application mutation requires Owner approval")
	}
	return nil
}
func applicationCommand(req StackKitLifecycleRequest, release stackkitrelease.Release) (*agentpb.StackKitCommand, error) {
	c, err := stackKitLifecycleCommand(req.DurableJobID, req, release)
	if err != nil {
		return nil, err
	}
	c.ApplicationContainerId = req.Application.ContainerID
	if req.Operation != StackKitLifecycleApplicationInspect && req.Operation != StackKitLifecycleApplicationVerify {
		c.ApplicationOperationId = req.DurableJobID
	}
	if req.Operation == StackKitLifecycleApplicationAdopt {
		c.ApplicationPlanDigest = req.Application.ReviewedPlan.PlanDigest
	}
	c.ApplicationAction = req.Application.Action
	if req.Application.ReleasePin == nil || !proto.Equal(c.Release, req.Application.ReleasePin) {
		return nil, fmt.Errorf("reviewed application release pin changed")
	}
	return c, stackkitcommand.ValidateCommand(c)
}
func runApplicationLifecycleJob(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release) error {
	if cfg.Services == nil {
		return NewConfigError(fmt.Errorf("application aggregate authority is unavailable"))
	}
	head, err := cfg.Services.ReadServiceEventHead(ctx, req.TenantID, req.ServiceID)
	if err != nil {
		return NewPermanentError(err)
	}
	if head.Service.StackID != req.StackID || head.Service.ServerID != req.Application.ServerID || ApplicationContainerIdentity(head.Service) != req.Application.ContainerID {
		return NewPermanentError(fmt.Errorf("application source identity changed"))
	}
	if head.Service.Metadata["application_adoption_job_id"] == job.ID {
		return nil
	}
	if head.Service.MutationLock.Locked() && req.Operation != StackKitLifecycleApplicationInspect && req.Operation != StackKitLifecycleApplicationVerify {
		return NewPermanentError(fmt.Errorf("application is locked"))
	}
	command, err := applicationCommand(req, release)
	if err != nil {
		return NewPermanentError(err)
	}
	// If a receipt was durably stored before projection failed, reconcile it
	// without repeating the host mutation. Otherwise the CLI journals operation ID.
	var evidence stackkitcommand.ApplicationEvidence
	if raw := job.Snapshot().Result["application_result_proof"]; raw != nil {
		encoded, _ := json.Marshal(raw)
		var result agentpb.StackKitResult
		if err = json.Unmarshal(encoded, &result); err != nil {
			return NewPermanentError(err)
		}
		if err = stackkitcommand.ValidateResult(&result, command); err != nil {
			return NewPermanentError(err)
		}
		evidence, err = stackkitcommand.ParseApplicationEvidence(command, &result)
		if err != nil {
			return NewPermanentError(err)
		}
	} else {
		if req.Operation != StackKitLifecycleApplicationInspect && req.Operation != StackKitLifecycleApplicationVerify {
			if cfg.ApplicationAuthorize == nil {
				return NewConfigError(fmt.Errorf("application operate authority is unavailable"))
			}
			if err := cfg.ApplicationAuthorize(contextWithJobRequestAuthority(ctx, job), req.TenantID, req.OwnerID, req.Application.ServerID); err != nil {
				return NewPermanentError(fmt.Errorf("application operate authority denied: %w", err))
			}
		}
		if head.Revision != req.Application.ExpectedServiceRevision {
			return NewPermanentError(fmt.Errorf("application review revision is stale"))
		}
		result, sendErr := sendStackKitCommandBoundedForTenant(ctx, cfg.Sender, req.TenantID, req.AgentID, command)
		if sendErr != nil {
			return NewResourceError(sendErr)
		}
		if err = stackkitcommand.ValidateResult(result, command); err != nil {
			return NewPermanentError(err)
		}
		if !result.Success {
			normalized, normalizeErr := normalizeStackKitLifecycleResult(req, result)
			if normalizeErr != nil {
				return NewPermanentError(normalizeErr)
			}
			job.replaceResult(normalized)
			var refusal struct {
				Data struct {
					SchemaVersion string   `json:"schemaVersion"`
					ReasonCode    string   `json:"reasonCode"`
					Message       string   `json:"message"`
					UserGuidance  []string `json:"userGuidance"`
				} `json:"data"`
			}
			_ = json.Unmarshal(result.CommandResultJson, &refusal)
			if message := refusal.Data.Message; refusal.Data.SchemaVersion == "stackkit.actionable-error/v1" && message != "" && len(message) <= 2048 {
				for _, guidance := range refusal.Data.UserGuidance {
					if len(message)+len(guidance) > 4096 {
						break
					}
					message += " " + guidance
				}
				return NewPermanentError(fmt.Errorf("application lifecycle refused (%s): %s", refusal.Data.ReasonCode, message))
			}
			return NewPermanentError(fmt.Errorf("application lifecycle refused; review the operation evidence and authorize this exact container through its local StackKits Owner before preparing a new assessment"))
		}
		evidence, err = stackkitcommand.ParseApplicationEvidence(command, result)
		if err != nil {
			return NewPermanentError(err)
		}
		if req.Application.ReviewedPlan != nil && req.Operation == StackKitLifecycleApplicationAdopt && !stackkitcommand.SameApplicationSource(*req.Application.ReviewedPlan, evidence) {
			return NewPermanentError(fmt.Errorf("application result changed approved source or preservation contract"))
		}
		normalized, err := normalizeStackKitLifecycleResult(req, result)
		if err != nil {
			return NewPermanentError(err)
		}
		normalized["application_evidence"] = stackkitcommand.ApplicationEvidenceMap(evidence)
		normalized["application_binding"] = req.Application
		// Preserve exact attested bytes rather than regenerating JSON from the
		// read projection. Reconciliation validates this same authenticated proof.
		normalized["application_result_proof"] = result
		job.replaceResult(normalized)
		syncer := q.executionSnapshotSyncer()
		if syncer == nil {
			return NewConfigError(fmt.Errorf("durable application receipt persistence is unavailable"))
		}
		if err = syncer(ctx, job.Snapshot()); err != nil {
			return NewResourceError(err)
		}
	}
	if req.Application.ReviewedPlan != nil && req.Operation == StackKitLifecycleApplicationAdopt && !stackkitcommand.SameApplicationSource(*req.Application.ReviewedPlan, evidence) {
		return NewPermanentError(fmt.Errorf("application durable receipt differs from approved source"))
	}
	if req.Operation == StackKitLifecycleApplicationInspect {
		return nil
	}
	head, err = cfg.Services.ReadServiceEventHead(ctx, req.TenantID, req.ServiceID)
	if err != nil {
		return NewPermanentError(err)
	}
	if head.Service.StackID != req.StackID || head.Service.ServerID != req.Application.ServerID || ApplicationContainerIdentity(head.Service) != req.Application.ContainerID {
		return NewPermanentError(fmt.Errorf("application source changed before receipt projection"))
	}
	if head.Service.Metadata["application_adoption_job_id"] == job.ID {
		return nil
	}
	if !applicationProjectionFence(head, req.Application) {
		return NewPermanentError(fmt.Errorf("application receipt retained but newer service intent requires review before projection"))
	}
	metadata := head.Service.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["application_adoption"] = stackkitcommand.ApplicationEvidenceMap(evidence)
	metadata["application_adoption_job_id"] = job.ID
	management := "managed"
	var capabilities []string
	if req.Operation == StackKitLifecycleApplicationRelease {
		management = "observed"
		capabilities = []string{}
	} else {
		for _, op := range evidence.AdmittedOperations {
			if op == "start" || op == "stop" || op == "restart" {
				capabilities = append(capabilities, op)
			}
		}
	}
	patch := controlplane.ServiceRuntime{ManagementState: management, Metadata: metadata, Capabilities: capabilities}
	if req.Operation == StackKitLifecycleApplicationAdopt {
		patch.DesiredState = head.Service.ObservedState
	}
	if req.Operation == StackKitLifecycleApplicationControl {
		patch.DesiredState = evidence.PowerState
	}
	// Preserve measured runtime dimensions: adoption and release assert ownership,
	// not health. The next Guard observation supplies current measured state.
	_, err = cfg.Services.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: req.TenantID, ServiceID: req.ServiceID, ExpectedRevision: &head.Revision, Authority: controlplane.ServiceEventAuthorityControlPlane, Source: "stackkit-application-adoption", ReasonCode: req.Operation, EnforceIdentityBinding: true, Runtime: patch, Evidence: map[string]any{"job_id": job.ID, "plan_digest": evidence.PlanDigest, "receipt": stackkitcommand.ApplicationEvidenceMap(evidence)}})
	if err != nil {
		return NewResourceError(err)
	}
	q.UpdateProgress(job.ID, 100, "Application lifecycle receipt verified and projected")
	return nil
}

// ApplicationIntentRevision is stamped by the existing aggregate writer for
// control-plane changes. Guard samples retain it only for the same exact source.
func ApplicationIntentRevision(service *controlplane.ServiceRuntime) int64 {
	if service == nil {
		return 0
	}
	data, _ := json.Marshal(service.Metadata["application_intent_revision"])
	var revision int64
	_ = json.Unmarshal(data, &revision)
	return revision
}

func applicationProjectionFence(head *controlplane.ServiceEventResult, binding *ApplicationLifecycleBinding) bool {
	if head.Service.MutationLock.Locked() || head.Revision < binding.ExpectedServiceRevision {
		return false
	}
	// Older retained requests cannot infer a semantic baseline.
	if binding.ExpectedManagementState == "" {
		return head.Revision == binding.ExpectedServiceRevision
	}
	jobID, _ := head.Service.Metadata["application_adoption_job_id"].(string)
	return ApplicationIntentRevision(head.Service) == binding.ExpectedIntentRevision &&
		head.Service.ManagementState == binding.ExpectedManagementState &&
		head.Service.DesiredState == binding.ExpectedDesiredState && jobID == binding.ExpectedAdoptionJobID
}

// ApplicationContainerIdentity consumes the exact collector identity. Names,
// images, ports and project labels never stand in for a full container ID.
func ApplicationContainerIdentity(service *controlplane.ServiceRuntime) string {
	if service == nil {
		return ""
	}
	return controlplane.ServiceContainerIdentity(*service)
}

// ApplicationAdoptionRelease admits only the exact producer release catalog.
// A pre-adoption release cannot advertise executable adoption.
func ApplicationAdoptionRelease() (*agentpb.StackKitReleasePin, error) {
	return applicationAdoptionPin(configuredTargetStackKitRelease)
}

// CachedApplicationAdoptionRelease answers the same question for read
// projections from CachedTargetStackKitRelease; execution admits afresh.
func CachedApplicationAdoptionRelease() (*agentpb.StackKitReleasePin, error) {
	return applicationAdoptionPin(CachedTargetStackKitRelease)
}

func applicationAdoptionPin(resolve func() (*stackkitrelease.Release, error)) (*agentpb.StackKitReleasePin, error) {
	release, err := resolve()
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("published StackKits application release is unavailable")
	}
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"application.inspect", "application.adopt", "application.verify", "application.release", "application.control"} {
		if err = catalog.Dispatchable(op); err != nil {
			return nil, err
		}
	}
	return grpcserver.StackKitReleasePinFor(*release), nil
}
