package routes

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/usernotice"
)

const workerUserNotificationBodyLimit int64 = 8 << 10

// workerUserNotificationRequest is the HTTPS form of the agent's
// UserNotification message. It carries no identity: the node comes from the
// authenticated worker.
type workerUserNotificationRequest struct {
	AutomationID   string `json:"automation_id"`
	Text           string `json:"text"`
	Audience       string `json:"audience"`
	MemberUsername string `json:"member_username"`
	Source         string `json:"source"`
}

// workerUserNotification admits one user-authored smart-home notice from an
// authenticated agent over the managed HTTPS channel. A refusal is a
// structured answer with the status that names it (422 for a malformed
// notice, 429 with Retry-After for the per-node rate limit).
func (h workerRouteHandlers) workerUserNotification(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	var req workerUserNotificationRequest
	decoder := json.NewDecoder(e.Request.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&req); err != nil {
		return httpx.BadRequest(e, "Invalid user notification", nil)
	}
	authCtx, ok := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !ok {
		return nil
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	if h.userNotices == nil {
		return refuseUserNotification(e, usernotice.Result{RefusalCode: usernotice.RefusalNotConfigured})
	}
	node := usernotice.Node{
		TenantID: authCtx.TenantID, StackID: authCtx.StackID, OwnerSubjectID: authCtx.OwnerID, AgentID: authCtx.RuntimeAgentID,
	}
	if authCtx.Worker != nil {
		node.StackID = firstNonEmpty(node.StackID, authCtx.Worker.StackID)
		node.OwnerSubjectID = firstNonEmpty(node.OwnerSubjectID, authCtx.Worker.OwnerSubjectID)
	}
	result, err := h.userNotices.Report(e.Request.Context(), node, usernotice.Notice{
		AutomationID: req.AutomationID, Text: req.Text, Audience: req.Audience,
		MemberUsername: req.MemberUsername, Source: req.Source,
	})
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "User notifications are unavailable", map[string]any{"retryable": true})
	}
	if !result.Accepted {
		return refuseUserNotification(e, result)
	}
	return httpx.Success(e, http.StatusAccepted, map[string]any{
		"user_notification": map[string]any{"status": "accepted", "recipients": result.Recipients},
	})
}

func refuseUserNotification(e *httpx.Event, result usernotice.Result) error {
	status := http.StatusUnprocessableEntity
	details := map[string]any{"reason_code": result.RefusalCode}
	switch result.RefusalCode {
	case usernotice.RefusalRateLimited:
		status = http.StatusTooManyRequests
		seconds := int(result.RetryAfter.Seconds())
		details["retry_after_seconds"] = seconds
		e.Response.Header().Set("Retry-After", strconv.Itoa(seconds))
	case usernotice.RefusalNotConfigured, usernotice.RefusalNodeUnresolved:
		status = http.StatusConflict
	}
	return httpx.Error(e, status, ksapi.ErrCodeValidation, "The notification was refused", details)
}
