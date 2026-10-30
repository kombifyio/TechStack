package routes

import (
	"errors"
	"net/http"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/autoinstall"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
)

// RegisterAutoInstallRoutes registers the host dependency endpoints. They
// read and change the machine this runtime runs on, so only the self-hosted
// owner may call them. Every other edition runs on the shared hosted control
// plane: there both routes refuse every caller before any detection or
// install (platform-vijkk).
func RegisterAutoInstallRoutes(r *httpx.Router, log *logger.Logger, edition config.Edition) {
	if edition != config.EditionSelfHostOSS {
		r.GET("/api/v1/autoinstall/status", refuseHostedAutoInstall)
		r.POST("/api/v1/autoinstall/install", refuseHostedAutoInstall)
		return
	}
	detector := autoinstall.NewDetector(log)

	r.GET("/api/v1/autoinstall/status", func(re *httpx.Event) error {
		if err := requireHostOwner(re); err != nil {
			return err
		}
		return handleAutoInstallStatus(re, detector, log)
	})

	r.POST("/api/v1/autoinstall/install", func(re *httpx.Event) error {
		if err := requireHostOwner(re); err != nil {
			return err
		}
		return handleAutoInstallInstall(re, detector, log)
	})
}

// requireHostOwner admits the self-hosted owner (the local admin) only; a
// signed-in session alone does not own the host.
func requireHostOwner(re *httpx.Event) error {
	if _, err := requireAuth(re); err != nil {
		return err
	}
	return requireAdmin(re)
}

func refuseHostedAutoInstall(re *httpx.Event) error {
	failure := ksapi.StructuredError{
		Status: http.StatusForbidden, Message: "Hosted Techstack does not install software on its host",
		ErrorCode: "host_install_unavailable", ReasonCode: "hosted_control_plane",
		Guidance: ksapi.UserGuidance{
			Title: "Not available on hosted Techstack",
			Body:  "Dependencies are installed only by the owner of a self-hosted Techstack.",
		},
	}
	return httpx.Error(re, failure.Status, failure.Code(), failure.Message, failure.Details())
}

// handleAutoInstallStatus checks for missing dependencies.
func handleAutoInstallStatus(re *httpx.Event, detector autoinstall.Detector, log *logger.Logger) error {
	ctx := re.Request.Context()

	missing, err := detector.Detect(ctx)
	if err != nil {
		log.Error("Failed to detect dependencies", "error", err)
		return httpx.Error(re, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to detect dependencies", nil)
	}

	return httpx.Success(re, http.StatusOK, map[string]any{
		"missing": missing,
		"count":   len(missing),
	})
}

// InstallRequest represents an install request.
type InstallRequest struct {
	Dependencies []string `json:"dependencies"`
	Strategy     string   `json:"strategy"` // "auto", "package_manager", "download"
}

// handleAutoInstallInstall installs missing dependencies.
func handleAutoInstallInstall(re *httpx.Event, detector autoinstall.Detector, log *logger.Logger) error {
	ctx := re.Request.Context()

	var req InstallRequest
	if err := re.BindBody(&req); err != nil {
		return httpx.Error(re, http.StatusBadRequest, ksapi.ErrCodeValidation, "Invalid request", nil)
	}

	// Detect missing
	missing, err := detector.Detect(ctx)
	if err != nil {
		return httpx.Error(re, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to detect dependencies", nil)
	}

	// Filter by requested dependencies
	var toInstall []autoinstall.MissingDependency
	if len(req.Dependencies) > 0 {
		depMap := make(map[string]bool)
		for _, name := range req.Dependencies {
			depMap[name] = true
		}

		for _, m := range missing {
			if depMap[m.Name] {
				toInstall = append(toInstall, m)
			}
		}
	} else {
		toInstall = missing
	}

	if len(toInstall) == 0 {
		return httpx.Success(re, http.StatusOK, map[string]any{
			"message": "No dependencies to install",
			"results": []autoinstall.InstallResult{},
		})
	}

	// Install all
	results, err := detector.InstallAll(ctx, toInstall)
	if err != nil {
		return httpx.Error(re, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Installation failed", nil)
	}

	// Check for failures
	responses := make([]autoInstallResultResponse, 0, len(results))
	var failed []autoInstallResultResponse
	for _, r := range results {
		response := publicAutoInstallResult(r)
		responses = append(responses, response)
		if !r.Success {
			failed = append(failed, response)
		}
	}

	if len(failed) > 0 {
		return httpx.Success(re, http.StatusPartialContent, map[string]any{
			"message": "Some installations failed",
			"results": responses,
			"failed":  failed,
		})
	}

	return httpx.Success(re, http.StatusOK, map[string]any{
		"message": "All dependencies installed successfully",
		"results": responses,
	})
}

// Internal errors are not wire values: encoding an error interface produces
// an empty object, while printing it can expose a URL or command detail.
type autoInstallResultResponse struct {
	Dependency    autoinstall.Dependency
	Success       bool
	Version       string
	InstalledPath string
	Error         *ksapi.ErrorDetail
	Strategy      autoinstall.InstallStrategy
	Message       string
}

func publicAutoInstallResult(result autoinstall.InstallResult) autoInstallResultResponse {
	response := autoInstallResultResponse{
		Dependency: result.Dependency, Success: result.Success,
		Version: result.Version, InstalledPath: result.InstalledPath,
		Strategy: result.Strategy, Message: result.Message,
	}
	if result.Success {
		return response
	}
	failure := ksapi.StructuredError{
		Status: http.StatusInternalServerError, Message: "Dependency installation failed",
		ErrorCode: "dependency_installation_failed", ReasonCode: "dependency_installation_failed",
		Guidance: ksapi.UserGuidance{
			Title: "Dependency installation failed",
			Body:  "Check the local installation log and dependency requirements.",
		},
	}
	if errors.Is(result.Error, autoinstall.ErrRemoteInstallDisabled) || dataprofile.IsDenied(result.Error) {
		failure.Status = http.StatusForbidden
		failure.Message = "Dependency installation blocked by Full Local"
		failure.ErrorCode = "data_profile_denied"
		failure.ReasonCode = dataprofile.ReasonFullLocal
		failure.Guidance = ksapi.UserGuidance{
			Title: "Full Local blocks the download",
			Body:  "Remote downloads are disabled by this data protection profile. Provide the dependency locally.",
		}
	}
	response.Error = &ksapi.ErrorDetail{Code: failure.Code(), Message: failure.Message, Details: failure.Details()}
	response.Message = failure.Message
	return response
}
