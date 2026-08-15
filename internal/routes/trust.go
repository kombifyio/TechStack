// Package routes provides REST API routes for kombifyTechstack.
// Trust routes have been refactored into the trust/ subpackage.
package routes

import (
	"github.com/pocketbase/pocketbase/core"

	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type TrustRouteStores = trust.RouteStores

func RegisterTrustRoutesWithStores(r *httpx.Router, app core.App, stores TrustRouteStores) { // pocketbase-migration-compat: legacy app bridge while trust stores are wired
	trust.RegisterRoutesWithStores(r, app, stores)
}
