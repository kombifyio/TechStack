//nolint:goconst // shares the stacks package's ubiquitous "pending"/"name"/"stacks" literals; matches crud_handlers.go
package stacks

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/google/uuid"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/specv2"
)

type persistedStack struct {
	Id string
	// Name is the actually-persisted stack name. It may differ from the
	// requested name when a duplicate was auto-resolved (e.g. "techstack" ->
	// "techstack-2"). Callers MUST use this for downstream specs/responses.
	Name string
	// IdempotentReplay is true when X-Idempotency-Key resolved a stack that was
	// already persisted by an earlier delivery of the same create request.
	IdempotentReplay bool
}

const (
	// maxStackNameAutoFixAttempts bounds the auto-increment search ("name",
	// "name-2", ... ) before falling back to a guaranteed-unique suffix.
	maxStackNameAutoFixAttempts = 100
	// maxStackNameLength matches the stacks.name storage limit; candidate
	// names are truncated to fit so auto-increment/fallback never overflow it.
	maxStackNameLength = 100
)

// stackPersistContext carries the canonical per-request persistence inputs.
type stackPersistContext struct {
	ownerID        string
	tenantID       string
	idempotencyKey string
	requestHash    string
	req            normalizedCreateStackRequest
}

// stackNameCandidate returns the auto-increment candidate for an attempt:
// attempt 0 -> base, attempt 1 -> "base-2", attempt 2 -> "base-3", ...
// The base is truncated so the result fits maxStackNameLength.
func stackNameCandidate(base string, attempt int) string {
	base = strings.TrimSpace(base)
	if attempt <= 0 {
		return truncateStackName(base, "")
	}
	return truncateStackName(base, fmt.Sprintf("-%d", attempt+1))
}

// uniqueStackNameFallback returns a guaranteed-unique name for the rare case
// that auto-increment is exhausted, so stack creation NEVER fails on a name
// collision (product rule 2026-05-28: a duplicate name must never error).
func uniqueStackNameFallback(base string) string {
	return truncateStackName(strings.TrimSpace(base), "-"+uuid.NewString()[:8])
}

// truncateStackName trims base so that base+suffix fits maxStackNameLength,
// always preserving the full suffix.
func truncateStackName(base, suffix string) string {
	if base == "" {
		base = "stack"
	}
	if len(base)+len(suffix) <= maxStackNameLength {
		return base + suffix
	}
	keep := maxStackNameLength - len(suffix)
	if keep < 1 {
		keep = 1
	}
	return base[:keep] + suffix
}

func (h crudRouteHandlers) persistStack(e *httpx.Event, ownerID, tenantID string, req normalizedCreateStackRequest) (*persistedStack, error) {
	return h.persistStackWithRequestHash(e, ownerID, tenantID, req, createStackRequestHash(req))
}

// persistStackWithRequestHash lets the wizard-run facade pin the idempotency
// fingerprint to the client's raw wire request. The default fingerprint hashes
// the normalized request AFTER owner-bootstrap resolution injects generated
// material (e.g. a fresh recovery hash) and after run-kind coercion mutates
// the options — both make a byte-identical retry hash differently and turn
// the promised same-key resume into a 409.
func (h crudRouteHandlers) persistStackWithRequestHash(e *httpx.Event, ownerID, tenantID string, req normalizedCreateStackRequest, requestHash string) (*persistedStack, error) {
	idempotencyKey := createStackIdempotencyKey(e)
	if len(idempotencyKey) > 256 {
		return nil, httpx.BadRequest(e, "X-Idempotency-Key must not exceed 256 characters", nil)
	}
	ctx := stackPersistContext{
		ownerID: ownerID, tenantID: strings.TrimSpace(tenantID), req: req,
		idempotencyKey: idempotencyKey, requestHash: requestHash,
	}
	if h.stackStore == nil {
		return nil, httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Stack create authority is temporarily unavailable", map[string]any{
				detailsKeyReasonCode: "stack_create_authority_unavailable",
				detailsKeyRetryable:  true,
			})
	}
	if err := h.ensureCanonicalHomelab(e, &ctx); err != nil {
		return nil, err
	}
	return h.persistStackViaStore(e, ctx)
}

