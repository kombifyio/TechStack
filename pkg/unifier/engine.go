// Package unifier provides the Unifier Engine implementation.
// This file contains the core Engine struct, constructors, and schema loading.
// Methods are split across: analyze.go, validate.go, unify.go, provision.go, stackkit_api.go
package unifier

import (
	"embed"
	"fmt"
	"log/slog"
	"sync"

	"cuelang.org/go/cue"
	"cuelang.org/go/cue/cuecontext"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/tofu"
)

//go:embed schema/*.cue
var schemaFS embed.FS

// Engine implements the core.Unifier interface using CUE.
type Engine struct {
	ctx       *cue.Context
	schema    cue.Value
	kitLoader *StackKitLoader
	logger    *slog.Logger
	runner    tofu.ExtendedRunner // OpenTofu runner for provisioning
	dataDir   string              // Base directory for stack data (default: "data/stacks")
	mu        sync.RWMutex
}

// New creates a new Unifier Engine with embedded CUE schemas.
func New() (*Engine, error) {
	ctx := cuecontext.New()

	// Load all embedded CUE schemas
	schemaValue, err := loadEmbeddedSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load CUE schemas: %w", err)
	}

	// Initialize StackKit loader
	kitLoader, err := NewStackKitLoader()
	if err != nil {
		// Non-fatal: engine can work without stackkits for basic validation
		kitLoader = nil
	}

	return &Engine{
		ctx:       ctx,
		schema:    schemaValue,
		kitLoader: kitLoader,
		logger:    logger.Default().Logger,
		runner:    &tofu.DefaultRunner{},
		dataDir:   "data/stacks",
	}, nil
}

// NewWithStackKitDir creates an Engine with a custom StackKit directory.
func NewWithStackKitDir(stackKitDir string) (*Engine, error) {
	ctx := cuecontext.New()

	schemaValue, err := loadEmbeddedSchemas(ctx)
	if err != nil {
		return nil, fmt.Errorf("failed to load CUE schemas: %w", err)
	}

	kitLoader, err := NewStackKitLoaderWithDir(stackKitDir)
	if err != nil {
		return nil, fmt.Errorf("failed to initialize StackKit loader: %w", err)
	}

	return &Engine{
		ctx:       ctx,
		schema:    schemaValue,
		kitLoader: kitLoader,
		logger:    logger.Default().Logger,
		runner:    &tofu.DefaultRunner{},
		dataDir:   "data/stacks",
	}, nil
}

// loadEmbeddedSchemas reads and compiles all .cue files from the embedded filesystem.
func loadEmbeddedSchemas(ctx *cue.Context) (cue.Value, error) {
	entries, err := schemaFS.ReadDir("schema")
	if err != nil {
		return cue.Value{}, fmt.Errorf("failed to read schema directory: %w", err)
	}

	var combined cue.Value
	first := true

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		content, err := schemaFS.ReadFile("schema/" + entry.Name())
		if err != nil {
			return cue.Value{}, fmt.Errorf("failed to read %s: %w", entry.Name(), err)
		}

		val := ctx.CompileBytes(content)
		if err := val.Err(); err != nil {
			return cue.Value{}, fmt.Errorf("failed to compile %s: %w", entry.Name(), err)
		}

		if first {
			combined = val
			first = false
		} else {
			combined = combined.Unify(val)
		}
	}

	if first {
		return cue.Value{}, fmt.Errorf("no CUE schemas found")
	}

	return combined, nil
}
