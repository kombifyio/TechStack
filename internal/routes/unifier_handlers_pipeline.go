package routes

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
)

func (api *UnifierAPI) handlePipeline(e *httpx.Event) error {
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
		return httpx.BadRequest(e, "request body cannot be empty; send a valid kombination.yaml or JSON configuration")
	}

	input, err := api.loader.LoadInputBytes(body)
	if err != nil {
		return httpx.BadRequest(e, "could not parse configuration: "+err.Error())
	}

	result := api.pipeline.ExecuteInput(input)

	statusCode := http.StatusOK
	if !result.Success {
		switch result.FailedStep {
		case "pre-validation", "stackkit-resolution", "environment-eligibility", "stackkit-profile-resolution":
			statusCode = http.StatusBadRequest
		case "unifier-engine":
			if result.ValidationResult != nil && !result.ValidationResult.Valid {
				statusCode = http.StatusBadRequest
			} else {
				statusCode = http.StatusInternalServerError
			}
		default:
			statusCode = http.StatusInternalServerError
		}
	}

	response := map[string]any{
		"success":      result.Success,
		"totalTime":    result.TotalTime.String(),
		"totalTimeMs":  result.TotalTime.Milliseconds(),
		"steps":        result.Steps,
		"warnings":     result.Warnings,
		"failedStep":   result.FailedStep,
		"errorMessage": result.ErrorMessage,
	}
	if result.ValidationResult != nil {
		response["validation"] = map[string]any{"valid": result.ValidationResult.Valid, "errors": result.ValidationResult.Errors}
	}
	if result.ResolveResult != nil {
		response["stackKitResolution"] = map[string]any{
			"stackKit":        result.ResolveResult.StackKit,
			"reason":          result.ResolveResult.Reason,
			"autoSelected":    result.ResolveResult.AutoSelected,
			"confidence":      result.ResolveResult.Confidence,
			"alternativeKits": result.ResolveResult.AlternativeKits,
			"valid":           result.ResolveResult.Valid,
		}
	}
	if result.DetectionResult != nil {
		response["addonDetection"] = map[string]any{
			"addons":       result.DetectionResult.Addons,
			"skippedCount": result.DetectionResult.SkippedCount,
			"totalChecked": result.DetectionResult.TotalChecked,
		}
	}
	if result.UnifiedSpec != nil {
		response["unifiedSpec"] = result.UnifiedSpec
	}
	return httpx.Success(e, statusCode, response)
}

func (api *UnifierAPI) handlePipelinePreview(e *httpx.Event) error {
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

	input, decisionContext, _, err := api.loader.LoadInputEnvelopeBytes(body)
	if err != nil {
		return httpx.BadRequest(e, "parse error: "+err.Error())
	}

	result := api.pipeline.ExecuteInputWithDecisionContext(input, decisionContext)

	response := buildPipelinePreviewResponse(result)
	return httpx.Success(e, http.StatusOK, response)
}

func buildPipelinePreviewResponse(result *unifier.PipelineResult) map[string]any {
	errors := pipelinePreviewErrors(result)
	valid := pipelinePreviewValid(result)
	response := map[string]any{
		"valid":             valid,
		"resolved_stackkit": pipelinePreviewResolvedStackKit(result),
		"detected_addons":   pipelinePreviewDetectedAddons(result),
		"stages":            pipelinePreviewStages(result),
		"warnings":          result.Warnings,
	}
	if result.RequirementsSpec != nil {
		response["requirements"] = result.RequirementsSpec
		response["environment_gaps"] = result.RequirementsSpec.EnvironmentGaps
		response["guidance"] = result.RequirementsSpec.Guidance
	}
	if result.DecisionContext != nil {
		response["decision_context"] = result.DecisionContext
		response["decision_context_hash"] = unifier.HashDecisionContext(result.DecisionContext)
	}
	if result.DecisionTrace != nil {
		response["decision_trace"] = result.DecisionTrace
		response["decision_trace_hash"] = unifier.HashDecisionTrace(result.DecisionTrace)
	}
	if len(errors) > 0 {
		response["errors"] = errors
	}
	if valid && result.UnifiedSpec != nil {
		response["config"] = result.UnifiedSpec
	}
	return response
}

func pipelinePreviewStages(result *unifier.PipelineResult) []map[string]any {
	stages := make([]map[string]any, 0, len(result.Steps))
	for _, step := range result.Steps {
		stages = append(stages, map[string]any{
			backupNamePathKey:   step.Name,
			preCheckStatusField: step.Status,
			"duration_ms":       step.Duration.Milliseconds(),
			routeErrorField:     step.Error,
		})
	}
	return stages
}

func pipelinePreviewDetectedAddons(result *unifier.PipelineResult) []string {
	detectedAddons := make([]string, 0)
	if result.DetectionResult != nil {
		for _, addon := range result.DetectionResult.Addons {
			detectedAddons = append(detectedAddons, addon.Name)
		}
	}
	return detectedAddons
}

func pipelinePreviewResolvedStackKit(result *unifier.PipelineResult) string {
	resolvedStackKit := registryBaseKit
	if result.ResolveResult != nil && result.ResolveResult.StackKit != "" {
		resolvedStackKit = result.ResolveResult.StackKit
	}
	return resolvedStackKit
}

func pipelinePreviewValid(result *unifier.PipelineResult) bool {
	valid := result.Success
	if result.ValidationResult != nil && !result.ValidationResult.Valid {
		valid = false
	}
	if result.ResolveResult != nil && !result.ResolveResult.Valid {
		valid = false
	}
	if result.DecisionTrace != nil && len(result.DecisionTrace.BlockingGaps) > 0 {
		valid = false
	}
	return valid
}

func pipelinePreviewErrors(result *unifier.PipelineResult) []map[string]string {
	errors := make([]map[string]string, 0)
	if result.ValidationResult != nil && !result.ValidationResult.Valid {
		for _, ve := range result.ValidationResult.Errors {
			errors = append(errors, map[string]string{"path": ve.Path, "code": ve.Code, "message": ve.Message})
		}
	}
	if result.ResolveResult != nil && !result.ResolveResult.Valid {
		errors = append(errors, pipelinePreviewStackKitError(result))
	}
	if result.DecisionTrace != nil {
		for _, gap := range result.DecisionTrace.BlockingGaps {
			message := gap.Message
			if message == "" {
				message = gap.Code
			}
			errors = append(errors, map[string]string{
				"path":    "environment",
				"code":    gap.Code,
				"message": message,
			})
		}
	}
	if !result.Success && result.ErrorMessage != "" && len(errors) == 0 {
		path := result.FailedStep
		if path == "" {
			path = "pipeline"
		}
		errors = append(errors, map[string]string{"path": path, "code": "pipeline", "message": result.ErrorMessage})
	}
	return errors
}

func pipelinePreviewStackKitError(result *unifier.PipelineResult) map[string]string {
	message := result.ErrorMessage
	if message == "" {
		message = "StackKit resolution failed"
	}
	return map[string]string{"path": "stackkit", "code": "stackkit_resolution", "message": message}
}

var _ = unifier.NormalizeInputSpec
