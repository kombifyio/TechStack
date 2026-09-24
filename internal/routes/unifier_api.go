// Package routes provides the Unifier API endpoints.
package routes

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/controlplane"
	kscore "github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/unifier"
)

const maxUnifierRequestBodyBytes int64 = 2 << 20 // 2 MiB

func readRequestBodyLimited(r io.Reader, limitBytes int64) ([]byte, bool, error) {
	limited := io.LimitReader(r, limitBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, false, err
	}
	if int64(len(body)) > limitBytes {
		return nil, true, nil
	}
	return body, false, nil
}

// UnifierAPI holds the Unifier engine and pipeline instances.
type UnifierAPI struct {
	engine          *unifier.Engine
	pipeline        *unifier.Pipeline
	loader          *unifier.Loader
	recommendations *unifier.WizardRecommendationAuthority
	workers         controlplane.WorkerStore
}

// NewUnifierAPI creates a new UnifierAPI instance with full pipeline support.
func NewUnifierAPI(workers controlplane.WorkerStore) (*UnifierAPI, error) {
	engine, err := unifier.New()
	if err != nil {
		return nil, err
	}

	pipeline := unifier.NewPipeline(engine)

	return &UnifierAPI{
		engine:          engine,
		pipeline:        pipeline,
		loader:          unifier.NewLoader(),
		recommendations: unifier.NewWizardRecommendationAuthority(engine),
		workers:         workers,
	}, nil
}

func requireUnifierAuth(e *httpx.Event) (string, error) {
	if userID, ok := authenticatedUserID(e); ok {
		return userID, nil
	}
	return "", httpx.RejectUnauthorized(e, "Authentication required")
}

// fetchWorkers loads the exact owner's workers from the canonical tenant store.
func (api *UnifierAPI) fetchWorkers(ctx context.Context, tenantID, ownerID string) ([]kscore.Worker, error) {
	if api.workers == nil {
		return nil, fmt.Errorf("unifier: worker store is not configured")
	}
	records, err := api.workers.ListWorkersByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}

	workers := make([]kscore.Worker, 0, len(records))
	for _, record := range records {
		if strings.TrimSpace(record.OwnerSubjectID) != strings.TrimSpace(ownerID) {
			continue
		}
		workers = append(workers, unifierWorkerFromRecord(record))
	}

	return workers, nil
}

func unifierWorkerFromRecord(record controlplane.Worker) kscore.Worker {
	tags := unifierWorkerTags(record.Tags)
	if ip := strings.TrimSpace(record.IP); ip != "" {
		tags["ip"] = ip
	}
	worker := kscore.Worker{
		ID:       record.ID,
		Name:     record.Hostname,
		Type:     record.Type,
		Provider: record.Provider,
		Status:   record.Status,
		Capabilities: kscore.WorkerCapabilities{
			CPU:            record.CPUCores,
			RAM:            record.RAMMB,
			Disk:           record.DiskGB,
			Arch:           record.Arch,
			OS:             record.OS,
			DockerVersion:  record.DockerVersion,
			HasNVMe:        record.HasNVME,
			HasHWTranscode: record.HasHWTranscode,
		},
		Tags: tags,
	}
	if gpu := strings.TrimSpace(record.GPU); gpu != "" {
		worker.Capabilities.GPU = &kscore.GPUInfo{Model: gpu}
	}
	if worker.Type == "" {
		worker.Type = "worker"
	}
	if worker.Provider == "" {
		worker.Provider = "local"
	}
	return worker
}

const wizardRecommendationInventoryTTL = 10 * time.Minute

// fetchRecommendationInventory reads the same tenant worker registry as the
// ordinary Unifier path, then applies the authenticated owner filter before
// exposing any inventory to the recommendation authority.
func (api *UnifierAPI) fetchRecommendationInventory(ctx context.Context, tenantID, ownerID string) (unifier.WizardRecommendationInventory, error) {
	inventory := unifier.WizardRecommendationInventory{
		State:      "unavailable",
		Source:     "worker-registry",
		SnapshotID: "worker-registry",
	}
	if api.workers == nil {
		return inventory, fmt.Errorf("unifier: worker store is not configured")
	}
	records, err := api.workers.ListWorkersByTenant(ctx, tenantID)
	if err != nil {
		return inventory, err
	}
	inventory.Workers = make([]kscore.Worker, 0, len(records))
	inventory.State = "fresh"
	stale := false
	var latest time.Time
	now := time.Now().UTC()
	for _, record := range records {
		if strings.TrimSpace(record.OwnerSubjectID) != strings.TrimSpace(ownerID) {
			continue
		}
		inventory.Workers = append(inventory.Workers, unifierWorkerFromRecord(record))
		if record.LastSeenAt == nil || now.Sub(record.LastSeenAt.UTC()) > wizardRecommendationInventoryTTL {
			stale = true
		}
		if record.LastSeenAt != nil && record.LastSeenAt.After(latest) {
			latest = record.LastSeenAt.UTC()
		}
	}
	if stale {
		inventory.State = "stale"
		inventory.StaleInputs = []string{"worker_inventory"}
	}
	inventory.ObservedAt = latest
	return inventory, nil
}

func unifierWorkerTags(values map[string]any) map[string]string {
	tags := make(map[string]string, len(values))
	for key, value := range values {
		if text, ok := value.(string); ok && strings.TrimSpace(key) != "" {
			tags[strings.TrimSpace(key)] = strings.TrimSpace(text)
		}
	}
	return tags
}

// RegisterUnifierRoutes adds the /api/v1/unifier/* endpoints.
func RegisterUnifierRoutes(r *httpx.Router, workers controlplane.WorkerStore) error {
	api, err := NewUnifierAPI(workers)
	if err != nil {
		return err
	}

	r.POST("/api/v1/unifier/validate", func(e *httpx.Event) error { return api.handleValidate(e) })
	r.POST("/api/v1/unifier/unify", func(e *httpx.Event) error { return api.handleUnify(e) })
	r.POST("/api/v1/unifier/recommendations", func(e *httpx.Event) error { return api.handleRecommendations(e) })
	r.POST("/api/v1/unifier/pipeline", func(e *httpx.Event) error { return api.handlePipeline(e) })
	r.POST("/api/v1/unifier/pipeline/validate", func(e *httpx.Event) error { return api.handlePipeline(e) })
	r.POST("/api/v1/unifier/pipeline/preview", func(e *httpx.Event) error { return api.handlePipelinePreview(e) })
	r.POST("/api/v1/unifier/generate", func(e *httpx.Event) error { return api.handleGenerate(e) })

	r.GET("/api/v1/stackkits", func(e *httpx.Event) error { return api.handleListStackKits(e) })
	// Registered before the {name} pattern so the literal path wins.
	r.GET("/api/v1/stackkits/use-cases", func(e *httpx.Event) error { return api.handleUseCaseCatalog(e) })
	r.GET("/api/v1/stackkits/{name}", func(e *httpx.Event) error { return api.handleGetStackKit(e) })

	r.GET("/api/v1/addons", func(e *httpx.Event) error { return api.handleListAddons(e) })
	r.POST("/api/v1/addons/detect", func(e *httpx.Event) error { return api.handleDetectAddons(e) })

	r.POST("/api/v1/unifier/analyze", func(e *httpx.Event) error { return api.handleAnalyze(e) })
	r.POST("/api/v1/unifier/iac", func(e *httpx.Event) error { return api.handleIaCGeneration(e) })
	r.POST("/api/v1/unifier/iac/preview", func(e *httpx.Event) error { return api.handleIaCPreview(e) })

	return nil
}
