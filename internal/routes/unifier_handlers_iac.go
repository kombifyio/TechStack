package routes

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func (api *UnifierAPI) handleAnalyze(e *httpx.Event) error {
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

	var spec *kscore.KombinationSpec
	if len(body) == 0 {
		spec = &kscore.KombinationSpec{Name: "default-stack"}
	} else {
		input, err := api.loader.LoadInputBytes(body)
		if err != nil {
			return httpx.BadRequest(e, "parse error: "+err.Error())
		} else {
			spec = unifier.NormalizeInputSpec(input)
			if spec == nil {
				spec = &kscore.KombinationSpec{Name: "default-stack"}
			}
		}
	}

	requirements, err := api.engine.Analyze(spec)
	if err != nil {
		return httpx.Error(e, http.StatusUnprocessableEntity, ksapi.ErrCodeBadRequest,
			"StackKits requirements could not be resolved", err.Error())
	}
	return httpx.Success(e, http.StatusOK, requirements)
}

func rejectRetiredUnifierIaC(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	return httpx.Error(
		e,
		http.StatusGone,
		ksapi.ErrCodeBadRequest,
		"direct Unifier IaC generation is retired; use the pinned StackKits CLI path",
		nil,
	)
}

func (api *UnifierAPI) handleIaCGeneration(e *httpx.Event) error {
	return rejectRetiredUnifierIaC(e)
}

func (api *UnifierAPI) handleIaCPreview(e *httpx.Event) error {
	return rejectRetiredUnifierIaC(e)
}
