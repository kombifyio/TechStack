package routes

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/kombifyio/techstack/pkg/httpx"
)

// WorkerTelemetry is the kombify Guard telemetry ingest as the worker routes use
// it. Nil in editions that store no telemetry; a Guard is then told so and stops
// collecting.
type WorkerTelemetry interface {
	Ingest(ctx context.Context, identity guardtelemetry.Identity, body []byte) (guardtelemetry.Ack, error)
}

// workerTelemetry takes a Guard's telemetry batch under its authenticated
// identity. The batch carries no identity and no label that could claim one:
// the tenant, worker and server of every stored series come from the
// authentication. Only what is stored is acknowledged, so a Guard keeps the rest.
func (h workerRouteHandlers) workerTelemetry(e *httpx.Event) error {
	id := strings.TrimSpace(e.Request.PathValue("id"))
	body, readErr := io.ReadAll(http.MaxBytesReader(e.Response, e.Request.Body, guardtelemetry.MaxBatchBytes))
	if readErr != nil {
		return httpx.Error(e, http.StatusRequestEntityTooLarge, ksapi.ErrCodeBadRequest, "The telemetry batch exceeds its bound", nil)
	}
	authCtx, ok := h.authenticateRuntimeAgent(e, id, workerInventoryRequest{RuntimeAgentID: id})
	if !ok {
		return nil
	}
	e.Response.Header().Set("Cache-Control", "no-store")
	if h.telemetry == nil {
		return httpx.Success(e, http.StatusOK, map[string]any{"telemetry": guardtelemetry.Ack{State: guardtelemetry.StateDisabled}})
	}
	identity := guardtelemetry.Identity{
		TenantID: authCtx.TenantID, WorkerID: authCtx.RuntimeAgentID,
		ServerID: firstNonEmpty(authCtx.ServerID, runtimeServerIDForWorker(authCtx.RuntimeAgentID)),
	}
	if authCtx.Worker != nil {
		identity.Hostname, identity.Provider = strings.TrimSpace(authCtx.Worker.Hostname), strings.TrimSpace(authCtx.Worker.Provider)
	}
	ack, err := h.telemetry.Ingest(e.Request.Context(), identity, body)
	if err == nil || errors.Is(err, guardtelemetry.ErrPartialBatch) {
		if capability, ok := h.telemetry.(interface{ DriveHealthSupported() bool }); ok && capability.DriveHealthSupported() {
			e.Response.Header().Set(guardtelemetry.DriveHealthCapabilityHeader, "1")
		}
	}
	switch {
	case errors.Is(err, guardtelemetry.ErrBatchInvalid):
		return httpx.BadRequest(e, "Invalid telemetry batch", nil)
	case errors.Is(err, guardtelemetry.ErrPartialBatch):
		// Part of the batch is stored: say how far, so the Guard keeps the rest.
		slog.Warn("guard_telemetry_partial", "worker_id", identity.WorkerID, "error", err.Error())
		return httpx.Success(e, http.StatusOK, map[string]any{"telemetry": ack})
	case err != nil:
		slog.Warn("guard_telemetry_ingest_failed", "worker_id", identity.WorkerID, "error", err.Error())
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Guard telemetry is unavailable", nil)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"telemetry": ack})
}