// ensureCanonicalHomelab resolves the one active homelab for the authenticated
// owner before a subordinate kit deployment is persisted. The owner-scoped
// GetOrCreate implementation is backed by the database singleton constraint,
// so concurrent create requests converge on the same umbrella while retaining
// distinct stack rows for distinct node/kit deployments.
func (h crudRouteHandlers) ensureCanonicalHomelab(e *httpx.Event, ctx *stackPersistContext) error {
	if ctx == nil || strings.TrimSpace(ctx.tenantID) == "" {
		return nil
	}
	if h.homelabStore == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Failed to resolve the canonical homelab", map[string]any{
				detailsKeyReasonCode: "canonical_homelab_unavailable",
				detailsKeyRetryable:  true,
			})
	}
	homelab, err := h.homelabStore.GetOrCreateHomelabForOwner(e.Request.Context(), controlplane.CreateHomelabRequest{
		ID:             deterministicHomelabID(ctx.tenantID, ctx.ownerID),
		TenantID:       ctx.tenantID,
		OwnerSubjectID: ctx.ownerID,
		Name:           defaultCreateStackName,
	})
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable,
			"Failed to resolve the canonical homelab", map[string]any{
				detailsKeyReasonCode: "canonical_homelab_unavailable",
				detailsKeyRetryable:  true,
			})
	}
	ctx.req.HomelabID = homelab.ID
	return nil
}

func internalCreateStackError(e *httpx.Event) error {
	return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to create stack", nil)
}

// persistStackViaStore writes to the canonical control-plane. A duplicate name
// MUST NEVER error (product rule 2026-05-28): auto-resolve by retrying "name",
// "name-2", ... then a guaranteed-unique fallback.
func (h crudRouteHandlers) persistStackViaStore(e *httpx.Event, ctx stackPersistContext) (*persistedStack, error) {
	stackID := uuid.NewString()
	if ctx.idempotencyKey != "" {
		stackID = deterministicStackID(ctx.tenantID, ctx.ownerID, ctx.idempotencyKey)
		if existing, getErr := h.stackStore.GetStack(e.Request.Context(), ctx.tenantID, stackID); getErr == nil {
			if existing.OwnerSubjectID != ctx.ownerID {
				return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "X-Idempotency-Key conflicts with an existing stack", idempotencyConflictDetails(""))
			}
			if storedHash := fieldString(existing.Config, "creation_request_sha256"); storedHash != "" && storedHash != ctx.requestHash {
				return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "X-Idempotency-Key was already used for a different stack creation request", idempotencyConflictDetails(existing.ID))
			}
			return &persistedStack{Id: existing.ID, Name: existing.Name, IdempotentReplay: true}, nil
		} else if !errors.Is(getErr, controlplane.ErrNotFound) {
			return nil, internalCreateStackError(e)
		}
	}
	create := func(name string) (*persistedStack, error) {
		config := stackConfigFromRequest(ctx.req)
		if ctx.idempotencyKey != "" {
			config["creation_request_sha256"] = ctx.requestHash
		}
		stack, err := h.stackStore.CreateStack(e.Request.Context(), controlplane.CreateStackRequest{
			ID:                 stackID,
			TenantID:           ctx.tenantID,
			OwnerSubjectID:     ctx.ownerID,
			HomelabID:          ctx.req.HomelabID,
			StackKitInstanceID: specv2.StackKitInstanceID(ctx.req.StackSpecV2),
			Name:               name,
			Mode:               ctx.req.Mode,
			Status:             "pending",
			Config:             config,
		})
		if err != nil {
			return nil, err
		}
		return &persistedStack{Id: stack.ID, Name: stack.Name}, nil
	}
	for attempt := 0; attempt < maxStackNameAutoFixAttempts; attempt++ {
		ps, err := create(stackNameCandidate(ctx.req.Name, attempt))
		if errors.Is(err, controlplane.ErrStackKitInstanceConflict) {
			return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict,
				"StackKit instance identity already exists in this homelab", map[string]any{
					detailsKeyReasonCode: "stackkit_instance_conflict",
					detailsKeyRetryable:  false,
				})
		}
		if errors.Is(err, controlplane.ErrConflict) {
			if ctx.idempotencyKey != "" {
				existing, getErr := h.stackStore.GetStack(e.Request.Context(), ctx.tenantID, stackID)
				if getErr == nil && existing.OwnerSubjectID == ctx.ownerID {
					if storedHash := fieldString(existing.Config, "creation_request_sha256"); storedHash != "" && storedHash != ctx.requestHash {
						return nil, httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "X-Idempotency-Key was already used for a different stack creation request", idempotencyConflictDetails(existing.ID))
					}
					return &persistedStack{Id: existing.ID, Name: existing.Name, IdempotentReplay: true}, nil
				}
			}
			continue
		}
		if err != nil {
			return nil, internalCreateStackError(e)
		}
		return ps, nil
	}
	ps, err := create(uniqueStackNameFallback(ctx.req.Name))
	if err != nil {
		return nil, internalCreateStackError(e)
	}
	return ps, nil
}

