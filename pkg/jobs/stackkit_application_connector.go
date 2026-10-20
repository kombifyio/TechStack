package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/runtimehealth"
	"github.com/kombifyio/techstack/pkg/secrets"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
	"google.golang.org/protobuf/proto"
)

const StackKitLifecycleApplicationConnector = "application_connector"
const StackKitLifecycleApplicationConnectorVerify = "application_connector_verify"

// This is correlation metadata, not local signedApply or credential authority.
type ApplicationConnectorBinding struct {
	ReleasePin             *agentpb.StackKitReleasePin      `json:"release_pin"`
	Request                stackkitcommand.ConnectorRequest `json:"request"`
	ServerID               string                           `json:"server_id"`
	ServerGeneration       int64                            `json:"server_generation"`
	WorkerGeneration       int64                            `json:"worker_generation"`
	LeaseID                string                           `json:"lease_id"`
	ProviderRef            string                           `json:"provider_ref"`
	NodeID                 string                           `json:"node_id"`
	ExpectedIntentRevision int64                            `json:"expected_intent_revision"`
	OriginalOperationID    string                           `json:"original_operation_id,omitempty"`
	OriginalReceiptBase64  string                           `json:"original_receipt_base64,omitempty"`
	OriginalPlanHash       string                           `json:"original_plan_hash,omitempty"`
	ExpectedConnectorJobID string                           `json:"expected_connector_job_id,omitempty"`
}

func IsApplicationConnectorLifecycleOperation(op string) bool {
	return op == StackKitLifecycleApplicationConnector || op == StackKitLifecycleApplicationConnectorVerify
}

func validateApplicationConnectorLifecycleRequest(req *StackKitLifecycleRequest) error {
	if req.Connector == nil || req.Application != nil || req.Connector.ServerID == "" || req.Connector.ServerGeneration <= 0 || req.ServiceID == "" || req.ServiceKey != "smart-home" || req.StackKitInstanceID == "" || req.DurableJobID == "" || req.DryRun || req.Offline {
		return fmt.Errorf("connector requires exact retained service and node authority")
	}
	if b := req.Connector; (b.OriginalOperationID != "" || b.OriginalReceiptBase64 != "" || b.OriginalPlanHash != "") && (b.OriginalOperationID == "" || len(b.OriginalReceiptBase64) > 87384 || req.Operation != StackKitLifecycleApplicationConnectorVerify || b.OriginalOperationID == req.DurableJobID || b.OriginalReceiptBase64 == "" || b.OriginalPlanHash == "") {
		return fmt.Errorf("connector refresh requires retained original proof and distinct read identity")
	}
	candidate := req.Connector.Request
	if candidate.Connector.Binding.Node == (stackkitcommand.ConnectorNode{}) {
		candidate.Connector.Binding.Node = stackkitcommand.ConnectorNode{SiteRef: "pending", NodeRef: "pending", ExecutionChannelRef: "pending"}
	}
	raw, err := json.Marshal(candidate)
	if err != nil {
		return err
	}
	r, err := stackkitcommand.DecodeConnectorRequest(raw)
	if err != nil {
		return err
	}
	if r.Connector.Binding.ResourceID != req.ServiceID {
		return fmt.Errorf("connector resource differs from service")
	}
	if req.Operation == StackKitLifecycleApplicationConnector && !req.OwnerApproved {
		return fmt.Errorf("connector mutation requires Owner approval")
	}
	if req.Operation == StackKitLifecycleApplicationConnectorVerify && req.OwnerApproved {
		return fmt.Errorf("connector verification is read-only")
	}
	return nil
}

// ApplicationConnectorImageDigest consumes only the collector's immutable
// image reference. A mutable tag or an adoption flag cannot grant setup authority.
func ApplicationConnectorImageDigest(service *controlplane.ServiceRuntime) string {
	if service == nil {
		return ""
	}
	image, _ := service.Metadata["image"].(string)
	if _, digest, ok := strings.Cut(image, "@"); ok {
		return digest
	}
	if strings.HasPrefix(image, "sha256:") {
		return image
	}
	return ""
}

