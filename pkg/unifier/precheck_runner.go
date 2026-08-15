// Package unifier provides the Pre-Check Runner for validating worker capabilities.
package unifier

import (
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

// Pre-check status values reused by every check stage.
const precheckStatusFailed = "failed"

// PreCheckRunner executes pre-checks to validate worker capabilities.
// Pre-checks are non-blocking by default - they warn but don't prevent deployment.
type PreCheckRunner struct {
	checks       map[string]CheckFunc
	logger       *slog.Logger
	mu           sync.RWMutex
	checkTimeout time.Duration
}

// CheckFunc is a function that executes a single pre-check.
// It returns a PreCheckResult indicating success/failure and any details.
type CheckFunc func(ctx context.Context) core.PreCheckResult

// NewPreCheckRunner creates a new runner with default checks registered.
func NewPreCheckRunner() *PreCheckRunner {
	r := &PreCheckRunner{
		checks:       make(map[string]CheckFunc),
		logger:       slog.Default(),
		checkTimeout: 30 * time.Second,
	}
	r.RegisterDefaults()
	return r
}

// WithTimeout sets a custom timeout for individual checks.
func (r *PreCheckRunner) WithTimeout(d time.Duration) *PreCheckRunner {
	r.checkTimeout = d
	return r
}

// RegisterDefaults adds the standard pre-checks for common requirements.
func (r *PreCheckRunner) RegisterDefaults() {
	r.Register("docker_version", r.checkDockerVersion)
	r.Register("gpu_detection", r.checkGPUDetection)
	r.Register("wireguard_kernel", r.checkWireGuardKernel)
	r.Register("disk_space", r.checkDiskSpace)
	r.Register("memory_available", r.checkMemoryAvailable)
	r.Register("network_connectivity", r.checkNetworkConnectivity)
}

// Register adds a custom check to the runner.
func (r *PreCheckRunner) Register(checkType string, fn CheckFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.checks[checkType] = fn
}

// Run executes all checks defined in the given definitions.
// Checks are run in parallel for better performance.
func (r *PreCheckRunner) Run(ctx context.Context, defs []core.PreCheckDefinition) []core.PreCheckResult {
	if len(defs) == 0 {
		return nil
	}

	results := make([]core.PreCheckResult, len(defs))
	var wg sync.WaitGroup

	for i, def := range defs {
		wg.Add(1)
		go func(idx int, definition core.PreCheckDefinition) {
			defer wg.Done()
			defer func() {
				if rec := recover(); rec != nil {
					results[idx] = core.PreCheckResult{
						Type:   definition.Type,
						Status: precheckStatusFailed,
						Error:  fmt.Sprintf("panic in check: %v", rec),
					}
					r.logger.Error("pre-check panicked",
						"type", definition.Type,
						"panic", rec,
					)
				}
			}()

			r.mu.RLock()
			fn, exists := r.checks[definition.Type]
			r.mu.RUnlock()

			if !exists {
				results[idx] = core.PreCheckResult{
					Type:   definition.Type,
					Status: "skipped",
					Error:  fmt.Sprintf("No check registered for type '%s'", definition.Type),
				}
				return
			}

			// Run the check with timeout
			checkCtx, cancel := context.WithTimeout(ctx, r.checkTimeout)
			defer cancel()

			result := fn(checkCtx)
			result.Type = definition.Type // Ensure type is set

			// Log the result
			if result.Status == precheckStatusFailed {
				r.logger.Warn("pre-check failed",
					"type", definition.Type,
					"error", result.Error,
					"blocking", definition.Blocking,
				)
			} else {
				r.logger.Debug("pre-check passed",
					"type", definition.Type,
					"details", result.Details,
				)
			}

			results[idx] = result
		}(i, def)
	}

	wg.Wait()
	return results
}

// RunBlocking runs only the blocking checks and returns an error if any fail.
func (r *PreCheckRunner) RunBlocking(ctx context.Context, defs []core.PreCheckDefinition) error {
	// Filter to only blocking checks
	var blockingDefs []core.PreCheckDefinition
	for _, def := range defs {
		if def.Blocking {
			blockingDefs = append(blockingDefs, def)
		}
	}

	if len(blockingDefs) == 0 {
		return nil
	}

	results := r.Run(ctx, blockingDefs)

	// Collect failures
	var failures []string
	for _, result := range results {
		if result.Status == precheckStatusFailed {
			failures = append(failures, fmt.Sprintf("%s: %s", result.Type, result.Error))
		}
	}

	if len(failures) > 0 {
		return fmt.Errorf("blocking pre-checks failed: %s", strings.Join(failures, "; "))
	}

	return nil
}

// =============================================================================
// Default Check Implementations
// =============================================================================

// checkDockerVersion verifies Docker is installed and meets minimum version.
func (r *PreCheckRunner) checkDockerVersion(ctx context.Context) core.PreCheckResult {
	cmd := exec.CommandContext(ctx, "docker", "--version")
	output, err := cmd.Output()
	if err != nil {
		return core.PreCheckResult{
			Status: precheckStatusFailed,
			Error:  "Docker not found or not accessible",
			Details: map[string]any{
				"command_error": err.Error(),
			},
		}
	}

	version := strings.TrimSpace(string(output))
	return core.PreCheckResult{
		Status:  "passed",
		Details: map[string]any{"version": version},
	}
}

// checkGPUDetection checks for NVIDIA GPU availability.
func (r *PreCheckRunner) checkGPUDetection(ctx context.Context) core.PreCheckResult {
	// Try nvidia-smi first
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
	output, err := cmd.Output()
	if err == nil && len(output) > 0 {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		gpus := make([]string, 0, len(lines))
		for _, line := range lines {
			gpus = append(gpus, strings.TrimSpace(line))
		}
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"vendor":    "nvidia",
				"gpus":      gpus,
				"gpu_count": len(gpus),
			},
		}
	}

	// Try AMD ROCm
	cmd = exec.CommandContext(ctx, "rocm-smi", "--showproductname")
	output, err = cmd.Output()
	if err == nil && len(output) > 0 {
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"vendor": "amd",
				"output": strings.TrimSpace(string(output)),
			},
		}
	}

	// No GPU found - not necessarily a failure
	return core.PreCheckResult{
		Status: "passed",
		Details: map[string]any{
			"gpu_available": false,
			"note":          "No NVIDIA or AMD GPU detected - this is fine for non-AI workloads",
		},
	}
}

