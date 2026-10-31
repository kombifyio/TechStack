package httpguard

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	providerexecutor "github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

type WorkerExecutor interface {
	ExecuteWorker(context.Context, providerexecutor.WorkerExecution) providerexecutor.WorkerExecutionResult
}

type providerControlRequest struct {
	controlRequest
	InvocationID   string                                  `json:"invocation_id,omitempty"`
	ProviderResult *providerexecutor.WorkerExecutionResult `json:"provider_result,omitempty"`
}

func (c *Client) runWorkerLoop(ctx context.Context) {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return
		case c.workerGate <- struct{}{}:
		}
		idle := c.runWorkerCycle(ctx)
		<-c.workerGate
		if idle && waitContext(ctx, time.Second) != nil {
			return
		}
	}
}

// HoldWorker waits until no provider command is in flight and claims no further
// one until release is called. A command's result lives only in this process, so
// a Guard that is about to restart holds the worker first.
func (c *Client) HoldWorker(ctx context.Context) (release func(), err error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case c.workerGate <- struct{}{}:
		return func() { <-c.workerGate }, nil
	}
}

// runWorkerCycle claims and completes at most one provider command, its result
// included. It reports whether there was none, so the caller waits before asking
// again.
func (c *Client) runWorkerCycle(ctx context.Context) (idle bool) {
	identity := c.controlIdentity()
	identity.Capabilities = []string{"proxmox-substrate-v1"}
	status, body, err := c.postJSONResponseLimit(ctx, c.cfg.WorkerCommandURL, providerControlRequest{controlRequest: identity}, 256<<10)
	if err != nil || status != http.StatusOK {
		if err == nil && status != http.StatusNoContent {
			c.log.Warn("substrate_poll_rejected", "status", status)
		}
		return true
	}
	var response struct {
		Data struct {
			Command *providerexecutor.WorkerExecution `json:"command"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &response) != nil || response.Data.Command == nil {
		return true
	}
	var result providerexecutor.WorkerExecutionResult
	if executor, ok := c.cfg.WorkerExecutor.(interface {
		ExecuteWorkerWithBootstrap(context.Context, providerexecutor.WorkerExecution, func(context.Context) ([]byte, error)) providerexecutor.WorkerExecutionResult
	}); ok {
		result = executor.ExecuteWorkerWithBootstrap(ctx, *response.Data.Command, func(fetchCtx context.Context) ([]byte, error) {
			status, body, err := c.postJSONResponseLimit(fetchCtx, strings.TrimSuffix(c.cfg.WorkerCommandURL, "/next")+"/bootstrap", providerControlRequest{controlRequest: identity, InvocationID: response.Data.Command.InvocationID}, 300<<10)
			if err != nil || status != http.StatusOK {
				return nil, errors.New("substrate bootstrap unavailable")
			}
			var bootstrap struct {
				Data struct {
					CloudInit string `json:"cloud_init"`
				} `json:"data"`
			}
			if json.Unmarshal(body, &bootstrap) != nil || bootstrap.Data.CloudInit == "" {
				return nil, errors.New("substrate bootstrap invalid")
			}
			return []byte(bootstrap.Data.CloudInit), nil
		})
	} else {
		result = c.cfg.WorkerExecutor.ExecuteWorker(ctx, *response.Data.Command)
	}
	// Retain the exact result until accepted; never re-execute the provider
	// action merely because its result response was lost.
	for ctx.Err() == nil {
		status, _, err = c.postJSONResponse(ctx, c.cfg.WorkerResultURL, providerControlRequest{controlRequest: identity, ProviderResult: &result})
		if err == nil && status >= 200 && status < 300 {
			break
		}
		if err == nil && status >= 400 && status < 500 {
			c.log.Warn("substrate_result_rejected", "status", status)
			break
		}
		if waitContext(ctx, time.Second) != nil {
			break
		}
	}
	return false
}
