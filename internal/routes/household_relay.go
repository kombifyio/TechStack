package routes

import (
	"net/http"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/serviceauth"
)

func RegisterHouseholdRelay(r *httpx.Router, hub *agentcontrol.Hub) {
	handle := func(e *httpx.Event) error {
		cfg, err := serviceauth.Apply(servicecall.Config{ServiceName: "techstack", Enabled: true, RequireEd25519: true,
			AllowedCallers: []string{agentcontrol.HouseholdRelayCaller}})
		if err != nil || hub == nil {
			http.Error(e.Response, "household relay unavailable", 503)
			return nil
		}
		// No shared-secret fallback, user OBO, dynamic receiver or scope expansion.
		cfg.Secret = ""
		cfg.SecretNext = ""
		servicecall.RequireBoundServiceAuth(servicecall.BoundRequestConfig{Config: cfg, RequiredScope: agentcontrol.HouseholdRelayScope, MaxBodyBytes: 128 << 10})(
			http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				caller := servicecall.FromContext(request.Context())
				if caller == nil || caller.ExpiresAt.Sub(caller.IssuedAt) > 2*time.Minute {
					http.Error(w, "relay lifetime denied", 403)
					return
				}
				hub.ServeHouseholdTransient(w, request)
			})).ServeHTTP(e.Response, e.Request)
		return nil
	}
	r.GET(agentcontrol.HouseholdRelayPath, handle)
	r.POST(agentcontrol.HouseholdRelayPath, handle)
}
