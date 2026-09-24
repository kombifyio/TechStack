package providercontrol

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

const invocationKeyVersion = "provider-invocation-v1"

func newAdapterInvocation(request providerexecutor.ExecutionRequest) (AdapterInvocation, error) {
	if err := request.Command.Validate(); err != nil {
		return AdapterInvocation{}, err
	}
	if request.Previous.OperationID != request.Command.OperationID ||
		request.Previous.TenantID != request.Command.TenantID ||
		request.Previous.Sequence == 0 || request.Previous.ReceiptDigest == "" {
		return AdapterInvocation{}, fmt.Errorf("%w: invocation head does not match command", ErrInvalidRequest)
	}
	payload, err := json.Marshal(struct {
		Version              string `json:"version"`
		OperationID          string `json:"operation_id"`
		ResourceGenerationID string `json:"resource_generation_id"`
		IdempotencyKey       string `json:"idempotency_key"`
		HeadSequence         uint64 `json:"head_sequence"`
		HeadDigest           string `json:"head_digest"`
	}{
		Version: invocationKeyVersion, OperationID: request.Command.OperationID,
		ResourceGenerationID: request.Command.ResourceGenerationID,
		IdempotencyKey:       request.Command.IdempotencyKey,
		HeadSequence:         request.Previous.Sequence, HeadDigest: request.Previous.ReceiptDigest,
	})
	if err != nil {
		return AdapterInvocation{}, fmt.Errorf("providercontrol: encode invocation identity: %w", err)
	}
	digest := sha256.Sum256(payload)
	digestText := hex.EncodeToString(digest[:])
	return AdapterInvocation{
		Request: request, Key: "pvi1_" + digestText, CorrelationID: "pvc1_" + digestText,
	}, nil
}
