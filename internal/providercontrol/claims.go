package providercontrol

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

const defaultExecutionClaimTTL = time.Minute

func executionClaimToken() (string, error) {
	bytes := make([]byte, 32)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("providercontrol: generate execution claim token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(bytes), nil
}

func normalizeClaimTTL(ttl time.Duration) (time.Duration, error) {
	if ttl == 0 {
		return defaultExecutionClaimTTL, nil
	}
	if ttl < time.Second || ttl > 15*time.Minute {
		return 0, fmt.Errorf("%w: execution claim ttl must be between one second and fifteen minutes", ErrInvalidRequest)
	}
	return ttl, nil
}

func validClaim(claim ExecutionClaim) bool {
	return validClaimCapability(claim) && !claim.ExpiresAt.IsZero()
}

func validClaimCapability(claim ExecutionClaim) bool {
	return strings.TrimSpace(claim.TenantID) != "" &&
		strings.TrimSpace(claim.OperationID) != "" &&
		strings.TrimSpace(claim.ResourceGenerationID) != "" &&
		claim.HeadSequence > 0 &&
		claim.HeadSequence <= providerexecutor.MaxJSONSafeInteger &&
		strings.TrimSpace(claim.HeadReceiptDigest) != "" &&
		validExecutionClaimAccess(claim.Access) &&
		strings.TrimSpace(claim.Token) != "" &&
		strings.TrimSpace(claim.Owner) != ""
}

func validExecutionClaimAccess(access ExecutionClaimAccess) bool {
	return access == ExecutionClaimReadOnly || access == ExecutionClaimSideEffecting
}

func classifyExecutionClaimAccess(operation providerexecutor.Operation, phase providerexecutor.Phase) (ExecutionClaimAccess, error) {
	switch operation {
	case providerexecutor.OperationPlan, providerexecutor.OperationObserve:
		if phase == providerexecutor.PhaseAccepted {
			return ExecutionClaimReadOnly, nil
		}
	case providerexecutor.OperationProvision:
		switch phase {
		case providerexecutor.PhaseAccepted:
			return ExecutionClaimSideEffecting, nil
		case providerexecutor.PhaseResourcesBound:
			return ExecutionClaimReadOnly, nil
		}
	case providerexecutor.OperationReconcile:
		if phase == providerexecutor.PhaseAccepted {
			return ExecutionClaimSideEffecting, nil
		}
		if phase == providerexecutor.PhaseResourcesBound {
			return ExecutionClaimReadOnly, nil
		}
	case providerexecutor.OperationDecommission:
		switch phase {
		case providerexecutor.PhaseAccepted:
			return ExecutionClaimSideEffecting, nil
		case providerexecutor.PhaseDeleteAccepted, providerexecutor.PhaseAbsencePending:
			return ExecutionClaimReadOnly, nil
		}
	}
	return "", fmt.Errorf(
		"%w: operation %q phase %q has no claim-access classification",
		ErrAdapterSafety, operation, phase,
	)
}

func newExecutePermit(claim ExecutionClaim, binding PreparedProvisionBinding) ExecutePermit {
	return ExecutePermit{
		tenantID:          claim.TenantID,
		operationID:       claim.OperationID,
		headReceiptDigest: claim.HeadReceiptDigest,
		claimTokenDigest:  executionClaimTokenDigest(claim.Token),
		preparationDigest: preparedProvisionBindingDigest(binding),
	}
}

func validExecutePermit(permit ExecutePermit, claim ExecutionClaim, binding PreparedProvisionBinding) bool {
	return permit.tenantID == claim.TenantID &&
		permit.operationID == claim.OperationID &&
		permit.headReceiptDigest == claim.HeadReceiptDigest &&
		permit.claimTokenDigest == executionClaimTokenDigest(claim.Token) &&
		permit.preparationDigest == preparedProvisionBindingDigest(binding) &&
		validClaimCapability(claim)
}

// claimHeartbeat renews a claim while an adapter is executing. A failed
// renewal is a fail-closed condition: the result must not be appended.
type claimHeartbeat struct {
	cancel   context.CancelFunc
	done     chan struct{}
	mu       sync.Mutex
	renewMu  sync.Mutex
	stopOnce sync.Once
	stopping bool
	lost     bool
	claim    ExecutionClaim
}

func startClaimHeartbeat(ctx context.Context, ledger Ledger, claim ExecutionClaim, ttl time.Duration) (context.Context, *claimHeartbeat) {
	ctx, cancel := context.WithCancel(ctx)
	h := &claimHeartbeat{cancel: cancel, done: make(chan struct{}), claim: claim}
	interval := ttl / 3
	if interval <= 0 {
		interval = time.Millisecond
	}
	go func() {
		defer close(h.done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				h.renewMu.Lock()
				renewed, err := ledger.RenewExecutionClaim(ctx, h.current(), ttl)
				h.renewMu.Unlock()
				if err != nil {
					h.mu.Lock()
					if !h.stopping && !errors.Is(err, context.Canceled) {
						h.lost = true
					}
					lost := h.lost
					h.mu.Unlock()
					if lost {
						cancel()
					}
					return
				}
				h.mu.Lock()
				h.claim = renewed
				h.mu.Unlock()
			}
		}
	}()
	return ctx, h
}

func (h *claimHeartbeat) current() ExecutionClaim {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.claim
}

func (h *claimHeartbeat) snapshot() (ExecutionClaim, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.claim, h.lost
}

func (h *claimHeartbeat) stop() (ExecutionClaim, bool) {
	h.initiateStop()
	<-h.done
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.claim, h.lost
}

func (h *claimHeartbeat) initiateStop() {
	h.stopOnce.Do(func() {
		h.mu.Lock()
		h.stopping = true
		h.mu.Unlock()
		h.cancel()
	})
}

// finalize serializes the atomic claimed append against renewal. The heartbeat
// stays responsible for the lease until fn begins; fn locks the operation and
// consumes the claim in one database transaction. Its defer is intentionally
// panic-safe so the coordinator's cleanup defer can always stop and release.
func (h *claimHeartbeat) finalize(fn func(ExecutionClaim) error) (ExecutionClaim, bool, error) {
	h.renewMu.Lock()
	unlocked := false
	defer func() {
		if !unlocked {
			h.initiateStop()
			h.renewMu.Unlock()
		}
	}()

	h.mu.Lock()
	claim, lost := h.claim, h.lost
	h.mu.Unlock()
	if lost {
		h.initiateStop()
		h.renewMu.Unlock()
		unlocked = true
		<-h.done
		return claim, true, ErrExecutionClaimLost
	}
	err := fn(claim)
	h.initiateStop()
	h.renewMu.Unlock()
	unlocked = true
	<-h.done
	h.mu.Lock()
	claim, lost = h.claim, h.lost
	h.mu.Unlock()
	return claim, lost, err
}
