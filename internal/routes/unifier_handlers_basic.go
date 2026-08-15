package routes

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func (api *UnifierAPI) handleValidate(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	body, tooLarge, err := readRequestBodyLimited(e.Request.Body, maxUnifierRequestBodyBytes)
	if err != nil {
		return httpx.BadRequest(e, "failed to read request body")
	}
	if tooLarge {
		return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "request body exceeds size limit", nil)
	}
	if len(body) == 0 {
		return httpx.BadRequest(e, "request body cannot be empty")
	}

	input, err := api.loader.LoadInputBytes(body)
	if err != nil {
		return httpx.BadRequest(e, "parse error: "+err.Error())
	}

	spec := unifier.NormalizeInputSpec(input)
	if spec == nil {
		return httpx.BadRequest(e, "spec cannot be nil")
	}

	result, err := api.pipeline.PreValidate(spec)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "validation error: "+err.Error(), nil)
	}

	return httpx.Success(e, http.StatusOK, map[string]any{
		"valid":  result.Valid,
		"errors": result.Errors,
	})
}

func (api *UnifierAPI) handleUnify(e *httpx.Event) error {
	ownerID, err := requireUnifierAuth(e)
	if err != nil {
		return err
	}
	body, tooLarge, err := readRequestBodyLimited(e.Request.Body, maxUnifierRequestBodyBytes)
	if err != nil {
		return httpx.BadRequest(e, "failed to read request body")
	}
	if tooLarge {
		return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "request body exceeds size limit", nil)
	}

	input, err := api.loader.LoadInputBytes(body)
	if err != nil {
		return httpx.BadRequest(e, "invalid spec: "+err.Error())
	}

	spec := unifier.NormalizeInputSpec(input)
	if spec == nil {
		return httpx.BadRequest(e, "spec cannot be nil")
	}

	if len(spec.Nodes) == 0 {
		return httpx.BadRequest(e, "Cannot unify without nodes. Nodes may be added later via agent registration; use /api/v1/unifier/pipeline for partial validation.")
	}

	workers, err := api.fetchWorkersFromDB(ownerID)
	if err != nil {
		workers = []kscore.Worker{}
	}
	unified, err := api.engine.Unify(spec, workers)
	if err != nil {
		return httpx.BadRequest(e, "unification failed: "+err.Error())
	}

	return httpx.Success(e, http.StatusOK, unified)
}

func (api *UnifierAPI) handleGenerate(e *httpx.Event) error {
	ownerID, err := requireUnifierAuth(e)
	if err != nil {
		return err
	}
	body, tooLarge, err := readRequestBodyLimited(e.Request.Body, maxUnifierRequestBodyBytes)
	if err != nil {
		return httpx.BadRequest(e, "failed to read request body")
	}
	if tooLarge {
		return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "request body exceeds size limit", nil)
	}

	input, err := api.loader.LoadInputBytes(body)
	if err != nil {
		return httpx.BadRequest(e, "invalid spec: "+err.Error())
	}
	spec := unifier.NormalizeInputSpec(input)
	if spec == nil {
		return httpx.BadRequest(e, "spec cannot be nil")
	}
	if len(spec.Nodes) == 0 {
		return httpx.BadRequest(e, "Cannot generate tfvars without nodes. Nodes may be added later via agent registration.")
	}

	workers, err := api.fetchWorkersFromDB(ownerID)
	if err != nil {
		workers = []kscore.Worker{}
	}
	unified, err := api.engine.Unify(spec, workers)
	if err != nil {
		return httpx.BadRequest(e, "unification failed: "+err.Error())
	}

	generator := unifier.NewGenerator("")
	tfvars, err := generator.Generate(unified)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "generation failed: "+err.Error(), nil)
	}
	return httpx.Success(e, http.StatusOK, tfvars)
}