func connectorCommand(id string, req StackKitLifecycleRequest, release stackkitrelease.Release, planHash string) (*agentpb.StackKitCommand, error) {
	c, err := stackKitLifecycleCommand(id, req, release)
	if err != nil {
		return nil, err
	}
	if req.Connector.ReleasePin == nil || !proto.Equal(c.Release, req.Connector.ReleasePin) {
		return nil, fmt.Errorf("connector release pin changed")
	}
	c.ApplicationOperationId = connectorOperationID(req)
	c.ApplicationConnectorRequestJson, err = json.Marshal(req.Connector.Request)
	if err != nil {
		return nil, err
	}
	c.ExpectedPlanHash = planHash
	return c, stackkitcommand.ValidateCommand(c)
}

func connectorHead(ctx context.Context, cfg StackKitLifecycleConfig, req StackKitLifecycleRequest) (*controlplane.ServiceEventResult, error) {
	if cfg.Services == nil || cfg.ConnectorAuthorize == nil {
		return nil, fmt.Errorf("connector current authority unavailable")
	}
	if err := cfg.ConnectorAuthorize(ctx, req); err != nil {
		return nil, err
	}
	head, err := cfg.Services.ReadServiceEventHead(ctx, req.TenantID, req.ServiceID)
	if err != nil {
		return nil, err
	}
	b := req.Connector
	source := b.Request.Connector.Binding
	if head.Service.TenantID != req.TenantID || head.Service.StackID != req.StackID || head.Service.ServerID != b.ServerID || !declaredServiceControlMember(*head.Service, "smart-home") || ApplicationContainerIdentity(head.Service) != source.ContainerID || ApplicationConnectorImageDigest(head.Service) != source.ImageDigest || head.Service.MutationLock.Locked() {
		return nil, fmt.Errorf("connector service source or intent changed")
	}
	jobID, _ := head.Service.Metadata["application_connector_job_id"].(string)
	intent := ApplicationIntentRevision(head.Service)
	projectedRevision := connectorRevision(head.Service.Metadata["application_connector_projection_revision"])
	projection, _ := head.Service.Metadata["application_connector"].(map[string]any)
	ownProjection := jobID == connectorOperationID(req)
	if b.OriginalOperationID != "" {
		ownProjection = ownProjection && projection["verification_job_id"] == req.DurableJobID
	}
	if intent != b.ExpectedIntentRevision && (!ownProjection || projectedRevision <= 0 || intent != projectedRevision) {
		return nil, fmt.Errorf("connector owner intent changed")
	}

	if jobID != b.ExpectedConnectorJobID && jobID != connectorOperationID(req) {
		return nil, fmt.Errorf("connector projection superseded")
	}
	return head, nil
}

func connectorWait(cause error) error {
	return &JobWaitError{Reason: "waiting_application_connector_verification", Message: "Connector outcome is unresolved; its original operation is fenced pending current read-only verification", ResumeAfter: time.Minute, Cause: cause}
}

