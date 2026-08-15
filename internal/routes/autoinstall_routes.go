package routes

import (
	"net/http"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/autoinstall"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/pocketbase/pocketbase/core"
)

// RegisterAutoInstallRoutes registers auto-install endpoints.
func RegisterAutoInstallRoutes(r *httpx.Router, app core.App, log *logger.Logger) {
	_ = app
	detector := autoinstall.NewDetector(log)

	r.GET("/api/v1/autoinstall/status", func(re *httpx.Event) error {
		if _, err := requireAuth(re); err != nil {
			return err
		}
		return handleAutoInstallStatus(re, detector, log)
	})

	r.POST("/api/v1/autoinstall/install", func(re *httpx.Event) error {
		if _, err := requireAuth(re); err != nil {
			return err
		}
		return handleAutoInstallInstall(re, detector, log)
	})
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
	var failed []autoinstall.InstallResult
	for _, r := range results {
		if !r.Success {
			failed = append(failed, r)
		}
	}

	if len(failed) > 0 {
		return httpx.Success(re, http.StatusPartialContent, map[string]any{
			"message": "Some installations failed",
			"results": results,
			"failed":  failed,
		})
	}

	return httpx.Success(re, http.StatusOK, map[string]any{
		"message": "All dependencies installed successfully",
		"results": results,
	})
}
