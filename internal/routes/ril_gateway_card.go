package routes

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/ril/actions"
	"github.com/kombifyio/techstack/pkg/serviceauth"
)

// RILGatewayCardPath is a private owner-scoped read for Gateway's decision adapter.
const RILGatewayCardPath = "/api/v1/internal/ril/decision-card"
const rilGatewayCardScope = "ril:decision-card:read"

// RegisterRILGatewayCardRoute exposes only persisted card facts to the Gateway's
// independently authenticated, request-bound service identity. The body names a
// card and owner/tenant selected by Gateway after verifying its caller.
func RegisterRILGatewayCardRoute(r *httpx.Router, authority actions.Authority) {
	r.POST(RILGatewayCardPath, func(e *httpx.Event) error {
		cfg, err := serviceauth.Apply(servicecall.Config{ServiceName: "techstack", Enabled: true, RequireEd25519: true, AllowedCallers: []string{"gateway"}})
		if err != nil || authority == nil {
			http.Error(e.Response, "authority unavailable", http.StatusServiceUnavailable)
			return nil
		}
		cfg.Secret, cfg.SecretNext = "", ""
		servicecall.RequireBoundServiceAuth(servicecall.BoundRequestConfig{Config: cfg, RequiredScope: rilGatewayCardScope, MaxBodyBytes: 4096})(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			caller := servicecall.FromContext(request.Context())
			if caller == nil || caller.Service != "gateway" || caller.ExpiresAt.Sub(caller.IssuedAt) <= 0 || caller.ExpiresAt.Sub(caller.IssuedAt) > time.Minute {
				http.Error(w, "identity denied", http.StatusForbidden)
				return
			}
			var body struct {
				CardID       string                    `json:"cardId"`
				OwnerSubject string                    `json:"ownerSubject"`
				TenantID     string                    `json:"tenantId"`
				Requester    actions.DecisionRequester `json:"requester"`
			}
			decoder := json.NewDecoder(io.LimitReader(request.Body, 4097))
			decoder.DisallowUnknownFields()
			if decoder.Decode(&body) != nil || strings.TrimSpace(body.CardID) != body.CardID || body.CardID == "" || len(body.CardID) > 192 || body.OwnerSubject == "" || strings.TrimSpace(body.OwnerSubject) != body.OwnerSubject || len(body.OwnerSubject) > 256 || body.TenantID == "" || strings.TrimSpace(body.TenantID) != body.TenantID || len(body.TenantID) > 256 || decoder.Decode(&struct{}{}) != io.EOF {
				http.Error(w, "invalid request", http.StatusBadRequest)
				return
			}
			owner, tenant := body.OwnerSubject, body.TenantID
			card, err := authority.Get(request.Context(), tenant, owner, body.CardID)
			if err != nil || card == nil {
				http.Error(w, "card unavailable", http.StatusNotFound)
				return
			}
			var proposal *actions.DecisionProposal
			var execution *actions.FrozenDecisionExecution
			if card.Status == "awaiting_approval" {
				preparer, ok := authority.(interface {
					PrepareDecision(context.Context, string, string, string, actions.DecisionRequester) (*actions.DecisionProposal, error)
				})
				if !ok {
					http.Error(w, "proposal unavailable", http.StatusServiceUnavailable)
					return
				}
				proposal, err = preparer.PrepareDecision(request.Context(), tenant, owner, body.CardID, body.Requester)
			} else {
				reader, ok := authority.(interface {
					ReadFrozenDecision(context.Context, string, string, string, actions.DecisionRequester) (*actions.GovernedCard, *actions.DecisionProposal, *actions.FrozenDecisionExecution, error)
				})
				if !ok {
					http.Error(w, "proposal unavailable", http.StatusServiceUnavailable)
					return
				}
				card, proposal, execution, err = reader.ReadFrozenDecision(request.Context(), tenant, owner, body.CardID, body.Requester)
			}
			if err != nil || card == nil || proposal == nil {
				http.Error(w, "proposal unavailable", http.StatusConflict)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(struct {
				OwnerSubject string                           `json:"ownerSubject"`
				TenantID     string                           `json:"tenantId"`
				Card         *actions.GovernedCard            `json:"card"`
				Proposal     *actions.DecisionProposal        `json:"proposal"`
				Execution    *actions.FrozenDecisionExecution `json:"execution,omitempty"`
			}{owner, tenant, card, proposal, execution})
		})).ServeHTTP(e.Response, e.Request)
		return nil
	})
}
