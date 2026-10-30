package jobs

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

const RILOperationReceiptKey = "ril_operation_receipt"

// RILOperationBinding is derived only after the central RIL authority admits
// an approved card. No HTTP lifecycle request exposes these fields.
type RILOperationBinding struct {
	CardID          string `json:"card_id"`
	ExecutionID     string `json:"execution_id"`
	RequestDigest   string `json:"request_digest"`
	AdmissionDigest string `json:"admission_digest"`
	ActivityKeyHash string `json:"activity_key_sha256"`
	IntentDigest    string `json:"intent_digest"`
}

func BindRILStackKitOperation(req StackKitLifecycleRequest, approved rilaction.Request, admissionDigest, activityKey string) (StackKitLifecycleRequest, error) {
	if req.TenantID != approved.TenantID || req.StackID != approved.StackID || req.AgentID != approved.Target.RuntimeInstanceRef ||
		approved.ActionCardID == "" || approved.ExecutionID == "" || len(activityKey) < 16 || len(activityKey) > 128 {
		return req, fmt.Errorf("RIL lifecycle identity is not bound to the admitted request")
	}
	requestDigest, err := rilaction.ComputeRequestDigest(approved)
	if err != nil {
		return req, err
	}
	if !rilBindingDigest(admissionDigest) {
		return req, fmt.Errorf("RIL lifecycle requires an immutable execution admission")
	}
	req.RILOperation, req.DurableJobID, req.ServiceActionDigest = nil, "", ""
	normalized, err := NormalizeStackKitLifecycleRequest(req)
	if err != nil {
		return req, err
	}
	intentDigest, err := rilStackKitIntentDigest(normalized)
	if err != nil {
		return req, err
	}
	keyHash := sha256.Sum256([]byte(activityKey))
	keyDigest := fmt.Sprintf("%x", keyHash)
	normalized.DurableJobID = RILStackKitOperationJobID(normalized.TenantID, normalized.OwnerID, approved.ActionCardID, activityKey)
	normalized.RILOperation = &RILOperationBinding{
		CardID: approved.ActionCardID, ExecutionID: approved.ExecutionID,
		RequestDigest: requestDigest, AdmissionDigest: admissionDigest,
		ActivityKeyHash: keyDigest, IntentDigest: intentDigest,
	}
	return normalized, nil
}

func ValidateRILStackKitBinding(req StackKitLifecycleRequest) error {
	binding := req.RILOperation
	if binding == nil {
		return nil
	}
	if binding.CardID == "" || binding.ExecutionID == "" || req.DurableJobID == "" || req.ServiceID != "" ||
		!rilBindingDigest(binding.RequestDigest) || !rilBindingDigest(binding.AdmissionDigest) ||
		!bareRILDigest(binding.ActivityKeyHash) || !bareRILDigest(binding.IntentDigest) {
		return fmt.Errorf("RIL lifecycle binding is incomplete")
	}
	intent, err := rilStackKitIntentDigest(req)
	if err != nil || intent != binding.IntentDigest || req.DurableJobID != rilStackKitJobID(req.TenantID, req.OwnerID, binding.CardID, binding.ActivityKeyHash) {
		return fmt.Errorf("RIL lifecycle binding does not authorize the current intent")
	}
	return nil
}

// FinalizeRILStackKitOperationBinding binds the canonical node/rollout defaults
// after enqueue admission has validated the original caller intent. Durable
// reload validates this final payload and never silently reseals a mutation.
func FinalizeRILStackKitOperationBinding(req StackKitLifecycleRequest) (StackKitLifecycleRequest, error) {
	if req.RILOperation == nil {
		return req, nil
	}
	digest, err := rilStackKitIntentDigest(req)
	if err != nil {
		return req, err
	}
	binding := *req.RILOperation
	binding.IntentDigest = digest
	req.RILOperation = &binding
	return req, ValidateRILStackKitBinding(req)
}

func rilStackKitJobID(tenant, owner, card, keyHash string) string {
	digest := sha256.Sum256([]byte(tenant + "\x00" + owner + "\x00" + card + "\x00" + keyHash))
	return fmt.Sprintf("job-stackkit-ril-%x", digest)
}

// RILStackKitOperationJobID locates the same durable reservation after a crash
// before the workflow stored the enqueue result. It grants no execution rights.
func RILStackKitOperationJobID(tenant, owner, card, activityKey string) string {
	keyHash := sha256.Sum256([]byte(activityKey))
	return rilStackKitJobID(tenant, owner, card, fmt.Sprintf("%x", keyHash))
}

func rilStackKitIntentDigest(req StackKitLifecycleRequest) (string, error) {
	req.RILOperation, req.DurableJobID, req.ServiceActionDigest = nil, "", ""
	intent, err := json.Marshal(struct {
		StackID string         `json:"techstack_id"`
		Payload map[string]any `json:"lifecycle_payload"`
	}{req.StackID, StackKitLifecyclePayload(req)})
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(intent)
	return fmt.Sprintf("%x", digest), nil
}

// RILStackKitOperationFromJob uses the same durable decoder and normalization
// as node execution. A receipt alone cannot authorize changed persisted intent.
func RILStackKitOperationFromJob(job *Job) (StackKitLifecycleRequest, error) {
	request, err := stackKitLifecycleRequestFromJob(job)
	if err != nil {
		return request, err
	}
	if request.RILOperation == nil || !MatchesRILStackKitOperationReceipt(job.Result, request) {
		return request, fmt.Errorf("RIL lifecycle job has no matching intent receipt")
	}
	return request, nil
}

func RILStackKitOperationReceipt(req StackKitLifecycleRequest) map[string]any {
	if req.RILOperation == nil {
		return nil
	}
	data, _ := json.Marshal(req.RILOperation)
	var out map[string]any
	_ = json.Unmarshal(data, &out)
	out["schema_version"] = "techstack.ril-lifecycle-receipt/v1"
	out["tenant_id"], out["owner_id"], out["techstack_id"] = req.TenantID, req.OwnerID, req.StackID
	out["agent_id"], out["operation"] = req.AgentID, req.Operation
	return out
}

// Advanced summaries replace the complete result, including unsuccessful
// summaries needed by recovery. Retain the original admitted intent receipt
// through the existing snapshot/persistence authority.
func (job *Job) replaceStackKitLifecycleResult(req StackKitLifecycleRequest, result map[string]any) {
	if receipt := WizardExtendReceipt(req); receipt != nil {
		result[WizardExtendReceiptKey] = receipt
	}
	if receipt := RILStackKitOperationReceipt(req); receipt != nil {
		result[RILOperationReceiptKey] = receipt
	}
	job.replaceResult(result)
}

func MatchesRILStackKitOperationReceipt(result map[string]any, req StackKitLifecycleRequest) bool {
	want := RILStackKitOperationReceipt(req)
	got, ok := result[RILOperationReceiptKey].(map[string]any)
	if want == nil || !ok {
		return false
	}
	for key, value := range want {
		actual, ok := got[key].(string)
		if !ok || actual != value {
			return false
		}
	}
	return true
}

func rilBindingDigest(value string) bool {
	return len(value) == 71 && value[:7] == "sha256:" && bareRILDigest(value[7:])
}

func bareRILDigest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}
