package routes

import (
	"errors"
	"net/http"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/specv2"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func (api *UnifierAPI) handleListStackKits(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	result, err := api.engine.ListStackKitInfo()
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "StackKits metadata could not be read", err.Error())
	}

	return httpx.Success(e, http.StatusOK, result)
}

func (api *UnifierAPI) handleGetStackKit(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	name := e.Request.PathValue("name")
	if name == "" {
		return httpx.BadRequest(e, "StackKit name is required")
	}

	info, err := api.engine.GetStackKitInfo(name)
	if err != nil {
		if errors.Is(err, stackkitrelease.ErrKitMetadataNotFound) {
			return httpx.NotFound(e, "StackKit not found")
		}
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "StackKits metadata could not be read", err.Error())
	}
	return httpx.Success(e, http.StatusOK, info)
}

func (api *UnifierAPI) handleListAddons(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Add-on catalog could not be read", err.Error())
	}

	result := catalog.AddOnViews()
	return httpx.Success(e, http.StatusOK, map[string]any{"addons": result, "count": len(result)})
}

func (api *UnifierAPI) handleDetectAddons(e *httpx.Event) error {
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

	result := api.pipeline.DetectAddons(spec)
	return httpx.Success(e, http.StatusOK, map[string]any{
		"addons":       result.Addons,
		"skippedCount": result.SkippedCount,
		"totalChecked": result.TotalChecked,
	})
}

// handleUseCaseCatalog serves the StackKits use-case catalog. The Unifier and
// Wizard consume the image-published catalog when TECHSTACK_STACKKIT_USE_CASE_CATALOG
// is set, otherwise the catalog generated from the pinned StackKits module.
// They never load CUE from a STACKKITS_REPO checkout.
func (api *UnifierAPI) handleUseCaseCatalog(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"StackKits use-case catalog could not be read", err.Error())
	}
	// Deliverability comes from the same compatibility manifest that
	// AuthorGoals gates on, so the card cannot promise something the
	// authoring path will drop. Five of the ten catalogued use cases have no
	// standalone-compose delivery in the pinned release; the wizard offered
	// them with nothing said, and a selected undeliverable goal is stored as
	// an unmapped goal and activated later by design - so this reports the
	// fact rather than removing the choice.
	deliverable, deliverabilityKnown, deliverErr := specv2.DeliverableGoals()
	if deliverErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"StackKits delivery compatibility could not be read", deliverErr.Error())
	}
	deliveryStatuses, _, statusErr := specv2.GoalDeliveryStatuses()
	if statusErr != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal,
			"StackKits delivery compatibility could not be read", statusErr.Error())
	}

	return httpx.Success(e, http.StatusOK,
		stackkitrelease.CreationCatalogPayload(catalog, deliverable, deliverabilityKnown, deliveryStatuses))
}
