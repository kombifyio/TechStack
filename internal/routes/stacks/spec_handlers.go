// Package stacks provides handlers for accessing persisted spec files.
package stacks

import (
	"encoding/base64"
	"net/http"

	"github.com/pocketbase/pocketbase/core"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
)

const (
	specPathStackIDKey             = "id"
	specStacksCollection           = "stacks"
	specOwnerIDField               = "owner_id"
	specResponseBaseDirKey         = "base_dir"
	specResponseByteLengthKey      = "byteLength"
	specResponseContentKey         = "content"
	specResponseEncodingKey        = "encoding"
	specResponseFilenameKey        = "filename"
	specResponseHashChainValidKey  = "hash_chain_valid"
	specResponseIntentByteLenKey   = "intent_byteLength"
	specResponseIntentExistsKey    = "intent_exists"
	specResponseIntentHashKey      = "intent_sha256"
	specResponseMessageKey         = "message"
	specResponseRequirementsKey    = "requirements_exists"
	specResponseRequirementsMeta   = "requirements_metadata"
	specResponseSHA256Key          = "sha256"
	specResponseStackIDKey         = "stack_id"
	specResponseUnifiedExistsKey   = "unified_exists"
	specResponseUnifiedMetaKey     = "unified_metadata"
	specResponseValidKey           = "valid"
	specStackIDRequiredMessage     = "stack ID required"
	specStackNotFoundMessage       = "Stack not found"
	specStackNotOwnedMessage       = "Not your stack"
	specHashChainValidMessage      = "Hash chain is valid - spec pipeline integrity verified"
	specHashChainMismatchMessage   = "Hash chain mismatch - spec pipeline may have been tampered with"
	specBase64Encoding             = "base64"
	specIntentMissingMessage       = "kombination.yaml not found for stack"
	specRequirementsMissingMessage = "requirements spec not found for stack"
	specUnifiedMissingMessage      = "unified spec not found for stack"
)

// RegisterSpecRoutes adds API endpoints for accessing persisted specs.
// Routes registered:
//   - GET /api/v1/stacks/{id}/intent - Get original intent spec
//   - GET /api/v1/stacks/{id}/requirements - Get requirements spec
//   - GET /api/v1/stacks/{id}/unified - Get unified spec
//   - GET /api/v1/stacks/{id}/verify-chain - Verify hash chain integrity
//   - GET /api/v1/stacks/{id}/pipeline-status - Get pipeline status
func RegisterSpecRoutes(r *httpx.Router, app core.App) {
	handlers := specRouteHandlers{app: app}
	r.GET("/api/v1/stacks/{id}/intent", handlers.intent)
	r.GET("/api/v1/stacks/{id}/requirements", handlers.requirements)
	r.GET("/api/v1/stacks/{id}/unified", handlers.unified)
	r.GET("/api/v1/stacks/{id}/verify-chain", handlers.verifyChain)
	r.GET("/api/v1/stacks/{id}/pipeline-status", handlers.pipelineStatus)
}

type specRouteHandlers struct {
	app core.App
}

type specRouteContext struct {
	stackID   string
	persister *unifier.SpecPersister
}

func (h specRouteHandlers) intent(e *httpx.Event) error {
	routeCtx, ok, err := h.routeContext(e)
	if !ok || err != nil {
		return err
	}
	if !routeCtx.persister.IntentExists() {
		return httpx.NotFound(e, specIntentMissingMessage)
	}

	data, err := routeCtx.persister.LoadIntentBytes()
	if err != nil {
		return specInternalError(e, "failed to load kombination.yaml", err)
	}

	return httpx.Success(e, http.StatusOK, specIntentResponse(routeCtx.stackID, data))
}

func (h specRouteHandlers) requirements(e *httpx.Event) error {
	routeCtx, ok, err := h.routeContext(e)
	if !ok || err != nil {
		return err
	}
	if !routeCtx.persister.RequirementsSpecExists() {
		return httpx.NotFound(e, specRequirementsMissingMessage)
	}

	spec, err := routeCtx.persister.LoadRequirementsSpec()
	if err != nil {
		return specInternalError(e, "failed to load requirements spec", err)
	}

	return httpx.Success(e, http.StatusOK, spec)
}

func (h specRouteHandlers) unified(e *httpx.Event) error {
	routeCtx, ok, err := h.routeContext(e)
	if !ok || err != nil {
		return err
	}
	if !routeCtx.persister.UnifiedSpecExists() {
		return httpx.NotFound(e, specUnifiedMissingMessage)
	}

	spec, err := routeCtx.persister.LoadUnifiedSpec()
	if err != nil {
		return specInternalError(e, "failed to load unified spec", err)
	}

	return httpx.Success(e, http.StatusOK, spec)
}