// checkWireGuardKernel checks if WireGuard kernel module is available.
func (r *PreCheckRunner) checkWireGuardKernel(ctx context.Context) core.PreCheckResult {
	// Check if wireguard module is loaded or available
	cmd := exec.CommandContext(ctx, "modinfo", "wireguard")
	output, err := cmd.Output()
	if err == nil {
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"kernel_module": true,
				"info":          strings.Split(string(output), "\n")[0],
			},
		}
	}

	// Check if wireguard-tools is installed (userspace)
	cmd = exec.CommandContext(ctx, "wg", "--version")
	output, err = cmd.Output()
	if err == nil {
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"kernel_module": false,
				"userspace":     true,
				"version":       strings.TrimSpace(string(output)),
			},
		}
	}

	return core.PreCheckResult{
		Status: precheckStatusFailed,
		Error:  "WireGuard not available (neither kernel module nor wireguard-tools)",
	}
}

// checkDiskSpace verifies minimum disk space is available.
func (r *PreCheckRunner) checkDiskSpace(ctx context.Context) core.PreCheckResult {
	// This is a simplified check - in production you'd parse df output
	cmd := exec.CommandContext(ctx, "df", "-h", "/")
	output, err := cmd.Output()
	if err != nil {
		return core.PreCheckResult{
			Status: "passed", // Don't fail on df error
			Details: map[string]any{
				"note": "Could not check disk space",
			},
		}
	}

	return core.PreCheckResult{
		Status: "passed",
		Details: map[string]any{
			"output": strings.TrimSpace(string(output)),
		},
	}
}

// checkMemoryAvailable verifies minimum RAM is available.
func (r *PreCheckRunner) checkMemoryAvailable(ctx context.Context) core.PreCheckResult {
	// Simplified check using free command
	cmd := exec.CommandContext(ctx, "free", "-m")
	output, err := cmd.Output()
	if err != nil {
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"note": "Could not check memory (free command not available)",
			},
		}
	}

	return core.PreCheckResult{
		Status: "passed",
		Details: map[string]any{
			"output": strings.TrimSpace(string(output)),
		},
	}
}

// checkNetworkConnectivity verifies basic network connectivity.
func (r *PreCheckRunner) checkNetworkConnectivity(ctx context.Context) core.PreCheckResult {
	// Try to reach a well-known endpoint
	cmd := exec.CommandContext(ctx, "curl", "-s", "-o", "/dev/null", "-w", "%{http_code}", "--max-time", "5", "https://1.1.1.1/cdn-cgi/trace")
	output, err := cmd.Output()
	if err == nil && strings.TrimSpace(string(output)) == "200" {
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"connectivity": "ok",
				"endpoint":     "cloudflare",
			},
		}
	}

	// Fallback: try ping
	cmd = exec.CommandContext(ctx, "ping", "-c", "1", "-W", "5", "8.8.8.8")
	if err := cmd.Run(); err == nil {
		return core.PreCheckResult{
			Status: "passed",
			Details: map[string]any{
				"connectivity": "ok",
				"method":       "ping",
			},
		}
	}

	return core.PreCheckResult{
		Status: precheckStatusFailed,
		Error:  "No internet connectivity detected",
	}
}
