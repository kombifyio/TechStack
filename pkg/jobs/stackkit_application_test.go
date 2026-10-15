package jobs

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/middleware"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// A receipt saved before aggregate projection must reconcile without host
// replay, permit routine observations, and never overwrite later owner intent.
func TestApplicationDurableReceiptReconciliationFencesOwnerIntent(t *testing.T) {
	for _, later := range []string{"observation", "projected", "release", "lock-unlock", "fresh-missing", "fresh-denied", "fresh-restored", "fresh-authorized"} {
		t.Run(later, func(t *testing.T) {
			ctx := t.Context()
			store := controlplane.NewMemoryStore()
			release := releaseWithAdvancedCatalog(t)
			id := strings.Repeat("a", 64)
			digest := "sha256:" + strings.Repeat("b", 64)
			_, err := store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{ID: "service-1", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "docker/ha", Source: "observed", ManagementState: "observed", ObservedState: "running", Metadata: map[string]any{"container_id": id}})
			if err != nil {
				t.Fatal(err)
			}
			initial, err := store.ReadServiceEventHead(ctx, "tenant-1", "service-1")
			if err != nil {
				t.Fatal(err)
			}
			evidence := stackkitcommand.ApplicationEvidence{APIVersion: "stackkit.application-adoption-result/v1", Authority: stackkitcommand.ApplicationAuthority{PlanHash: digest, LifecycleContractHash: digest, LifecycleVersion: "v1", PackageRef: "smart-home@v1"}, WorkloadRef: "smart-home", ProfileRef: stackkitcommand.HomeAssistantAdoptionProfile, Mode: "adopt_in_place", Source: stackkitcommand.ApplicationSource{ContainerID: id, ImageID: digest, ImageDigest: digest, Revision: digest, ConfigMount: "/srv/ha", ConfigurationDigest: digest, AccountsDigest: digest}, PlanDigest: digest, OperationID: "job-adopt-1", OwnedFields: []string{"container.power-state"}, PreservedFields: []string{"accounts", "configuration", "persistentData"}, AdmittedOperations: []string{"verify", "start", "stop", "restart", "release"}, VerifiedOwnerRef: "owner/local/ha", Status: "verified", VerifiedAt: time.Now().UTC()}
			binding := &ApplicationLifecycleBinding{ContainerID: id, ServerID: "server-1", ExpectedServiceRevision: initial.Revision, ExpectedIntentRevision: ApplicationIntentRevision(initial.Service), ExpectedManagementState: initial.Service.ManagementState, ExpectedDesiredState: initial.Service.DesiredState, ReleasePin: grpcserver.StackKitReleasePinFor(release), PlanJobID: "job-inspect-1", ReviewedPlan: &evidence}
			req := StackKitLifecycleRequest{TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1", AgentID: "agent-1", StackKit: "basement-kit", StackKitInstanceID: "ha-main", ServiceID: "service-1", ServiceKey: "smart-home", WorkingDirectory: "/opt/stackkit", Operation: StackKitLifecycleApplicationAdopt, DurableJobID: evidence.OperationID, OwnerApproved: true, Application: binding}
			command, err := applicationCommand(req, release)
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(map[string]any{"schemaVersion": stackkitcommand.CommandResultVersion, "command": stackkitcommand.ResultCommandName(command.Operation), "status": "success", "data": map[string]any{"result": evidence, "signature": map[string]any{"ownerRef": "owner/local/ha", "keyId": "key-1", "value": "signed"}}})
			hash := sha256.Sum256(raw)
			result := &agentpb.StackKitResult{CommandId: command.CommandId, Release: command.Release, Success: true, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion, EventsSchemaVersion: stackkitcommand.RolloutEventVersion, CommandResultJson: raw, PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: "sha256:" + hex.EncodeToString(hash[:])}
			job := &Job{ID: evidence.OperationID, Result: map[string]any{"application_result_proof": result}}
			sender := &applicationAuthoritySender{result: result}
			cfg := StackKitLifecycleConfig{Services: store, Sender: sender}
			queue := NewQueue(0, nil)
			queue.SetExecutionSnapshotSyncer(func(context.Context, JobSnapshot) error { return nil })
			fresh := strings.HasPrefix(later, "fresh-")
			if fresh {
				job.Payload = StackKitLifecyclePayload(req)
				job.Payload["tenant_id"], job.Payload["owner_id"] = req.TenantID, req.OwnerID
				job.Result = nil
				requestCtx := identity.NewContext(ctx, &identity.Identity{UserID: req.OwnerID, OrgID: req.TenantID})
				if later != "fresh-missing" {
					requestCtx = middleware.WithSignedEntitlements(requestCtx, "techstack.inventory.operate")
				}
				CaptureRequestOperateAuthority(requestCtx, job, req.TenantID, req.OwnerID)
				if later == "fresh-restored" {
					// A durable restore carries payload and result, never the verified
					// in-process request authority. Forged payload grants cannot replace it.
					job = &Job{ID: job.ID, Payload: job.Payload, Result: map[string]any{"entitlements": []string{"techstack.inventory.operate"}}}
				}
				cfg.ApplicationAuthorize = func(authorityCtx context.Context, tenant, owner, server string) error {
					grants, _, ok := middleware.AuthorizedEntitlementsFromContext(authorityCtx)
					if !ok || !grants.Has("techstack.inventory.operate") || later == "fresh-denied" || tenant != req.TenantID || owner != req.OwnerID || server != req.Application.ServerID {
						return errors.New("operate authority denied")
					}
					return nil
				}
			}
			event := controlplane.ServiceEvent{TenantID: req.TenantID, ServiceID: req.ServiceID, Authority: controlplane.ServiceEventAuthorityGuard, Runtime: controlplane.ServiceRuntime{ObservedState: "stopped", Metadata: map[string]any{"container_id": id}}}
			if later == "release" || later == "projected" {
				event.Authority = controlplane.ServiceEventAuthorityControlPlane
				event.Runtime.ManagementState = "observed"
				event.Runtime.Metadata["application_adoption_job_id"] = "job-release-2"
				if later == "projected" {
					event.Runtime.ManagementState = "managed"
					event.Runtime.Metadata["application_adoption_job_id"] = job.ID
				}
			}
			if later == "lock-unlock" {
				event.Authority = controlplane.ServiceEventAuthorityControlPlane
				event.Runtime.MutationLock = controlplane.ServiceMutationLock{State: serviceregistry.MutationLocked}
			}
			if event.Authority == controlplane.ServiceEventAuthorityControlPlane {
				event.Runtime.ObservedState = ""
			}
			if !fresh {
				_, err = store.ApplyServiceEvent(ctx, event)
				if err != nil {
					t.Fatal(err)
				}
			}
			if later == "lock-unlock" {
				event.Runtime.MutationLock.State = serviceregistry.MutationUnlocked
				_, err = store.ApplyServiceEvent(ctx, event)
				if err != nil {
					t.Fatal(err)
				}
			}
			before, _ := store.ReadServiceEventHead(ctx, req.TenantID, req.ServiceID)
			err = runApplicationLifecycleJob(ctx, cfg, job, queue, req, release)
			after, _ := store.ReadServiceEventHead(ctx, req.TenantID, req.ServiceID)
			if fresh {
				allowed := later == "fresh-authorized"
				if sender.sent != allowed {
					t.Fatalf("host dispatch authorized=%v, sent=%v", allowed, sender.sent)
				}
				if allowed {
					if err != nil || after.Service.ManagementState != "managed" {
						t.Fatalf("authorized adoption did not project: %v", err)
					}
				} else if err == nil || after.Revision != before.Revision || after.Service.ManagementState != "observed" {
					t.Fatal("unauthorized mutation changed the service aggregate")
				}
			} else if later == "observation" {
				if err != nil {
					t.Fatal(err)
				}
				if after.Service.ManagementState != "managed" || after.Service.Metadata["application_adoption_job_id"] != job.ID {
					t.Fatal("saved receipt was not reconciled after observation")
				}
			} else {
				if later == "projected" && err != nil {
					t.Fatal(err)
				}
				if later != "projected" && err == nil {
					t.Fatal("saved receipt overwrote later owner intent")
				}
				if after.Revision != before.Revision {
					t.Fatal("retry rewrote a newer projection")
				}
			}
			if !fresh && sender.sent {
				t.Fatal("durable receipt replay sent a new host command")
			}
		})
	}
}