func (h specRouteHandlers) verifyChain(e *httpx.Event) error {
	routeCtx, ok, err := h.routeContext(e)
	if !ok || err != nil {
		return err
	}

	valid, err := routeCtx.persister.VerifyHashChain()
	if err != nil {
		return specInternalError(e, "failed to verify hash chain", err)
	}

	return httpx.Success(e, http.StatusOK, specHashChainResponse(routeCtx.stackID, valid))
}

func (h specRouteHandlers) pipelineStatus(e *httpx.Event) error {
	routeCtx, ok, err := h.routeContext(e)
	if !ok || err != nil {
		return err
	}

	return httpx.Success(e, http.StatusOK, specPipelineStatusResponse(routeCtx.stackID, routeCtx.persister))
}

func (h specRouteHandlers) routeContext(e *httpx.Event) (specRouteContext, bool, error) {
	stackID, ok, err := specStackIDFromPath(e)
	if !ok || err != nil {
		return specRouteContext{}, ok, err
	}

	ownerID, authErr := requireStackAuth(e)
	if authErr != nil || ownerID == "" {
		return specRouteContext{}, false, authErr
	}

	stack, err := h.app.FindRecordById(specStacksCollection, stackID)
	if err != nil {
		return specRouteContext{}, false, httpx.NotFound(e, specStackNotFoundMessage)
	}
	if stack.GetString(specOwnerIDField) != ownerID {
		return specRouteContext{}, false, httpx.Forbidden(e, specStackNotOwnedMessage)
	}

	persister, err := unifier.NewSpecPersister(stackID)
	if err != nil {
		return specRouteContext{}, false, specInternalError(e, "failed to initialize persister", err)
	}
	return specRouteContext{stackID: stackID, persister: persister}, true, nil
}

func specStackIDFromPath(e *httpx.Event) (string, bool, error) {
	stackID := e.Request.PathValue(specPathStackIDKey)
	if stackID == "" {
		return "", false, httpx.BadRequest(e, specStackIDRequiredMessage)
	}
	return stackID, true, nil
}

func specInternalError(e *httpx.Event, message string, err error) error {
	return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, message+": "+err.Error(), nil)
}

func specIntentResponse(stackID string, data []byte) map[string]any {
	return map[string]any{
		specResponseStackIDKey:    stackID,
		specResponseFilenameKey:   unifier.IntentSpecFilename,
		specResponseEncodingKey:   specBase64Encoding,
		specResponseSHA256Key:     unifier.ComputeDataHash(data),
		specResponseByteLengthKey: len(data),
		specResponseContentKey:    base64.StdEncoding.EncodeToString(data),
	}
}

func specHashChainResponse(stackID string, valid bool) map[string]any {
	message := specHashChainValidMessage
	if !valid {
		message = specHashChainMismatchMessage
	}
	return map[string]any{
		specResponseStackIDKey: stackID,
		specResponseValidKey:   valid,
		specResponseMessageKey: message,
	}
}

func specPipelineStatusResponse(stackID string, persister *unifier.SpecPersister) map[string]any {
	intentExists := persister.IntentExists()
	requirementsExists := persister.RequirementsSpecExists()
	unifiedExists := persister.UnifiedSpecExists()

	status := map[string]any{
		specResponseStackIDKey:       stackID,
		specResponseIntentExistsKey:  intentExists,
		specResponseRequirementsKey:  requirementsExists,
		specResponseUnifiedExistsKey: unifiedExists,
		specResponseBaseDirKey:       persister.BaseDir,
	}

	if intentExists {
		if data, err := persister.LoadIntentBytes(); err == nil {
			status[specResponseIntentHashKey] = unifier.ComputeDataHash(data)
			status[specResponseIntentByteLenKey] = len(data)
		}
	}
	if requirementsExists {
		if spec, err := persister.LoadRequirementsSpec(); err == nil {
			status[specResponseRequirementsMeta] = spec.Metadata
		}
	}
	if unifiedExists {
		if spec, err := persister.LoadUnifiedSpec(); err == nil {
			status[specResponseUnifiedMetaKey] = spec.PipelineInfo
		}
	}
	if requirementsExists && unifiedExists {
		valid, _ := persister.VerifyHashChain()
		status[specResponseHashChainValidKey] = valid
	}

	return status
}
