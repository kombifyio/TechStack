package routes

import (
	"net/http"
	"sort"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func (api *UnifierAPI) handleListStackKits(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	kits := api.engine.ListStackKits()

	result := make([]*unifier.StackKitInfo, 0, len(kits))
	for _, kit := range kits {
		info, err := api.engine.GetStackKitInfo(kit)
		if err != nil {
			result = append(result, &unifier.StackKitInfo{
				Name:        kit,
				DisplayName: kit,
				Version:     "1.0.0",
				Description: "StackKit: " + kit,
				Tags:        []string{},
				Deprecated:  false,
			})
			continue
		}
		result = append(result, info)
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
		return httpx.NotFound(e, "StackKit not found: "+err.Error())
	}
	return httpx.Success(e, http.StatusOK, info)
}

func (api *UnifierAPI) handleListAddons(e *httpx.Event) error {
	if _, err := requireUnifierAuth(e); err != nil {
		return err
	}
	loader := unifier.NewAddonSchemaLoader()
	if err := loader.LoadEmbedded(); err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Add-on catalog could not be loaded", err.Error())
	}

	names := loader.ListNames()
	sort.Strings(names)
	result := make([]map[string]any, 0, len(names))
	for _, name := range names {
		addon, err := loader.GetLoadedAddon(name)
		if err != nil {
			continue
		}
		result = append(result, map[string]any{
			"name":        addon.Name,
			"displayName": addon.DisplayName,
			"description": addon.Description,
			"priority":    addon.Priority,
		})
	}
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
