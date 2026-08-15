package controlplane

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

const (
	WorkerCredentialTokenSHA256ResourceKey       = "agent_token_sha256"
	WorkerCredentialIdempotencySHA256ResourceKey = "enrollment_idempotency_sha256"
	WorkerCredentialRequestSHA256ResourceKey     = "enrollment_request_sha256"
	WorkerCredentialGenerationResourceKey        = "credential_generation"
)

var workerCredentialSHA256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// WorkerCredentialState is the non-reversible durable state for one runtime
// agent credential generation. Raw credentials and idempotency keys are never
// persisted.
type WorkerCredentialState struct {
	TokenSHA256       string
	IdempotencySHA256 string
	RequestSHA256     string
	Generation        int64
}

// WorkerCredentialCAS atomically advances one tenant-scoped worker credential
// from Expected to Next. Next must be exactly Expected.Generation+1.
type WorkerCredentialCAS struct {
	TenantID string
	WorkerID string
	Expected WorkerCredentialState
	Next     WorkerCredentialState
}

// WorkerCredentialStore is deliberately separate from WorkerStore so tests and
// read-only consumers do not accidentally acquire credential mutation
// authority. Enrollment composition requires both interfaces.
type WorkerCredentialStore interface {
	CompareAndSwapWorkerCredential(ctx context.Context, command WorkerCredentialCAS) (*Worker, error)
}

// WorkerCredentialStateFromWorker decodes reserved credential metadata without
// treating unrelated worker resources as credential authority.
func WorkerCredentialStateFromWorker(worker Worker) (WorkerCredentialState, error) {
	resources := worker.Resources
	state := WorkerCredentialState{
		TokenSHA256:       strings.TrimSpace(stringValue(resources[WorkerCredentialTokenSHA256ResourceKey])),
		IdempotencySHA256: strings.TrimSpace(stringValue(resources[WorkerCredentialIdempotencySHA256ResourceKey])),
		RequestSHA256:     strings.TrimSpace(stringValue(resources[WorkerCredentialRequestSHA256ResourceKey])),
	}
	generation, err := workerCredentialGeneration(resources[WorkerCredentialGenerationResourceKey])
	if err != nil {
		return WorkerCredentialState{}, err
	}
	state.Generation = generation
	return state, nil
}

func normalizeWorkerCredentialCAS(command WorkerCredentialCAS) (WorkerCredentialCAS, error) {
	command.TenantID = strings.TrimSpace(command.TenantID)
	command.WorkerID = strings.TrimSpace(command.WorkerID)
	command.Expected.TokenSHA256 = strings.TrimSpace(command.Expected.TokenSHA256)
	command.Expected.IdempotencySHA256 = strings.TrimSpace(command.Expected.IdempotencySHA256)
	command.Expected.RequestSHA256 = strings.TrimSpace(command.Expected.RequestSHA256)
	command.Next.TokenSHA256 = strings.TrimSpace(command.Next.TokenSHA256)
	command.Next.IdempotencySHA256 = strings.TrimSpace(command.Next.IdempotencySHA256)
	command.Next.RequestSHA256 = strings.TrimSpace(command.Next.RequestSHA256)
	if command.TenantID == "" || command.WorkerID == "" {
		return command, fmt.Errorf("%w: tenant and worker are required", ErrConflict)
	}
	if command.Expected.Generation < 0 || command.Next.Generation != command.Expected.Generation+1 {
		return command, fmt.Errorf("%w: credential generation must advance exactly once", ErrConflict)
	}
	for name, digest := range map[string]string{
		"token": command.Next.TokenSHA256, "idempotency": command.Next.IdempotencySHA256,
		"request": command.Next.RequestSHA256,
	} {
		if !workerCredentialSHA256Pattern.MatchString(digest) {
			return command, fmt.Errorf("%w: %s digest must be lowercase SHA-256", ErrConflict, name)
		}
	}
	return command, nil
}

func workerCredentialStateEqual(left, right WorkerCredentialState) bool {
	return left.Generation == right.Generation &&
		left.TokenSHA256 == right.TokenSHA256 &&
		left.IdempotencySHA256 == right.IdempotencySHA256 &&
		left.RequestSHA256 == right.RequestSHA256
}

func workerCredentialResources(state WorkerCredentialState) map[string]any {
	return map[string]any{
		WorkerCredentialTokenSHA256ResourceKey:       state.TokenSHA256,
		WorkerCredentialIdempotencySHA256ResourceKey: state.IdempotencySHA256,
		WorkerCredentialRequestSHA256ResourceKey:     state.RequestSHA256,
		WorkerCredentialGenerationResourceKey:        state.Generation,
	}
}

// preserveWorkerCredentialResources prevents generic heartbeat/inventory
// upserts from replacing or deleting credential authority on an existing row.
func preserveWorkerCredentialResources(existing, incoming map[string]any) map[string]any {
	out := cloneMap(incoming)
	if out == nil {
		out = map[string]any{}
	}
	for _, key := range []string{
		WorkerCredentialTokenSHA256ResourceKey,
		WorkerCredentialIdempotencySHA256ResourceKey,
		WorkerCredentialRequestSHA256ResourceKey,
		WorkerCredentialGenerationResourceKey,
	} {
		delete(out, key)
		if value, ok := existing[key]; ok {
			out[key] = value
		}
	}
	return out
}

func workerCredentialGeneration(value any) (int64, error) {
	if value == nil {
		return 0, nil
	}
	switch typed := value.(type) {
	case int:
		if typed < 0 {
			break
		}
		return int64(typed), nil
	case int32:
		if typed < 0 {
			break
		}
		return int64(typed), nil
	case int64:
		if typed < 0 {
			break
		}
		return typed, nil
	case float64:
		if typed < 0 || typed > math.MaxInt64 || math.Trunc(typed) != typed {
			break
		}
		return int64(typed), nil
	case json.Number:
		parsed, err := strconv.ParseInt(string(typed), 10, 64)
		if err == nil && parsed >= 0 {
			return parsed, nil
		}
	case string:
		parsed, err := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		if err == nil && parsed >= 0 {
			return parsed, nil
		}
	}
	return 0, fmt.Errorf("%w: invalid worker credential generation", ErrConflict)
}

func stringValue(value any) string {
	text, _ := value.(string)
	return text
}
