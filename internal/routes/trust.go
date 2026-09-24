// Package routes provides REST API routes for kombifyTechstack.
// Trust routes have been refactored into the trust/ subpackage.
package routes

import (
	"github.com/kombifyio/techstack/internal/routes/trust"
	"github.com/kombifyio/techstack/pkg/httpx"
)

type TrustRouteStores = trust.RouteStores

func RegisterTrustRoutesWithStores(r *httpx.Router, stores TrustRouteStores) {
	trust.RegisterRoutesWithStores(r, stores)
}
