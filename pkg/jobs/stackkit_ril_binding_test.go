package jobs

import (
	"strings"
	"testing"
)

// A persisted hash is not an authorization for a subsequently changed intent.
// This pure boundary probe supplies no DB, grant, approval or node result.
func TestRILStackKitBindingRefusesChangedDurableIntent(t *testing.T) {
	request, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		TenantID: "tenant-model", OwnerID: "subject-model", StackID: "stack-model", AgentID: "agent-model",
		Operation: StackKitLifecycleVerify,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := rilStackKitIntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.RILOperation = &RILOperationBinding{
		CardID: "card-model", ExecutionID: "execution-model", RequestDigest: "sha256:" + strings.Repeat("a", 64),
		AdmissionDigest: "sha256:" + strings.Repeat("b", 64), ActivityKeyHash: strings.Repeat("c", 64), IntentDigest: intent,
	}
	request.DurableJobID = rilStackKitJobID(request.TenantID, request.OwnerID, request.RILOperation.CardID, request.RILOperation.ActivityKeyHash)
	if _, err := NormalizeStackKitLifecycleRequest(request); err != nil {
		t.Fatalf("unchanged semantic intent was rejected: %v", err)
	}
	result := map[string]any{RILOperationReceiptKey: RILStackKitOperationReceipt(request)}
	if _, err := RILStackKitOperationFromJob(&Job{ID: request.DurableJobID, TargetID: request.StackID, Payload: StackKitLifecyclePayload(request), Result: result}); err != nil {
		t.Fatalf("unchanged durable intent was rejected: %v", err)
	}
	mutations := map[string]func(*StackKitLifecycleRequest){
		"operation": func(v *StackKitLifecycleRequest) { v.Operation = StackKitLifecyclePlan },
		"agent":     func(v *StackKitLifecycleRequest) { v.AgentID = "other-agent-model" },
		"stack":     func(v *StackKitLifecycleRequest) { v.StackID = "other-stack-model" },
		"tenant":    func(v *StackKitLifecycleRequest) { v.TenantID = "other-tenant-model" },
		"owner":     func(v *StackKitLifecycleRequest) { v.OwnerID = "other-subject-model" },
		"spec":      func(v *StackKitLifecycleRequest) { v.SpecPath = "other-spec.yaml" },
		"release":   func(v *StackKitLifecycleRequest) { v.TargetRelease = "v0.52.6" },
		"approval":  func(v *StackKitLifecycleRequest) { v.OwnerApproved = true },
		"job":       func(v *StackKitLifecycleRequest) { v.DurableJobID = "foreign-job-model" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if _, err := RILStackKitOperationFromJob(&Job{ID: request.DurableJobID, TargetID: changed.StackID, Payload: StackKitLifecyclePayload(changed), Result: result}); err == nil {
				t.Fatal("durable re-read admitted a changed intent with the original binding")
			}
		})
	}
	canonical := request
	canonical.NodeID, canonical.SpecPath = "node-model", "canonical-spec.yaml"
	canonical, err = FinalizeRILStackKitOperationBinding(canonical)
	if err != nil {
		t.Fatalf("canonical node defaults could not be bound: %v", err)
	}
	canonicalResult := map[string]any{RILOperationReceiptKey: RILStackKitOperationReceipt(canonical)}
	if _, err := RILStackKitOperationFromJob(&Job{ID: canonical.DurableJobID, TargetID: canonical.StackID, Payload: StackKitLifecyclePayload(canonical), Result: canonicalResult}); err != nil {
		t.Fatalf("canonical durable intent was rejected: %v", err)
	}
	canonical.NodeID = "changed-node-model"
	if _, err := RILStackKitOperationFromJob(&Job{ID: canonical.DurableJobID, TargetID: canonical.StackID, Payload: StackKitLifecyclePayload(canonical), Result: canonicalResult}); err == nil {
		t.Fatal("canonical defaults were silently resealed during durable re-read")
	}
}

// Exercise the production Advanced result setter, snapshot and durable
// decoder with an unsuccessful DTO. No approval, Grant or node is supplied.
func TestRILStackKitBindingSurvivesAdvancedResultReplacement(t *testing.T) {
	request, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
		TenantID: "tenant-model", OwnerID: "subject-model", StackID: "stack-model", AgentID: "agent-model",
		Operation: StackKitLifecycleVerify,
	})
	if err != nil {
		t.Fatal(err)
	}
	intent, err := rilStackKitIntentDigest(request)
	if err != nil {
		t.Fatal(err)
	}
	request.RILOperation = &RILOperationBinding{
		CardID: "card-model", ExecutionID: "execution-model", RequestDigest: "sha256:" + strings.Repeat("a", 64),
		AdmissionDigest: "sha256:" + strings.Repeat("b", 64), ActivityKeyHash: strings.Repeat("c", 64), IntentDigest: intent,
	}
	request.DurableJobID = rilStackKitJobID(request.TenantID, request.OwnerID, request.RILOperation.CardID, request.RILOperation.ActivityKeyHash)
	job := &Job{ID: request.DurableJobID, TargetID: request.StackID, Payload: StackKitLifecyclePayload(request), Result: map[string]any{RILOperationReceiptKey: RILStackKitOperationReceipt(request)}}
	summary := map[string]any{"schema_version": "techstack.stackkit-lifecycle-result/v1", "success": false}
	job.replaceStackKitLifecycleResult(request, summary)
	snapshot := job.Snapshot()
	reloaded, err := RILStackKitOperationFromJob(&Job{ID: snapshot.ID, TargetID: snapshot.TargetID, Payload: snapshot.Payload, Result: snapshot.Result})
	if err != nil {
		t.Fatalf("unsuccessful summary lost its durable RIL identity: %v", err)
	}
	if *reloaded.RILOperation != *request.RILOperation || snapshot.Result["success"] != false {
		t.Fatal("replacement changed the admitted identity or promoted the unsuccessful result")
	}
	summary[RILOperationReceiptKey].(map[string]any)["card_id"] = "foreign-card-model"
	if _, err := RILStackKitOperationFromJob(job.DetachedCopy()); err != nil {
		t.Fatalf("producer map mutation changed the stored snapshot binding: %v", err)
	}
}
