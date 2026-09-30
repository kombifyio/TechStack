package routes

import (
	"context"
	"net/http"

	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	// managedServersBudgetKey is the register/signed-budget key whose usage
	// Techstack owns: the count of managed-server slots the subject holds.
	managedServersBudgetKey  = "cloud.runtime.credits#managed_servers"
	managedServersBudgetUnit = "server"
)

var usageBudgetsSurface = cloudReadSurface{
	errorPrefix: "techstack.usage_budgets.", unavailableReason: "usage_budgets_unavailable",
	title: "Budget usage unavailable", body: "Techstack could not report managed-server usage for this account safely.",
}

// ManagedServersHeldFunc counts the managed-server slots one owner holds in
// one tenant, exactly as managed-runtime admission counts them.
type ManagedServersHeldFunc func(ctx context.Context, tenantID, ownerSubjectID string) (int, error)

// budgetUsage is one entry of the live usage read model Cloud aggregates into
// GET /api/v1/usage/budgets. Techstack reports usage only: Limit, Period and
// ResetsAt stay null because a service call carries no Gateway-signed budget;
// Cloud fills the limit from plan-entitlement-budgets.json.
type budgetUsage struct {
	Key      string  `json:"key"`
	Used     int     `json:"used"`
	Limit    *int    `json:"limit"`
	Unit     string  `json:"unit"`
	Period   *string `json:"period"`
	ResetsAt *string `json:"resets_at"`
}

type budgetUsageResponse struct {
	Budgets []budgetUsage `json:"budgets"`
}

// httpInternalUsageBudgets is the Cloud servicecall read behind the budget
// panel, authenticated exactly like the runtime summary.
func (h inventoryHandlers) httpInternalUsageBudgets(e *httpx.Event) error {
	return h.serveCloudRead(e, usageBudgetsSurface, h.serveUsageBudgets)
}

func (h inventoryHandlers) serveUsageBudgets(e *httpx.Event, ctx context.Context, scope inventoryScope) {
	// An unknown count is never reported as zero held servers.
	if h.managedServersHeld == nil {
		_ = usageBudgetsSurface.deny(e, http.StatusServiceUnavailable, usageBudgetsSurface.unavailableReason, true,
			"Retry after Techstack managed-runtime capacity is available.")
		return
	}
	held, err := h.managedServersHeld(ctx, scope.tenantID, scope.ownerID)
	if err != nil {
		_ = usageBudgetsSurface.deny(e, http.StatusServiceUnavailable, usageBudgetsSurface.unavailableReason, true,
			"Retry shortly; Techstack could not read the managed-server count.")
		return
	}
	_ = e.JSON(http.StatusOK, budgetUsageResponse{Budgets: []budgetUsage{{
		Key: managedServersBudgetKey, Used: held, Unit: managedServersBudgetUnit,
	}}})
}
