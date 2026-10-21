package stacks

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/orchestrator"
)

// destroyStackBodyLimit bounds the optional destroy request body; the id limits
// are the ones the API contract states.
const (
	destroyStackBodyLimit      = 8 << 10
	destroyConfirmMaxServers   = 64
	destroyConfirmMaxServerIDs = 128
)

// destroyStackRequest is the optional body of POST /api/v1/stacks/{id}/destroy.
// Without a body the destroy removes the stack through its nodes.
type destroyStackRequest struct {
	OfflineRemoval *struct {
		ConfirmServerIDs []string `json:"confirm_server_ids"`
	} `json:"offline_removal"`
}

// readDestroyStackRequest reads the optional destroy body. An absent or empty
// body is a plain destroy; any other body must be exactly one known JSON object.
func readDestroyStackRequest(request *http.Request) (*orchestrator.OfflineRemovalRequest, error) {
	if request == nil || request.Body == nil {
		return nil, nil
	}
	raw, err := io.ReadAll(io.LimitReader(request.Body, destroyStackBodyLimit+1))
	if err != nil {
		return nil, errors.New("destroy request body could not be read")
	}
	if len(raw) > destroyStackBodyLimit {
		return nil, errors.New("destroy request body is too large")
	}
	if strings.TrimSpace(string(raw)) == "" {
		return nil, nil
	}
	var body destroyStackRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return nil, errors.New("destroy request body must be a JSON object with an optional offline_removal")
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, errors.New("destroy request must contain one JSON object")
	}
	if body.OfflineRemoval == nil {
		return nil, nil
	}
	if len(body.OfflineRemoval.ConfirmServerIDs) > destroyConfirmMaxServers {
		return nil, errors.New("offline_removal.confirm_server_ids names too many servers")
	}
	for _, id := range body.OfflineRemoval.ConfirmServerIDs {
		if len(id) > destroyConfirmMaxServerIDs {
			return nil, errors.New("offline_removal.confirm_server_ids holds an id that is too long")
		}
	}
	return &orchestrator.OfflineRemovalRequest{ConfirmServerIDs: body.OfflineRemoval.ConfirmServerIDs}, nil
}

// offlineRemovalRefusal answers a destroy that admission refused because of the
// state of the stack's servers. It reports false for any other error.
func offlineRemovalRefusal(e *httpx.Event, err error) (bool, error) {
	var refusal *orchestrator.OfflineRemovalError
	if !errors.As(err, &refusal) {
		return false, nil
	}
	details := map[string]any{"reason_code": refusal.Code, "retryable": false}
	if len(refusal.Servers) > 0 {
		details["servers"] = refusal.Servers
	}
	status, code := http.StatusConflict, ksapi.ErrCodeConflict
	switch refusal.Code {
	case orchestrator.ReasonOfflineRemovalInvalid:
		status, code = http.StatusUnprocessableEntity, ksapi.ErrCodeValidation
	case orchestrator.ReasonOfflineRemovalUnavailable:
		status, code = http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable
		details["retryable"] = true
	}
	return true, httpx.Error(e, status, code, refusal.Message, details)
}