func runApplicationConnectorJob(ctx context.Context, cfg StackKitLifecycleConfig, job *Job, q *Queue, req StackKitLifecycleRequest, release stackkitrelease.Release) error {
	// Catalog qualification is mandatory even on restored jobs. There is no
	// sibling checkout or guessed minimum-release path in production.
	ctx = contextWithJobRequestAuthority(ctx, job)
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return NewConfigError(err)
	}
	for _, op := range []string{"application.connector", "application.connector.verify"} {
		if err = catalog.Dispatchable(op); err != nil {
			return NewConfigError(err)
		}
	}
	retained := job.Snapshot().Result
	if retained == nil {
		retained = map[string]interface{}{}
	}
	dispatched, _ := retained["connector_dispatch_intent"].(bool)
	head, err := connectorHead(ctx, cfg, req)
	if err != nil {
		if dispatched {
			return connectorWait(err)
		}
		return NewPermanentError(err)
	}

	syncer := q.executionSnapshotSyncer()
	if syncer == nil {
		return NewConfigError(fmt.Errorf("durable connector intent persistence unavailable"))
	}
	projection, _ := head.Service.Metadata["application_connector"].(map[string]any)
	if req.Connector.OriginalOperationID != "" && projection["verification_job_id"] == req.DurableJobID && retained["connector_result_proof"] != nil {
		retained["connector_outcome_unresolved"] = false
		job.replaceResult(retained)
		if err = syncer(ctx, job.Snapshot()); err != nil {
			return NewResourceError(err)
		}
		q.UpdateProgress(job.ID, 100, "Node connector verification already projected")
		return nil
	}
	planHash, _ := retained["connector_plan_hash"].(string)
	if req.Connector.OriginalOperationID != "" {
		planHash = req.Connector.OriginalPlanHash
	}
	effective := req
	id := job.ID
	if dispatched {
		effective.Operation = StackKitLifecycleApplicationConnectorVerify
		effective.OwnerApproved = false
		id = job.ID + "-read-" + fmt.Sprint(time.Now().UnixNano())
	}
	var result agentpb.StackKitResult
	proof := retained["connector_result_proof"]
	if proof != nil && dispatched && req.Connector.OriginalOperationID != "" {
		encoded, _ := json.Marshal(proof)
		var previous agentpb.StackKitResult
		if json.Unmarshal(encoded, &previous) != nil {
			return NewPermanentError(fmt.Errorf("retained connector proof is invalid"))
		}
		// A stale pre-CAS read cannot refresh availability. Keep its bytes until a
		// distinct current read replaces them; never reissue the original setup.
		if time.Since(time.Unix(previous.FinishedAtUnix, 0)) > runtimehealth.FreshHeartbeatWindow {
			proof = nil
		}
	}
	if proof != nil {
		encoded, _ := json.Marshal(proof)
		if err = json.Unmarshal(encoded, &result); err != nil {
			return NewPermanentError(err)
		}
		if verified, _ := retained["connector_proof_read_only"].(bool); verified {
			effective.Operation = StackKitLifecycleApplicationConnectorVerify
			effective.OwnerApproved = false
		}
		id = result.CommandId
	} else {
		if !dispatched && req.Operation == StackKitLifecycleApplicationConnector {
			if head.Revision != req.Connector.Request.Connector.Binding.ServiceRevision {
				return NewPermanentError(fmt.Errorf("connector review is stale"))
			}
			if cfg.ApplicationAuthorize == nil {
				return NewConfigError(fmt.Errorf("connector operate authority unavailable"))
			}
			if err = cfg.ApplicationAuthorize(contextWithJobRequestAuthority(ctx, job), req.TenantID, req.OwnerID, req.Connector.ServerID); err != nil {
				return NewPermanentError(err)
			}
			// Read the current node plan. The CLI independently requires its own
			// signedApply; a plan or managed flag alone cannot authorize setup.
			planReq := req
			planReq.Connector = nil
			planReq.ServiceKey = ""
			planReq.ServiceID = ""
			planReq.DurableJobID = ""
			planReq.ServiceActionDigest = ""
			planHash, err = planStackKitLifecycleApply(ctx, cfg.Sender, job.ID, planReq, release)
			if err != nil {
				return NewResourceError(err)
			}
			head, err = connectorHead(ctx, cfg, req)
			if err != nil {
				return NewPermanentError(err)
			}
			if head.Revision != req.Connector.Request.Connector.Binding.ServiceRevision {
				return NewPermanentError(fmt.Errorf("connector source changed during plan admission"))
			}
			if err = cfg.ApplicationAuthorize(contextWithJobRequestAuthority(ctx, job), req.TenantID, req.OwnerID, req.Connector.ServerID); err != nil {
				return NewPermanentError(err)
			}
			if _, err = connectorCommand(id, effective, release, planHash); err != nil {
				return NewPermanentError(err)
			}
			retained["connector_plan_hash"] = planHash
			retained["connector_dispatch_intent"] = true
			retained["connector_outcome_unresolved"] = true
			job.replaceResult(retained)
			if err = syncer(ctx, job.Snapshot()); err != nil {
				return NewResourceError(err)
			}
			dispatched = true
		}
		command, commandErr := connectorCommand(id, effective, release, planHash)
		if commandErr != nil {
			return NewPermanentError(commandErr)
		}
		// Read jobs also retain their intent before any host command.
		if !dispatched {
			retained["connector_plan_hash"] = planHash
			retained["connector_dispatch_intent"] = true
			retained["connector_outcome_unresolved"] = true
			job.replaceResult(retained)
			if err = syncer(ctx, job.Snapshot()); err != nil {
				return NewResourceError(err)
			}
			dispatched = true
		}
		response, sendErr := sendStackKitCommandBoundedForTenant(ctx, cfg.Sender, req.TenantID, req.AgentID, command)
		if sendErr != nil {
			if dispatched {
				return connectorWait(sendErr)
			}
			return NewResourceError(sendErr)
		}
		if response == nil {
			return connectorWait(fmt.Errorf("connector result unavailable"))
		}
		proto.Reset(&result)
		proto.Merge(&result, response)
	}
	command, err := connectorCommand(id, effective, release, planHash)
	if err != nil {
		return NewPermanentError(err)
	}
	if err = stackkitcommand.ValidateResult(&result, command); err != nil {
		return connectorWait(err)
	}

	if !result.Success {
		var refusal struct {
			Data struct {
				SchemaVersion string   `json:"schemaVersion"`
				ReasonCode    string   `json:"reasonCode"`
				Message       string   `json:"message"`
				UserGuidance  []string `json:"userGuidance"`
			} `json:"data"`
		}
		if json.Unmarshal(result.CommandResultJson, &refusal) == nil && refusal.Data.SchemaVersion == "stackkit.actionable-error/v1" && len(refusal.Data.Message) <= 2048 && len(refusal.Data.ReasonCode) <= 128 {
			guidance := []string{}
			for _, value := range refusal.Data.UserGuidance {
				if len(value) <= 2048 && len(guidance) < 8 {
					guidance = append(guidance, secrets.Redact(value))
				}
			}
			retained["connector_refusal"] = map[string]any{"reason_code": refusal.Data.ReasonCode, "message": secrets.Redact(refusal.Data.Message), "user_guidance": guidance}
			job.replaceResult(retained)
			if err = syncer(ctx, job.Snapshot()); err != nil {
				return NewResourceError(err)
			}
		}
		return connectorWait(fmt.Errorf("pinned CLI did not verify connector outcome; review retained refusal guidance"))
	}
	evidence, receipt, err := stackkitcommand.ParseApplicationConnectorEvidence(command, &result)
	if err != nil {
		return connectorWait(err)
	}
	if req.Connector.OriginalOperationID != "" {
		if evidence.ReceiptBase64 != req.Connector.OriginalReceiptBase64 || result.FinishedAtUnix < job.CreatedAt.Unix() || result.FinishedAtUnix > time.Now().Add(time.Minute).Unix() || time.Since(time.Unix(result.FinishedAtUnix, 0)) > runtimehealth.FreshHeartbeatWindow {
			return connectorWait(fmt.Errorf("connector refresh lacks fresh exact original receipt evidence"))
		}
	}
	// Retain exact attested bytes before the aggregate CAS; restoration never
	// reissues the mutation. Projection contains no credential or secretRef.
	retained["connector_result_proof"] = &result
	retained["connector_proof_read_only"] = effective.Operation == StackKitLifecycleApplicationConnectorVerify
	job.replaceResult(retained)
	if err = syncer(ctx, job.Snapshot()); err != nil {
		return NewResourceError(err)
	}
	head, err = connectorHead(ctx, cfg, req)
	if err != nil {
		return connectorWait(err)
	}
	projection, _ = head.Service.Metadata["application_connector"].(map[string]any)
	if head.Service.Metadata["application_connector_job_id"] != connectorOperationID(req) || (req.Connector.OriginalOperationID != "" && projection["verification_job_id"] != req.DurableJobID) {
		metadata := head.Service.Metadata
		metadata["application_connector_job_id"] = connectorOperationID(req)
		metadata["application_connector_projection_revision"] = head.Revision + 1
		metadata["application_connector"] = map[string]any{"status": receipt.Result.HomeAssistantConnector.Issuer.Status, "receipt_digest": evidence.ReceiptDigest, "operation_id": connectorOperationID(req), "verification_job_id": job.ID, "application_version": receipt.Result.ApplicationVersion, "container_id": req.Connector.Request.Connector.Binding.ContainerID, "image_digest": req.Connector.Request.Connector.Binding.ImageDigest, "verified_at": time.Unix(result.FinishedAtUnix, 0).UTC(), "server_generation": req.Connector.ServerGeneration, "worker_generation": req.Connector.WorkerGeneration, "node_id": req.Connector.NodeID, "lease_id": req.Connector.LeaseID, "provider_ref": req.Connector.ProviderRef, "expires_at": receipt.Result.HomeAssistantConnector.Issuer.ExpiresAt}
		_, err = cfg.Services.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: req.TenantID, ServiceID: req.ServiceID, ExpectedRevision: &head.Revision, Authority: controlplane.ServiceEventAuthorityControlPlane, Source: "stackkit-application-connector", ReasonCode: req.Operation, EnforceIdentityBinding: true, Runtime: controlplane.ServiceRuntime{Metadata: metadata}, Evidence: map[string]any{"job_id": job.ID, "receipt_digest": evidence.ReceiptDigest}})
		if err != nil {
			return connectorWait(err)
		}
	}
	retained["connector_outcome_unresolved"] = false
	job.replaceResult(retained)
	if err = syncer(ctx, job.Snapshot()); err != nil {
		return NewResourceError(err)
	}
	q.UpdateProgress(job.ID, 100, "Node connector receipt verified and projected")
	return nil
}

