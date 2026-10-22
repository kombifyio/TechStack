package routes

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"
	commonedgeauth "github.com/kombifyio/techstack/internal/gocommon/edgeauth"

	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/middleware"
)

// dataProfileResponse is the data protection profile that applies to the
// signed-in user, which every product shows (DATA-ARCHITECTURE.md §5.4).
type dataProfileResponse struct {
	Res string `json:"res"`
	Ret string `json:"ret"`
	Loc string `json:"loc"`
	Rev int64  `json:"rev"`
	// Source is "edge" for the Gateway-signed profile of a hosted account and
	// "boot" for the edition profile of a self-hosted install.
	Source string `json:"source"`
}

// RegisterDataProfileRoutes adds GET /api/v1/data-profile. A hosted request
// returns the profile the Gateway signed for this request; without one it
// answers the data_profile_unresolvable denial, never Standard. A self-hosted
// install returns its boot profile. While hosted verification is off
// (TECHSTACK_DATA_PROFILE_MODE) the route answers 404 so the UI shows nothing.
func RegisterDataProfileRoutes(r *httpx.Router, mode config.DeploymentMode) {
	selfHosted := !mode.IsSaaS()
	r.GET("/api/v1/data-profile", func(e *httpx.Event) error {
		if _, _, ok := authenticatedUser(e); !ok {
			return httpx.Unauthorized(e, "")
		}
		e.Response.Header().Set("Cache-Control", "no-store")
		if !selfHosted && !middleware.DataProfileVerificationActive() {
			// Not rolled out on this deployment: nothing to show, and not a
			// denial either, because no request depends on it yet.
			return httpx.NotFound(e, "data protection profile is not active on this deployment")
		}
		profile, ok := middleware.DataProfileFromContext(e.Request.Context(), selfHosted)
		if !ok {
			envelope := dataprofile.UnresolvableDenial(strings.TrimSpace(e.Request.Header.Get(commonedgeauth.HeaderRequestID)))
			e.Response.Header().Set("Content-Type", "application/json")
			e.Response.WriteHeader(http.StatusForbidden)
			return json.NewEncoder(e.Response).Encode(envelope)
		}
		source := "edge"
		if selfHosted {
			source = "boot"
		}
		return httpx.Success(e, http.StatusOK, dataProfileResponse{
			Res: string(profile.Res), Ret: string(profile.Ret), Loc: string(profile.Loc), Rev: profile.Rev, Source: source,
		})
	})
}