type applicationAuthoritySender struct {
	result *agentpb.StackKitResult
	sent   bool
}

func (s *applicationAuthoritySender) SendStackKitCommand(context.Context, string, *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	s.sent = true
	return s.result, nil
}

// Sensitive invariant: unknown acknowledgement never resubmits setup. Current
// read verification of the original operation must precede service projection.
func TestApplicationConnectorUnknownAcknowledgementOnlyReconcilesReadOnly(t *testing.T) {
	for _, scenario := range []string{"qualified", "tampered", "stale-intent", "authority-changed", "durable-proof"} {
		t.Run(scenario, func(t *testing.T) {
			ctx := t.Context()
			store := controlplane.NewMemoryStore()
			release := releaseWithAdvancedCatalog(t)
			root := filepath.Dir(release.BinaryPath())
			raw, _ := os.ReadFile(filepath.Join(root, stackkitrelease.AdvancedOperationsFileName))
			var catalog map[string]any
			_ = json.Unmarshal(raw, &catalog)
			operations := catalog["operations"].([]any)
			for _, op := range []string{"application.connector", "application.connector.verify"} {
				operations = append(operations, map[string]any{"operation": op, "status": "available", "sinceRelease": "pending", "command": "stackkit setup connector", "argv": []string{"setup", "connector"}})
			}
			catalog["operations"] = operations
			raw, _ = json.Marshal(catalog)
			if err := os.WriteFile(filepath.Join(root, stackkitrelease.AdvancedOperationsFileName), raw, 0600); err != nil {
				t.Fatal(err)
			}
			release, err := (stackkitrelease.Cache{Root: root}).ResolvePin(filepath.Join(root, "stackkits-release-pin.json"))
			if err != nil {
				t.Fatal(err)
			}
			container := strings.Repeat("a", 64)
			digest := "sha256:" + strings.Repeat("b", 64)
			_, err = store.UpsertServiceRuntime(ctx, controlplane.ServiceRuntime{ID: "ha", TenantID: "tenant-1", StackID: "stack-1", ServerID: "server-1", ServiceKey: "smart-home", Source: serviceregistry.SourceStackKitsInventory, ManagementState: "managed", Metadata: map[string]any{"container_id": container, "image": "ghcr.io/home-assistant/home-assistant@" + digest}})
			if err != nil {
				t.Fatal(err)
			}
			head, err := store.ReadServiceEventHead(ctx, "tenant-1", "ha")
			if err != nil {
				t.Fatal(err)
			}
			request := stackkitcommand.ConnectorRequest{SchemaVersion: stackkitcommand.ApplicationConnectorRequestSchema}
			request.Connector.Binding = stackkitcommand.ConnectorSource{ResourceID: "ha", ContainerID: container, ImageDigest: digest, ServiceRevision: head.Revision, Node: stackkitcommand.ConnectorNode{SiteRef: "home", NodeRef: "main", ExecutionChannelRef: "local-home-main"}}
			request.Connector.Action = "ensure"
			request.Connector.LifespanDays = 30
			req := StackKitLifecycleRequest{TenantID: "tenant-1", OwnerID: "owner-1", StackID: "stack-1", AgentID: "agent-1", StackKit: "basement-kit", StackKitInstanceID: "instance-1", WorkingDirectory: "/opt/stackkit", ServiceID: "ha", ServiceKey: "smart-home", DurableJobID: "connector-original", ServiceActionDigest: strings.Repeat("c", 64), Operation: StackKitLifecycleApplicationConnector, OwnerApproved: true, Connector: &ApplicationConnectorBinding{Request: request, ReleasePin: grpcserver.StackKitReleasePinFor(release), ServerID: "server-1", ServerGeneration: 1, WorkerGeneration: 1, ExpectedIntentRevision: ApplicationIntentRevision(head.Service)}}
			sender := &connectorLossSender{t: t, req: req, tamper: scenario == "tampered"}
			queue := NewQueue(0, nil)
			persisted := false
			queue.SetExecutionSnapshotSyncer(func(_ context.Context, s JobSnapshot) error {
				if marked, _ := s.Result["connector_dispatch_intent"].(bool); marked {
					persisted = true
				}
				return nil
			})
			sender.persisted = &persisted
			denied := false
			cfg := StackKitLifecycleConfig{Sender: sender, Services: store, ApplicationAuthorize: func(context.Context, string, string, string) error { return nil }, ConnectorAuthorize: func(context.Context, StackKitLifecycleRequest) error {
				if denied {
					return errors.New("current worker generation changed")
				}
				return nil
			}, releaseResolver: func() (*stackkitrelease.Release, error) { return &release, nil }}
			job := &Job{ID: req.DurableJobID, TargetID: req.StackID, Payload: StackKitLifecyclePayload(req), Result: map[string]interface{}{"service_action_receipt": StackKitServiceActionReceipt(req)}}
			handler := StackKitLifecycleHandler(cfg)
			err = handler(ctx, job, queue)
			if _, waiting := asJobWaitError(err); !waiting || !sender.mutated || !persisted {
				t.Fatalf("unknown ACK was not fenced after durable intent: %v", err)
			}
			if scenario == "stale-intent" {
				_, err = store.ApplyServiceEvent(ctx, controlplane.ServiceEvent{TenantID: req.TenantID, ServiceID: req.ServiceID, Authority: controlplane.ServiceEventAuthorityControlPlane, Runtime: controlplane.ServiceRuntime{DesiredState: "stopped"}})
				if err != nil {
					t.Fatal(err)
				}
			}
			if scenario == "authority-changed" {
				denied = true
			}
			err = handler(ctx, job, queue)
			after, _ := store.ReadServiceEventHead(ctx, req.TenantID, req.ServiceID)
			qualified := scenario == "qualified" || scenario == "durable-proof"
			if qualified {
				if err != nil || after.Service.Metadata["application_connector_job_id"] != req.DurableJobID || !sender.readVerified {
					t.Fatalf("current read did not project original operation: %v", err)
				}
				projection, _ := json.Marshal(after.Service.Metadata["application_connector"])
				if strings.Contains(string(projection), "secret://") || strings.Contains(string(projection), "signature") {
					t.Fatal("node custody escaped into service projection")
				}
				// A retained exact proof must project without another host command.
				sender.noSend = true
				if err = handler(ctx, job, queue); err != nil {
					t.Fatalf("saved proof failed no-send recovery: %v", err)
				}
			} else {
				if err == nil || after.Service.Metadata["application_connector_job_id"] == req.DurableJobID {
					t.Fatal("unqualified observation projected connector")
				}
				if unresolved, _ := job.Snapshot().Result["connector_outcome_unresolved"].(bool); !unresolved {
					t.Fatal("unqualified outcome released durable service fence")
				}
			}
		})
	}
}

