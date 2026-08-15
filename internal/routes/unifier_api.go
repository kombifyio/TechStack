// Package routes provides the Unifier API endpoints.
package routes

import (
	"io"
	"strings"

	pbcore "github.com/pocketbase/pocketbase/core"

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
	engine           *unifier.Engine
	pipeline         *unifier.Pipeline
	extendedPipeline *unifier.ExtendedPipeline
	loader           *unifier.Loader
	app              pbcore.App
	iacOutputDir     string
}

// NewUnifierAPI creates a new UnifierAPI instance with full pipeline support.
func NewUnifierAPI(app pbcore.App) (*UnifierAPI, error) {
	engine, err := unifier.New()
	if err != nil {
		return nil, err
	}

	pipeline := unifier.NewPipeline(engine)
	iacOutputDir := "data/stacks"
	extPipeline := unifier.NewExtendedPipeline(engine, iacOutputDir)
	if stackkitsDir := unifier.DefaultStackKitsDir(); stackkitsDir != "" {
		extPipeline = extPipeline.WithStackKitDir(stackkitsDir)
	}

	return &UnifierAPI{
		engine:           engine,
		pipeline:         pipeline,
		extendedPipeline: extPipeline,
		loader:           unifier.NewLoader(),
		app:              app,
		iacOutputDir:     iacOutputDir,
	}, nil
}

func requireUnifierAuth(e *httpx.Event) (string, error) {
	if userID, ok := authenticatedUserID(e); ok {
		return userID, nil
	}
	return "", httpx.RejectUnauthorized(e, "Authentication required")
}

func parseWorkerTags(tagStr string) map[string]string {
	tags := make(map[string]string)
	if tagStr == "" {
		return tags
	}

	for _, part := range strings.Split(tagStr, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		tags[strings.TrimSpace(kv[0])] = strings.TrimSpace(kv[1])
	}

	return tags
}

// fetchWorkersFromDB loads registered workers from the database.
func (api *UnifierAPI) fetchWorkersFromDB(ownerID string) ([]kscore.Worker, error) {
	records, err := api.app.FindRecordsByFilter(
		"workers",
		"owner_id = {:ownerId}",
		"-created",
		0, 0,
		map[string]any{"ownerId": ownerID},
	)
	if err != nil {
		return nil, err
	}

	workers := make([]kscore.Worker, 0, len(records))
	for _, r := range records {
		tags := parseWorkerTags(r.GetString("tags"))
		if ip := strings.TrimSpace(r.GetString("ip")); ip != "" {
			tags["ip"] = ip
		}

		worker := kscore.Worker{
			ID:       r.Id,
			Name:     r.GetString("hostname"),
			Type:     r.GetString("type"),
			Provider: r.GetString("provider"),
			Status:   r.GetString("status"),
			Capabilities: kscore.WorkerCapabilities{
				CPU:            r.GetInt("cpu_cores"),
				RAM:            r.GetInt("ram_mb"),
				Disk:           r.GetInt("disk_gb"),
				Arch:           r.GetString("arch"),
				OS:             r.GetString("os"),
				DockerVersion:  r.GetString("docker_version"),
				HasNVMe:        r.GetBool("has_nvme"),
				HasHWTranscode: r.GetBool("has_hw_transcode"),
			},
			Tags: tags,
		}

		if gpu := strings.TrimSpace(r.GetString("gpu")); gpu != "" {
			worker.Capabilities.GPU = &kscore.GPUInfo{Model: gpu}
		}

		if worker.Type == "" {
			worker.Type = "worker"
		}
		if worker.Provider == "" {
			worker.Provider = "local"
		}
		workers = append(workers, worker)
	}

	return workers, nil
}

// RegisterUnifierRoutes adds the /api/v1/unifier/* endpoints.
func RegisterUnifierRoutes(r *httpx.Router, app pbcore.App) error {
	api, err := NewUnifierAPI(app)
	if err != nil {
		return err
	}

	r.POST("/api/v1/unifier/validate", func(e *httpx.Event) error { return api.handleValidate(e) })
	r.POST("/api/v1/unifier/unify", func(e *httpx.Event) error { return api.handleUnify(e) })
	r.POST("/api/v1/unifier/pipeline", func(e *httpx.Event) error { return api.handlePipeline(e) })
	r.POST("/api/v1/unifier/pipeline/validate", func(e *httpx.Event) error { return api.handlePipeline(e) })
	r.POST("/api/v1/unifier/pipeline/preview", func(e *httpx.Event) error { return api.handlePipelinePreview(e) })
	r.POST("/api/v1/unifier/generate", func(e *httpx.Event) error { return api.handleGenerate(e) })

	r.GET("/api/v1/stackkits", func(e *httpx.Event) error { return api.handleListStackKits(e) })
	r.GET("/api/v1/stackkits/{name}", func(e *httpx.Event) error { return api.handleGetStackKit(e) })

	r.GET("/api/v1/addons", func(e *httpx.Event) error { return api.handleListAddons(e) })
	r.POST("/api/v1/addons/detect", func(e *httpx.Event) error { return api.handleDetectAddons(e) })

	r.POST("/api/v1/unifier/analyze", func(e *httpx.Event) error { return api.handleAnalyze(e) })
	r.POST("/api/v1/unifier/iac", func(e *httpx.Event) error { return api.handleIaCGeneration(e) })
	r.POST("/api/v1/unifier/iac/preview", func(e *httpx.Event) error { return api.handleIaCPreview(e) })

	return nil
}
