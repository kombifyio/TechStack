package routes

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/routes/tenantguard"
	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/runtimeconvergence"
)

// WorkerStackKitsUpdates is Techstack's StackKits update management of a
// node (D-2026-11-02-03): the proposal, the node's update setting and the
// start of an update Techstack runs without a click.
type WorkerStackKitsUpdates interface {
	StackKitsUpdate(ctx context.Context, tenantID, ownerID, workerID string) (orchestrator.StackKitsUpdateView, error)
	SetStackKitsUpdateBehavior(ctx context.Context, tenantID, ownerID, workerID, behavior string) (orchestrator.StackKitsUpdateView, error)
	EvaluateStackKitsUpdate(ctx context.Context, tenantID, workerID string) (string, error)
}

func (h workerRouteHandlers) ownedWorkerForStackKitsUpdate(e *httpx.Event, capability string) (ownerID, tenantID, workerID string, err error) {
	ownerID, err = requireWorkerOwner(e)
	if err != nil || ownerID == "" {
		return "", "", "", err
	}
	tenantID, err = tenantguard.TenantScope(requestExplicitTenantID(e), ownerID, capability)
	if err != nil {
		return "", "", "", err
	}
	if h.stackKitsUpdates == nil {
		return "", "", "", httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "StackKits update management is unavailable", nil)
	}
	return ownerID, tenantID, strings.TrimSpace(e.Request.PathValue("id")), nil
}

func stackKitsUpdateFailure(e *httpx.Event, err error) error {
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		return httpx.NotFound(e, "Worker not found")
	case errors.Is(err, orchestrator.ErrStackKitsUpdateUnavailable):
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "StackKits update management is unavailable", nil)
	default:
		return httpx.Error(e, http.StatusInternalServerError, ksapi.ErrCodeInternal, "Failed to read the StackKits update", nil)
	}
}

// stackKitsUpdate shows the owner the node's update setting, the pending
// release with its highlights and changelog link, and the last update.
func (h workerRouteHandlers) stackKitsUpdate(e *httpx.Event) error {
	ownerID, tenantID, workerID, err := h.ownedWorkerForStackKitsUpdate(e, "techstack.workers.stackkits_update.read")
	if err != nil || ownerID == "" {
		return err
	}
	view, err := h.stackKitsUpdates.StackKitsUpdate(e.Request.Context(), tenantID, ownerID, workerID)
	if err != nil {
		return stackKitsUpdateFailure(e, err)
	}
	return httpx.Success(e, http.StatusOK, view)
}

type stackKitsUpdateBehaviorRequest struct {
	Behavior string `json:"behavior"`
}

// setStackKitsUpdateBehavior stores the node's update setting: approve each
// update (the default) or install updates automatically.
func (h workerRouteHandlers) setStackKitsUpdateBehavior(e *httpx.Event) error {
	ownerID, tenantID, workerID, err := h.ownedWorkerForStackKitsUpdate(e, "techstack.workers.stackkits_update.behavior")
	if err != nil || ownerID == "" {
		return err
	}
	raw, readErr := io.ReadAll(io.LimitReader(e.Request.Body, 1025))
	var request stackKitsUpdateBehaviorRequest
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if readErr != nil || len(raw) > 1024 || decoder.Decode(&request) != nil ||
		(request.Behavior != controlplane.StackKitsUpdateApprove && request.Behavior != controlplane.StackKitsUpdateAutomatic) {
		return httpx.BadRequest(e, "behavior must be approve or automatic", nil)
	}
	view, err := h.stackKitsUpdates.SetStackKitsUpdateBehavior(e.Request.Context(), tenantID, ownerID, workerID, request.Behavior)
	if err != nil {
		return stackKitsUpdateFailure(e, err)
	}
	return httpx.Success(e, http.StatusOK, view)
}

// stackKitsUpdateEvaluator starts a node's pending update off the heartbeat
// path: at most one evaluation per node per interval and process, and at most
// a few at once per process, so a Guard that reports a pending release on every
// heartbeat never loops. The claim in the worker row makes the start once-only
// across replicas.
type stackKitsUpdateEvaluator struct {
	updates  WorkerStackKitsUpdates
	interval time.Duration
	slots    chan struct{}
	mu       sync.Mutex
	last     map[string]time.Time
}

const (
	stackKitsUpdateEvaluationInterval    = 2 * time.Minute
	stackKitsUpdateConcurrentEvaluations = 4
)

func newStackKitsUpdateEvaluator(updates WorkerStackKitsUpdates) *stackKitsUpdateEvaluator {
	if updates == nil {
		return nil
	}
	return &stackKitsUpdateEvaluator{
		updates: updates, interval: stackKitsUpdateEvaluationInterval,
		slots: make(chan struct{}, stackKitsUpdateConcurrentEvaluations), last: map[string]time.Time{},
	}
}

func (ev *stackKitsUpdateEvaluator) observe(tenantID, workerID string, convergence *runtimeconvergence.Snapshot) {
	if ev == nil || convergence == nil || !stackKitsUpdatePending(*convergence) {
		return
	}
	select {
	case ev.slots <- struct{}{}:
	default:
		// Busy: a later heartbeat of this node evaluates it.
		return
	}
	key := tenantID + "\x00" + workerID
	now := time.Now()
	ev.mu.Lock()
	if now.Sub(ev.last[key]) < ev.interval {
		ev.mu.Unlock()
		<-ev.slots
		return
	}
	ev.last[key] = now
	for other, at := range ev.last {
		if now.Sub(at) > 10*ev.interval {
			delete(ev.last, other)
		}
	}
	ev.mu.Unlock()
	go func() {
		defer func() { <-ev.slots }()
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		if _, err := ev.updates.EvaluateStackKitsUpdate(ctx, tenantID, workerID); err != nil {
			logger.Default().Warn("stackkits_update_evaluation_failed", "worker_id", workerID, "error", err.Error())
		}
	}()
}

func stackKitsUpdatePending(snapshot runtimeconvergence.Snapshot) bool {
	for _, component := range snapshot.Components {
		if component.Name == runtimeconvergence.StackKitsRuntimeComponent && component.UpdateAvailable != nil {
			return true
		}
	}
	return false
}
