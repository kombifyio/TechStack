package routes

import (
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func sshHostForWorker(w kscore.Worker) string {
	if w.Tags != nil {
		if ip := strings.TrimSpace(w.Tags["ip"]); ip != "" {
			return ip
		}
	}
	return w.Name
}

func nodeSpecFromWorker(w kscore.Worker) kscore.NodeSpec {
	host := sshHostForWorker(w)
	return kscore.NodeSpec{
		Name:     w.Name,
		Type:     "worker",
		Provider: w.Provider,
		SSH: &kscore.SSHConfig{
			Host: host,
			Port: 22,
			User: "root",
		},
	}
}

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
			spec = &kscore.KombinationSpec{Name: "default-stack"}
		} else {
			spec = unifier.NormalizeInputSpec(input)
			if spec == nil {
				spec = &kscore.KombinationSpec{Name: "default-stack"}
			}
		}
	}

	requirements, err := api.engine.Analyze(spec)
	if err != nil {
		return httpx.Success(e, http.StatusOK, map[string]any{
			"stackKit":            registryBaseKit,
			"detectedAddons":      []string{},
			"requiredWorkers":     map[string]any{"minLocalServers": 1, "minRAM": 1024, "minCPU": 1},
			"requiredCredentials": []any{},
			"requiredPreChecks":   []any{},
			"appliedDefaults":     map[string]any{"kit": registryBaseKit},
			"description":         "Default stack using Basement Kit.",
			"intentName":          spec.Name,
			"_warning":            err.Error(),
		})
	}
	return httpx.Success(e, http.StatusOK, requirements)
}

func (api *UnifierAPI) handleIaCGeneration(e *httpx.Event) error {
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
	if len(body) == 0 {
		return httpx.BadRequest(e, "request body cannot be empty")
	}

	input, err := api.loader.LoadInputBytes(body)
	if err != nil {
		return httpx.BadRequest(e, "parse error: "+err.Error())
	}
	spec := unifier.NormalizeInputSpec(input)
	if spec == nil {
		return httpx.BadRequest(e, "could not parse spec")
	}

	mode := e.Request.URL.Query().Get("mode")
	if mode == "" {
		mode = "simple"
	}
	api.extendedPipeline.WithMode(mode)

	if len(spec.Nodes) == 0 {
		workers, _ := api.fetchWorkersFromDB(ownerID)
		for _, w := range workers {
			spec.Nodes = append(spec.Nodes, nodeSpecFromWorker(w))
		}
	}

	result := api.extendedPipeline.ExecuteWithIaC(spec)

	response := map[string]any{
		"success":      result.Success,
		"mode":         mode,
		"outputDir":    result.OutputDir,
		"steps":        result.Steps,
		"warnings":     result.Warnings,
		"failedStep":   result.FailedStep,
		"errorMessage": result.ErrorMessage,
	}
	if result.IaCOutput != nil {
		files := make([]map[string]any, 0)
		for name, content := range result.IaCOutput.Files {
			files = append(files, map[string]any{"name": name, "content": content, "size": len(content)})
		}
		response["generatedFiles"] = files
	}
	if result.UnifiedSpec != nil {
		response["unifiedSpec"] = result.UnifiedSpec
	}

	statusCode := http.StatusOK
	if !result.Success {
		statusCode = http.StatusBadRequest
	}
	return httpx.Success(e, statusCode, response)
}

func (api *UnifierAPI) handleIaCPreview(e *httpx.Event) error {
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
	if len(body) == 0 {
		return httpx.BadRequest(e, "request body cannot be empty")
	}

	input, err := api.loader.LoadInputBytes(body)
	if err != nil {
		return httpx.BadRequest(e, "parse error: "+err.Error())
	}
	spec := unifier.NormalizeInputSpec(input)
	if spec == nil {
		return httpx.BadRequest(e, "could not parse spec")
	}

	mode := e.Request.URL.Query().Get("mode")
	if mode == "" {
		mode = "simple"
	}
	api.extendedPipeline.WithMode(mode)

	if len(spec.Nodes) == 0 {
		workers, _ := api.fetchWorkersFromDB(ownerID)
		for _, w := range workers {
			spec.Nodes = append(spec.Nodes, nodeSpecFromWorker(w))
		}
	}

	result, err := api.extendedPipeline.ValidateAndPreviewIaC(spec)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "preview failed: "+err.Error(), nil)
	}

	response := map[string]any{"success": result.Success, "mode": mode, "steps": result.Steps, "warnings": result.Warnings, "outputDir": result.OutputDir}
	if result.IaCOutput != nil {
		files := make([]map[string]any, 0)
		for name, content := range result.IaCOutput.Files {
			lang := "hcl"
			if strings.HasSuffix(name, ".json") {
				lang = "json"
			}
			files = append(files, map[string]any{"name": name, "content": content, "size": len(content), "language": lang})
		}
		response["files"] = files
	}
	if result.ResolveResult != nil {
		response["stackKit"] = result.ResolveResult.StackKit
	}
	if result.DetectionResult != nil {
		addons := make([]string, 0)
		for _, a := range result.DetectionResult.Addons {
			addons = append(addons, a.Name)
		}
		response["addons"] = addons
	}

	statusCode := http.StatusOK
	if !result.Success {
		statusCode = http.StatusBadRequest
	}
	return httpx.Success(e, statusCode, response)
}