type connectorLossSender struct {
	t                                     *testing.T
	req                                   StackKitLifecycleRequest
	persisted                             *bool
	mutated, readVerified, tamper, noSend bool
}

func (s *connectorLossSender) SendStackKitCommand(ctx context.Context, agent string, c *agentpb.StackKitCommand) (*agentpb.StackKitResult, error) {
	s.t.Helper()
	if s.noSend {
		s.t.Fatal("durable proof caused host replay")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_PLAN {
		return (&recordingStackKitCommandSender{}).SendStackKitCommand(ctx, agent, c)
	}
	if c.ApplicationOperationId != s.req.DurableJobID {
		s.t.Fatal("original operation identity changed")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR {
		if s.mutated {
			s.t.Fatal("unknown ACK resubmitted mutation")
		}
		if !*s.persisted {
			s.t.Fatal("mutation sent before durable intent")
		}
		s.mutated = true
		return nil, errors.New("ACK lost after node mutation")
	}
	if c.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY || c.OwnerApproved || c.CommandId == s.req.DurableJobID {
		s.t.Fatal("reconciliation was not a new read-only command")
	}
	s.readVerified = true
	receipt := stackkitcommand.ConnectorSignedReceipt{Signature: stackkitcommand.ConnectorSignature{OwnerRef: "local-owner", KeyID: "key", Value: "synthetic-signed"}}
	receipt.Result = stackkitcommand.ConnectorSetupResult{APIVersion: "stackkit.application-setup-result/v1", Authority: stackkitcommand.ApplicationAuthority{PlanHash: c.ExpectedPlanHash, LifecycleContractHash: testResolvedPlanHash, LifecycleVersion: "v1", PackageRef: "smart-home@v1"}, WorkloadRef: "smart-home", OperationID: s.req.DurableJobID, ActionRef: "home-assistant-owner-bootstrap", ApplyResultHash: testResolvedPlanHash, ArtifactDigest: testResolvedPlanHash, InstanceRef: "local-ha", ApplicationVersion: "2026.7.2", AccountRef: "local-ha-user", Initialized: true, AdminLoginVerified: true, OnboardingComplete: true, VerifiedAt: time.Now().UTC()}
	receipt.Result.HomeAssistantConnector.Issuer = stackkitcommand.ConnectorIssuer{OwnerRef: "local-owner", HAUserID: "local-ha-user", HAVersion: "2026.7.2", RequestedBinding: s.req.Connector.Request.Connector.Binding, SecretRef: "secret://home-assistant/connector/" + strings.Repeat("e", 64), Status: "active", ObservedAt: time.Now().UTC(), ExpiresAt: time.Now().Add(30 * 24 * time.Hour), Signature: receipt.Signature}
	deployment := stackkitcommand.ConnectorDeployment{Node: s.req.Connector.Request.Connector.Binding.Node, InstanceRef: "local-ha", ContainerID: s.req.Connector.Request.Connector.Binding.ContainerID, ImageDigest: s.req.Connector.Request.Connector.Binding.ImageDigest}
	receipt.Result.HomeAssistantConnector.VerifiedLocalDeployment = deployment
	raw, _ := json.Marshal(receipt)
	hash := sha256.Sum256(raw)
	e := stackkitcommand.ApplicationConnectorEvidence{SchemaVersion: stackkitcommand.ApplicationConnectorResultSchema, SignatureVerified: true, ReceiptBase64: base64.StdEncoding.EncodeToString(raw), ReceiptDigest: "sha256:" + hex.EncodeToString(hash[:]), CurrentLocalDeployment: deployment}
	if s.tamper {
		e.CurrentLocalDeployment.ContainerID = strings.Repeat("d", 64)
	}
	envelope, _ := json.Marshal(map[string]any{"schemaVersion": stackkitcommand.CommandResultVersion, "command": "stackkit setup connector", "status": "success", "data": e})
	attestation := sha256.Sum256(envelope)
	return &agentpb.StackKitResult{CommandId: c.CommandId, Release: c.Release, Success: true, CommandResultSchemaVersion: stackkitcommand.CommandResultVersion, EventsSchemaVersion: stackkitcommand.RolloutEventVersion, CommandResultJson: envelope, FinishedAtUnix: time.Now().Unix(), PinnedCliEvidenceVerified: true, PinnedCliEvidenceSha256: "sha256:" + hex.EncodeToString(attestation[:])}, nil
}
