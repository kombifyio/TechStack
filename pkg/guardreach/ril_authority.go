package guardreach

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
)

const rilAuthorityPath = "/v1/internal/ril/authority"

type RILAuthorityResult struct {
	Allowed     bool            `json:"allowed"`
	DecisionID  string          `json:"decisionId"`
	State       string          `json:"state"`
	Claim       string          `json:"claim"`
	Lease       string          `json:"lease"`
	Receipt     json.RawMessage `json:"receipt"`
	Effect      json.RawMessage `json:"effect"`
	ExecutionID string          `json:"executionId"`
	Fence       string          `json:"fence"`
	LeaseID     string          `json:"leaseId"`
}

// RILAuthority calls the Gateway's request-bound Techstack service route. The
// caller supplies identities already checked by Techstack's owner-scoped route.
func (g *Gateway) RILAuthority(ctx context.Context, owner, tenant, operation, cardID, lease, executionID, fence string) (RILAuthorityResult, error) {
	var result RILAuthorityResult
	if g == nil || owner == "" || tenant == "" || cardID == "" || (operation != "prepare" && operation != "issue" && operation != "claim") {
		return result, errors.New("RIL authority input invalid")
	}
	body := map[string]string{"operation": operation, "cardId": cardID}
	if lease != "" {
		body["lease"] = lease
	}
	if executionID != "" {
		body["executionId"] = executionID
	}
	if fence != "" {
		body["fence"] = fence
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	_, err = g.do(ctx, "ril-authority:"+operation, http.MethodPost, rilAuthorityPath, raw, &servicecall.OnBehalfOf{Sub: owner, OrgID: tenant}, &result)
	if err != nil {
		return result, err
	}
	if operation != "prepare" && !result.Allowed {
		return result, errors.New("RIL authority denied")
	}
	return result, nil
}