func ApplicationConnectorRelease() (*agentpb.StackKitReleasePin, error) {
	release, err := configuredTargetStackKitRelease()
	return applicationConnectorReleasePin(release, err)
}

// CachedApplicationConnectorAvailable is the read-path gate of
// ApplicationConnectorRelease: it reuses the cached release admission, so the
// per-inventory reconcile does not digest the StackKits bundle each time. An
// enqueued refresh still admits the release afresh.
func CachedApplicationConnectorAvailable() error {
	release, err := CachedTargetStackKitRelease()
	_, err = applicationConnectorReleasePin(release, err)
	return err
}

func applicationConnectorReleasePin(release *stackkitrelease.Release, err error) (*agentpb.StackKitReleasePin, error) {
	if err != nil {
		return nil, err
	}
	if release == nil {
		return nil, fmt.Errorf("published connector release unavailable")
	}
	catalog, err := release.AdvancedOperations()
	if err != nil {
		return nil, err
	}
	for _, op := range []string{"application.connector", "application.connector.verify"} {
		if err = catalog.Dispatchable(op); err != nil {
			return nil, err
		}
	}
	return grpcserver.StackKitReleasePinFor(*release), nil
}

// BindApplicationConnectorNode uses the already-admitted worker's node and the
// existing kit placement authority; public requests cannot choose a channel.
func BindApplicationConnectorNode(req StackKitLifecycleRequest) (StackKitLifecycleRequest, error) {
	b, err := localExecutionBindingForNode(req.StackKit, req.NodeRef)
	if err != nil {
		return req, err
	}
	req.Connector.Request.Connector.Binding.Node = stackkitcommand.ConnectorNode{SiteRef: b.SiteRef, NodeRef: b.NodeRef, ExecutionChannelRef: b.ExecutionChannelRef}
	return req, nil
}

