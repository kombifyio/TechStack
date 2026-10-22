package middleware

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"
	commonedgeauth "github.com/kombifyio/techstack/internal/gocommon/edgeauth"

	"github.com/kombifyio/techstack/pkg/fulllocal"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// Data protection profile of a hosted request (workspace
// platform/standards/DATA-ARCHITECTURE.md section 5.3, Beads
// platform-m09kf.1.11). The Gateway resolves the subject's profile and signs
// it as the x-kombify-profile header set, bound to the identity envelope of
// the same request (kombify-Gateway data-profile.ts, EDGE_PROFILE_SECRET).
//
// TECHSTACK_DATA_PROFILE_MODE stages the rollout:
//   - off (default): profile headers are dropped and nothing is read.
//   - observe: a present profile is verified and attached to the request
//     context; a missing or invalid one is logged and the request continues
//     without a profile.
//   - enforce: a Techstack request without a valid profile is refused with
//     the data_profile_unresolvable denial. The Gateway must sign every
//     Techstack request first (DATA_PROFILE_SERVICES) or this locks users out.
//
// A self-hosted edition never reads these headers; its profile is the boot
// profile (pkg/fulllocal).
const (
	dataProfileModeEnv     = "TECHSTACK_DATA_PROFILE_MODE"
	dataProfileModeOff     = "off"
	dataProfileModeObserve = "observe"
	dataProfileModeEnforce = "enforce"
)

func dataProfileMode() string {
	switch mode := strings.ToLower(strings.TrimSpace(os.Getenv(dataProfileModeEnv))); mode {
	case dataProfileModeObserve, dataProfileModeEnforce:
		return mode
	default:
		return dataProfileModeOff
	}
}

// DataProfileVerificationActive reports whether hosted requests have their
// Gateway profile verified (TECHSTACK_DATA_PROFILE_MODE observe or enforce).
func DataProfileVerificationActive() bool {
	return dataProfileMode() != dataProfileModeOff
}

func hasAnyProfileHeader(r *http.Request) bool {
	for _, header := range []string{
		commonedgeauth.HeaderProfile,
		commonedgeauth.HeaderProfileSignature,
		commonedgeauth.HeaderProfileTimestamp,
		commonedgeauth.HeaderProfileKeyID,
	} {
		if strings.TrimSpace(r.Header.Get(header)) != "" {
			return true
		}
	}
	return false
}

func stripProfileHeaders(r *http.Request) {
	r.Header.Del(commonedgeauth.HeaderProfile)
	r.Header.Del(commonedgeauth.HeaderProfileSignature)
	r.Header.Del(commonedgeauth.HeaderProfileTimestamp)
	r.Header.Del(commonedgeauth.HeaderProfileKeyID)
}

// attachDataProfile runs after the identity envelope verified. It returns
// false when it already answered the request with a denial.
func attachDataProfile(e *httpx.Event, cfg EdgeIdentityConfig) bool {
	mode := dataProfileMode()
	if mode == dataProfileModeOff {
		stripProfileHeaders(e.Request)
		return true
	}
	service := strings.TrimSpace(e.Request.Header.Get(headerEdgeService))
	if service != techStackDecisionAudience {
		// Only the Techstack route is opted in at the Gateway; the other
		// services sharing this origin carry no profile yet.
		stripProfileHeaders(e.Request)
		return true
	}
	var err error
	profile := dataprofile.Profile{}
	if hasAnyProfileHeader(e.Request) {
		profile, _, err = commonedgeauth.VerifyProfileHeaders(e.Request, commonedgeauth.ProfileVerifyConfig{
			ExpectedAudience:     techStackDecisionAudience,
			ExpectedPublicPrefix: techStackDecisionPublicPrefix,
			SignatureWindow:      cfg.EdgeSignatureWindow,
			IdentityConfig: commonedgeauth.Config{
				EdgeAuthSecret:     firstNonEmpty(cfg.EdgeAuthSecret, os.Getenv("EDGE_AUTH_SECRET")),
				EdgeAuthNextSecret: firstNonEmpty(cfg.EdgeAuthNextSecret, os.Getenv("EDGE_AUTH_SECRET_NEXT")),
				EdgeAuthKeyID:      firstNonEmpty(cfg.EdgeAuthKeyID, os.Getenv("EDGE_AUTH_KEY_ID"), defaultEdgeKeyID),
				EdgeAuthNextKeyID:  firstNonEmpty(cfg.EdgeAuthNextKeyID, os.Getenv("EDGE_AUTH_KEY_ID_NEXT"), defaultEdgeNextKeyID),
				SignatureWindow:    cfg.EdgeSignatureWindow,
				ExpectedServices:   edgeExpectedServices,
			},
		})
	} else {
		err = errDataProfileMissing
	}
	stripProfileHeaders(e.Request)
	if err == nil {
		e.Request = e.Request.WithContext(dataprofile.WithProfile(e.Request.Context(), profile))
		return true
	}
	slog.Warn("data_profile_unverified", "mode", mode, "error", err, "path", e.Request.URL.Path)
	if mode != dataProfileModeEnforce {
		return true
	}
	envelope := dataprofile.UnresolvableDenial(strings.TrimSpace(e.Request.Header.Get(commonedgeauth.HeaderRequestID)))
	e.Response.Header().Set("Content-Type", "application/json")
	e.Response.WriteHeader(http.StatusForbidden)
	_ = json.NewEncoder(e.Response).Encode(envelope)
	return false
}

type dataProfileError string

func (e dataProfileError) Error() string { return string(e) }

const errDataProfileMissing = dataProfileError("edge_profile_missing")

// DataProfileFromContext returns the data protection profile that applies to
// this request: the verified Gateway profile of a hosted request, or the boot
// profile of a self-hosted edition. ok is false for a hosted request without
// a verified profile; callers that must protect content treat that as a
// denial, never as Standard.
func DataProfileFromContext(ctx context.Context, selfHosted bool) (dataprofile.Profile, bool) {
	if selfHosted {
		return fulllocal.Profile(), true
	}
	return dataprofile.FromContext(ctx)
}
