package orchestrator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

const (
	provisionIdempotencySchema        = "techstack/provision-idempotency/v1"
	deployIdempotencySchema           = "techstack/deploy-idempotency/v1"
	deployIdempotencyReceiptResultKey = "deploy_idempotency"
)

var (
	ErrProvisionIdempotencyConflict = errors.New("provision idempotency conflict")
	ErrDeployIdempotencyConflict    = errors.New("deploy idempotency conflict")
)

type stackLifecycleIdempotency struct {
	jobID         string
	jobType       string
	resultField   string
	step          string
	conflictErr   error
	receipt       map[string]any
	initialResult map[string]any
}

// ValidateProvisionIdempotencyKey keeps raw retry keys at the request boundary.
// Durable state contains only identity and request digests.
func ValidateProvisionIdempotencyKey(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) < 8 || len(value) > 256 || strings.TrimSpace(value) != value || strings.Contains(value, ",") {
		return "", fmt.Errorf("invalid provision idempotency key")
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e {
			return "", fmt.Errorf("invalid provision idempotency key")
		}
	}
	return value, nil
}

func newProvisionIdempotency(stack *orchestratorStack, spec map[string]interface{}, key string) (*stackLifecycleIdempotency, error) {
	return newStackLifecycleIdempotency(stack, map[string]any{"spec": spec}, key,
		provisionIdempotencySchema, "provision", persistentJobTypeProvision,
		jobs.ProvisionIdempotencyReceiptResultField, "Queued for provisioning", ErrProvisionIdempotencyConflict)
}

func newDeployIdempotency(stack *orchestratorStack, key string) (*stackLifecycleIdempotency, error) {
	return newStackLifecycleIdempotency(stack, map[string]any{"action": "deploy"}, key,
		deployIdempotencySchema, "deploy", persistentJobTypeDeploy,
		deployIdempotencyReceiptResultKey, "Queued for deployment", ErrDeployIdempotencyConflict)
}

func newStackLifecycleIdempotency(stack *orchestratorStack, intent map[string]any, key, schema, jobPrefix, jobType, resultField, step string, conflictErr error) (*stackLifecycleIdempotency, error) {
	jobID, identitySum, key, err := stackLifecycleIdempotencyIdentity(schema, jobPrefix, stack.tenantID, stack.ownerID, stack.id, key)
	if err != nil || key == "" {
		return nil, err
	}
	requestFields := map[string]any{"schema": schema, "tenant_id": stack.tenantID, "owner_id": stack.ownerID, "stack_id": stack.id}
	for name, value := range intent {
		requestFields[name] = value
	}
	request, err := json.Marshal(requestFields)
	if err != nil {
		return nil, fmt.Errorf("encode %s idempotency request: %w", jobType, err)
	}
	requestSum := sha256.Sum256(request)
	return &stackLifecycleIdempotency{
		jobID: jobID, jobType: jobType, resultField: resultField, step: step, conflictErr: conflictErr,
		receipt: map[string]any{
			"schema":          schema,
			"identity_sha256": "sha256:" + hex.EncodeToString(identitySum[:]),
			"request_sha256":  "sha256:" + hex.EncodeToString(requestSum[:]),
		},
	}, nil
}

// ProvisionIdempotencyJobID returns the non-secret durable correlation ID used
// by the HTTP admission fence before it permits a replay past active-state checks.
func ProvisionIdempotencyJobID(tenantID, ownerID, stackID, key string) (string, error) {
	jobID, _, _, err := stackLifecycleIdempotencyIdentity(provisionIdempotencySchema, "provision", tenantID, ownerID, stackID, key)
	return jobID, err
}

// DeployIdempotencyJobID exposes the non-secret durable deploy correlation ID
// to the HTTP admission fence without leaking the caller's raw key.
func DeployIdempotencyJobID(tenantID, ownerID, stackID, key string) (string, error) {
	jobID, _, _, err := stackLifecycleIdempotencyIdentity(deployIdempotencySchema, "deploy", tenantID, ownerID, stackID, key)
	return jobID, err
}

func stackLifecycleIdempotencyIdentity(schema, jobPrefix, tenantID, ownerID, stackID, key string) (string, [sha256.Size]byte, string, error) {
	key, err := ValidateProvisionIdempotencyKey(key)
	if err != nil || key == "" {
		return "", [sha256.Size]byte{}, key, err
	}
	identity := strings.Join([]string{schema, tenantID, ownerID, stackID, key}, "\x00")
	sum := sha256.Sum256([]byte(identity))
	return jobPrefix + "-" + hex.EncodeToString(sum[:16]), sum, key, nil
}

func (o *Orchestrator) findStackLifecycleIdempotencyReplay(ctx context.Context, stack *orchestratorStack, identity *stackLifecycleIdempotency) (string, bool, error) {
	if identity == nil {
		return "", false, nil
	}
	if o == nil || o.jobStore == nil {
		return "", false, fmt.Errorf("durable job store is required")
	}
	existing, err := o.jobStore.GetJob(ctx, stack.tenantID, identity.jobID)
	if errors.Is(err, controlplane.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load %s idempotency receipt: %w", identity.jobType, err)
	}
	if !matchingStackLifecycleIdempotency(existing, stack, identity) {
		return "", false, identity.conflictErr
	}
	return existing.ID, true, nil
}

func (o *Orchestrator) createStackLifecycleIdempotencyJob(ctx context.Context, stack *orchestratorStack, identity *stackLifecycleIdempotency) (string, bool, error) {
	request := controlplane.UpsertJobRequest{
		ID: identity.jobID, TenantID: stack.tenantID, StackID: stack.id,
		Type: identity.jobType, State: persistentStatePending,
		Step: identity.step, Message: identity.step,
		Result: map[string]any{identity.resultField: identity.receipt},
	}
	for key, value := range identity.initialResult {
		request.Result[key] = value
	}
	if _, err := o.jobStore.CreateJob(ctx, request); err == nil {
		return identity.jobID, false, nil
	} else if !errors.Is(err, controlplane.ErrConflict) {
		return "", false, fmt.Errorf("persist %s idempotency receipt: %w", identity.jobType, err)
	}
	return o.findStackLifecycleIdempotencyReplay(ctx, stack, identity)
}

func matchingStackLifecycleIdempotency(job *controlplane.Job, stack *orchestratorStack, identity *stackLifecycleIdempotency) bool {
	if job == nil || job.ID != identity.jobID || job.TenantID != stack.tenantID ||
		job.StackID != stack.id || job.Type != identity.jobType {
		return false
	}
	receipt, ok := job.Result[identity.resultField].(map[string]any)
	return ok && receipt["schema"] == identity.receipt["schema"] &&
		receipt["identity_sha256"] == identity.receipt["identity_sha256"] &&
		receipt["request_sha256"] == identity.receipt["request_sha256"]
}