// idempotencyConflictDetails marks a 409 as an attempt-key conflict (never a
// name conflict — duplicate names auto-resolve on this endpoint) so clients
// can discard the burned key and re-mint instead of dead-ending on retries.
func idempotencyConflictDetails(stackID string) map[string]any {
	details := map[string]any{
		detailsKeyReasonCode: "idempotency_conflict",
		detailsKeyRetryable:  false,
	}
	if stackID != "" {
		details[creationStackIDField] = stackID
	}
	return details
}

// deterministicStackID derives the keyed create-stack id; the wizard-run
// facade uses the same derivation to detect a same-key resume before its
// coercion logic runs.
func deterministicStackID(tenantID, ownerID, idempotencyKey string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte(strings.Join([]string{tenantID, ownerID, idempotencyKey}, "\x00"))).String()
}

func createStackIdempotencyKey(e *httpx.Event) string {
	if e == nil || e.Request == nil {
		return ""
	}
	return strings.TrimSpace(e.Request.Header.Get("X-Idempotency-Key"))
}

func createStackRequestHash(req normalizedCreateStackRequest) string {
	payload, err := json.Marshal(map[string]any{
		"name": req.Name, "mode": req.Mode, "user_config": req.UserConfig,
		"user_config_raw": req.UserConfigRaw, "user_config_format": req.UserConfigFormat,
		"options": req.Options,
	})
	if err != nil {
		payload = []byte(req.Name + "\x00" + req.Mode)
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func stackConfigFromRequest(req normalizedCreateStackRequest) map[string]any {
	config := map[string]any{}
	runtimeConfig := runtimeConfigFromRequest(req)
	if req.UserConfig != nil {
		config["user_config"] = redactUserConfigForStorage(req.UserConfig)
	}
	if req.StackSpecV2 != nil {
		// The projected v2 spec carries release-authored secretRef HANDLES, never
		// secret values, so it is stored unredacted as the join/rollout
		// authority (config_json.stack_spec_v2).
		config[stackConfigKeySpecV2] = req.StackSpecV2
	}
	if raw := strings.TrimSpace(req.UserConfigRaw); raw != "" {
		config["user_config_raw"] = redactUserConfigRawForStorage(raw)
	}
	if format := strings.TrimSpace(req.UserConfigFormat); format != "" {
		config["user_config_format"] = format
	}
	for key, value := range runtimeFieldsFromConfig(runtimeConfig) {
		config[key] = value
	}
	return config
}