func connectorRevision(value any) int64 {
	raw, _ := json.Marshal(value)
	var result int64
	_ = json.Unmarshal(raw, &result)
	return result
}

func connectorOperationID(req StackKitLifecycleRequest) string {
	if req.Connector != nil && req.Connector.OriginalOperationID != "" {
		return req.Connector.OriginalOperationID
	}
	return req.DurableJobID
}

// ApplicationConnectorRefreshRequest builds only from the immutable completed
// setup and its current canonical projection. It never grants local authority.
func ApplicationConnectorRefreshRequest(original *controlplane.Job, service *controlplane.ServiceRuntime) (StackKitLifecycleRequest, error) {
	release, err := configuredTargetStackKitRelease()
	if err != nil || release == nil {
		return StackKitLifecycleRequest{}, fmt.Errorf("published connector release unavailable")
	}
	return applicationConnectorRefreshRequest(original, service, *release)
}

func applicationConnectorRefreshRequest(original *controlplane.Job, service *controlplane.ServiceRuntime, release stackkitrelease.Release) (StackKitLifecycleRequest, error) {
	var empty StackKitLifecycleRequest
	if original == nil || service == nil || original.State != "completed" || original.TenantID != service.TenantID || original.StackID != service.StackID || service.MutationLock.Locked() {
		return empty, fmt.Errorf("completed connector setup unavailable")
	}
	req, err := stackKitLifecycleRequestFromJob(&Job{ID: original.ID, TargetID: original.StackID, Payload: original.Payload})
	if err != nil || req.Connector == nil || req.Operation != StackKitLifecycleApplicationConnector || req.DurableJobID != original.ID || req.ServiceID != service.ID || req.Connector.Request.Connector.Action != "ensure" {
		return empty, fmt.Errorf("original owned connector setup unavailable")
	}
	projection, _ := service.Metadata["application_connector"].(map[string]any)
	intent := ApplicationIntentRevision(service)
	if service.Metadata["application_connector_job_id"] != original.ID || projection["status"] != "active" || intent <= 0 || intent != connectorRevision(service.Metadata["application_connector_projection_revision"]) || !declaredServiceControlMember(*service, "smart-home") || service.ServerID != req.Connector.ServerID || ApplicationContainerIdentity(service) != req.Connector.Request.Connector.Binding.ContainerID || ApplicationConnectorImageDigest(service) != req.Connector.Request.Connector.Binding.ImageDigest {
		return empty, fmt.Errorf("connector setup projection or owner intent changed")
	}
	planHash, _ := original.Result["connector_plan_hash"].(string)
	var proof agentpb.StackKitResult
	raw, _ := json.Marshal(original.Result["connector_result_proof"])
	if json.Unmarshal(raw, &proof) != nil {
		return empty, fmt.Errorf("original connector proof unavailable")
	}
	proofReq := req
	if read, _ := original.Result["connector_proof_read_only"].(bool); read {
		proofReq.Operation = StackKitLifecycleApplicationConnectorVerify
		proofReq.OwnerApproved = false
	}
	command, err := connectorCommand(proof.CommandId, proofReq, release, planHash)
	if err != nil {
		return empty, err
	}
	if err = stackkitcommand.ValidateResult(&proof, command); err != nil {
		return empty, err
	}
	evidence, _, err := stackkitcommand.ParseApplicationConnectorEvidence(command, &proof)
	if err != nil || projection["receipt_digest"] != evidence.ReceiptDigest {
		return empty, fmt.Errorf("retained setup proof differs from canonical projection")
	}
	req.Operation, req.OwnerApproved = StackKitLifecycleApplicationConnectorVerify, false
	req.Connector.OriginalOperationID = original.ID
	req.Connector.OriginalReceiptBase64 = evidence.ReceiptBase64
	req.Connector.OriginalPlanHash = planHash
	req.Connector.ExpectedIntentRevision = intent
	req.Connector.ExpectedConnectorJobID = original.ID
	req.DurableJobID = StackKitServiceActionJobID(req.TenantID, req.OwnerID, fmt.Sprintf("connector-read:%s:%d", original.ID, intent))
	fingerprint := sha256.Sum256([]byte(fmt.Sprintf("%s:%s:%d", original.ID, evidence.ReceiptDigest, intent)))
	req.ServiceActionDigest = hex.EncodeToString(fingerprint[:])
	return NormalizeStackKitLifecycleRequest(req)
}
