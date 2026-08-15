// Package agent provides the agent-side pre-check execution for kombifyTechstack.
package agent

import (
	"context"
	"fmt"
	"net"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

// PreCheckStatus represents the outcome of a pre-check.
type PreCheckStatus string

const (
	PreCheckStatusPassed  PreCheckStatus = "passed"
	PreCheckStatusFailed  PreCheckStatus = "failed"
	PreCheckStatusSkipped PreCheckStatus = "skipped"
	PreCheckStatusWarning PreCheckStatus = "warning"
)

// PreCheckResult is the outcome of running a pre-check definition.
// This mirrors core.PreCheckResult but includes agent-specific fields.
type PreCheckResult struct {
	Type    string         `json:"type"`
	Status  PreCheckStatus `json:"status"`
	Message string         `json:"message,omitempty"`
	Details map[string]any `json:"details,omitempty"`
	Error   string         `json:"error,omitempty"`
}

// ToCoreResult converts to the core.PreCheckResult type for gRPC transport.
func (r *PreCheckResult) ToCoreResult() core.PreCheckResult {
	return core.PreCheckResult{
		Type:    r.Type,
		Status:  string(r.Status),
		Details: r.Details,
		Error:   r.Error,
	}
}

// PreCheckRunner executes pre-check definitions on the agent.
type PreCheckRunner struct {
	checks  []core.PreCheckDefinition
	timeout time.Duration
}

// NewPreCheckRunner creates a new PreCheckRunner with the given checks.
func NewPreCheckRunner(checks []core.PreCheckDefinition) *PreCheckRunner {
	return &PreCheckRunner{
		checks:  checks,
		timeout: 30 * time.Second, // Default timeout per check
	}
}

// WithTimeout sets the timeout for each individual check.
func (r *PreCheckRunner) WithTimeout(timeout time.Duration) *PreCheckRunner {
	r.timeout = timeout
	return r
}

// SetChecks replaces the current checks with new ones.
func (r *PreCheckRunner) SetChecks(checks []core.PreCheckDefinition) {
	r.checks = checks
}

// RunAll executes all registered pre-checks and returns results.
func (r *PreCheckRunner) RunAll(ctx context.Context) []PreCheckResult {
	results := make([]PreCheckResult, 0, len(r.checks))
	for _, check := range r.checks {
		result := r.runCheck(ctx, check)
		results = append(results, result)
	}
	return results
}

// RunBlocking executes only blocking checks and returns true if all passed.
func (r *PreCheckRunner) RunBlocking(ctx context.Context) ([]PreCheckResult, bool) {
	var blockingResults []PreCheckResult
	allPassed := true

	for _, check := range r.checks {
		if !check.Blocking {
			continue
		}
		result := r.runCheck(ctx, check)
		blockingResults = append(blockingResults, result)
		if result.Status == PreCheckStatusFailed {
			allPassed = false
		}
	}

	return blockingResults, allPassed
}

// runCheck executes a single pre-check with timeout.
func (r *PreCheckRunner) runCheck(ctx context.Context, check core.PreCheckDefinition) PreCheckResult {
	// Create context with timeout
	checkCtx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	// Execute check based on type
	switch check.Type {
	case "docker_version":
		return r.checkDockerVersion(checkCtx, check)
	case "gpu_available":
		return r.checkGPUAvailable(checkCtx, check)
	case "disk_space":
		return r.checkDiskSpace(checkCtx, check)
	case "ram_available":
		return r.checkRAMAvailable(checkCtx, check)
	case "port_available":
		return r.checkPortAvailable(checkCtx, check)
	default:
		return PreCheckResult{
			Type:    check.Type,
			Status:  PreCheckStatusSkipped,
			Message: fmt.Sprintf("unknown check type: %s", check.Type),
		}
	}
}

// checkDockerVersion verifies Docker is installed and meets minimum version.
func (r *PreCheckRunner) checkDockerVersion(ctx context.Context, check core.PreCheckDefinition) PreCheckResult {
	result := PreCheckResult{
		Type:    check.Type,
		Details: make(map[string]any),
	}

	// Run docker version command
	cmd := exec.CommandContext(ctx, "docker", "version", "--format", "{{.Server.Version}}")
	output, err := cmd.Output()
	if err != nil {
		result.Status = PreCheckStatusFailed
		result.Error = fmt.Sprintf("docker not available: %v", err)
		return result
	}

	version := strings.TrimSpace(string(output))
	result.Details["version"] = version
	result.Details["minVersion"] = check.MinVersion

	// Check minimum version if specified
	if check.MinVersion != "" {
		if !isVersionAtLeast(version, check.MinVersion) {
			result.Status = PreCheckStatusFailed
			result.Error = fmt.Sprintf("docker version %s is below minimum %s", version, check.MinVersion)
			return result
		}
	}

	result.Status = PreCheckStatusPassed
	result.Message = fmt.Sprintf("Docker version %s installed", version)
	return result
}

// checkGPUAvailable checks for NVIDIA GPU availability via nvidia-smi.
func (r *PreCheckRunner) checkGPUAvailable(ctx context.Context, check core.PreCheckDefinition) PreCheckResult {
	result := PreCheckResult{
		Type:    check.Type,
		Details: make(map[string]any),
	}

	// Try nvidia-smi for NVIDIA GPUs
	cmd := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader")
	output, err := cmd.Output()
	if err != nil {
		// GPU not available or nvidia-smi not installed
		if check.Blocking {
			result.Status = PreCheckStatusFailed
			result.Error = "NVIDIA GPU not detected (nvidia-smi failed)"
		} else {
			result.Status = PreCheckStatusWarning
			result.Message = "NVIDIA GPU not detected"
		}
		return result
	}

	gpuInfo := strings.TrimSpace(string(output))
	if gpuInfo == "" {
		if check.Blocking {
			result.Status = PreCheckStatusFailed
			result.Error = "no GPU found"
		} else {
			result.Status = PreCheckStatusWarning
			result.Message = "no GPU found"
		}
		return result
	}

	// Parse GPU info
	lines := strings.Split(gpuInfo, "\n")
	gpus := make([]map[string]string, 0, len(lines))
	for _, line := range lines {
		parts := strings.Split(line, ", ")
		if len(parts) >= 2 {
			gpus = append(gpus, map[string]string{
				"name":   strings.TrimSpace(parts[0]),
				"memory": strings.TrimSpace(parts[1]),
			})
		}
	}

	result.Details["gpus"] = gpus
	result.Details["count"] = len(gpus)
	result.Status = PreCheckStatusPassed
	result.Message = fmt.Sprintf("%d GPU(s) detected", len(gpus))
	return result
}

// checkDiskSpace verifies available disk space meets requirements.
func (r *PreCheckRunner) checkDiskSpace(ctx context.Context, check core.PreCheckDefinition) PreCheckResult {
	result := PreCheckResult{
		Type:    check.Type,
		Details: make(map[string]any),
	}

	// Get disk usage (platform-specific function from resource_*.go)
	used, total := getDiskUsage()
	if total == 0 {
		result.Status = PreCheckStatusFailed
		result.Error = "failed to get disk space information"
		return result
	}

	available := total - used
	availableGB := float64(available) / (1024 * 1024 * 1024)
	totalGB := float64(total) / (1024 * 1024 * 1024)
	usedGB := float64(used) / (1024 * 1024 * 1024)

	result.Details["availableGB"] = fmt.Sprintf("%.2f", availableGB)
	result.Details["totalGB"] = fmt.Sprintf("%.2f", totalGB)
	result.Details["usedGB"] = fmt.Sprintf("%.2f", usedGB)
	result.Details["usedPercent"] = fmt.Sprintf("%.1f%%", (float64(used)/float64(total))*100)

	// Parse minimum requirement from MinVersion field (e.g., "50GB")
	minGB := parseStorageSize(check.MinVersion)
	if minGB > 0 {
		result.Details["requiredGB"] = minGB
		if availableGB < float64(minGB) {
			result.Status = PreCheckStatusFailed
			result.Error = fmt.Sprintf("insufficient disk space: %.2f GB available, %d GB required", availableGB, minGB)
			return result
		}
	}

	result.Status = PreCheckStatusPassed
	result.Message = fmt.Sprintf("%.2f GB available (%.1f%% used)", availableGB, (float64(used)/float64(total))*100)
	return result
}

// checkRAMAvailable verifies total RAM meets requirements.
func (r *PreCheckRunner) checkRAMAvailable(ctx context.Context, check core.PreCheckDefinition) PreCheckResult {
	result := PreCheckResult{
		Type:    check.Type,
		Details: make(map[string]any),
	}

	// Get memory info (platform-specific)
	totalRAM := getTotalRAM()
	if totalRAM == 0 {
		result.Status = PreCheckStatusFailed
		result.Error = "failed to get RAM information"
		return result
	}

	totalGB := float64(totalRAM) / (1024 * 1024 * 1024)
	result.Details["totalGB"] = fmt.Sprintf("%.2f", totalGB)

	// Parse minimum requirement from MinVersion field (e.g., "8GB" or "8192MB")
	minGB := parseStorageSize(check.MinVersion)
	if minGB > 0 {
		result.Details["requiredGB"] = minGB
		if totalGB < float64(minGB) {
			result.Status = PreCheckStatusFailed
			result.Error = fmt.Sprintf("insufficient RAM: %.2f GB available, %d GB required", totalGB, minGB)
			return result
		}
	}

	result.Status = PreCheckStatusPassed
	result.Message = fmt.Sprintf("%.2f GB RAM available", totalGB)
	return result
}

// checkPortAvailable verifies specified ports are not in use.
func (r *PreCheckRunner) checkPortAvailable(ctx context.Context, check core.PreCheckDefinition) PreCheckResult {
	result := PreCheckResult{
		Type:    check.Type,
		Details: make(map[string]any),
	}

	// Parse port from MinVersion field (e.g., "80" or "80,443,8080")
	portStr := check.MinVersion
	if portStr == "" {
		result.Status = PreCheckStatusSkipped
		result.Message = "no port specified"
		return result
	}

	ports := strings.Split(portStr, ",")
	var unavailablePorts []string
	var availablePorts []string

	for _, portStr := range ports {
		port := strings.TrimSpace(portStr)
		if port == "" {
			continue
		}

		// Try to listen on the port
		listener, err := net.Listen("tcp", ":"+port)
		if err != nil {
			unavailablePorts = append(unavailablePorts, port)
		} else {
			listener.Close()
			availablePorts = append(availablePorts, port)
		}
	}

	result.Details["checkedPorts"] = ports
	result.Details["availablePorts"] = availablePorts
	result.Details["unavailablePorts"] = unavailablePorts

	if len(unavailablePorts) > 0 {
		result.Status = PreCheckStatusFailed
		result.Error = fmt.Sprintf("ports already in use: %s", strings.Join(unavailablePorts, ", "))
		return result
	}

	result.Status = PreCheckStatusPassed
	result.Message = fmt.Sprintf("all ports available: %s", strings.Join(availablePorts, ", "))
	return result
}

// isVersionAtLeast compares two semver-like version strings.
// Returns true if actual >= required.
func isVersionAtLeast(actual, required string) bool {
	// Normalize versions by removing leading 'v' and trimming
	actual = strings.TrimPrefix(strings.TrimSpace(actual), "v")
	required = strings.TrimPrefix(strings.TrimSpace(required), "v")

	// Split into parts
	actualParts := strings.Split(actual, ".")
	requiredParts := strings.Split(required, ".")

	// Compare each part
	for i := 0; i < len(requiredParts); i++ {
		if i >= len(actualParts) {
			return false
		}

		// Parse as integers, handling non-numeric suffixes
		actualNum := parseVersionPart(actualParts[i])
		requiredNum := parseVersionPart(requiredParts[i])

		if actualNum > requiredNum {
			return true
		}
		if actualNum < requiredNum {
			return false
		}
	}

	return true
}

// parseVersionPart extracts the leading integer from a version part.
func parseVersionPart(part string) int {
	// Extract leading digits
	var numStr string
	for _, c := range part {
		if c >= '0' && c <= '9' {
			numStr += string(c)
		} else {
			break
		}
	}
	if numStr == "" {
		return 0
	}
	num, _ := strconv.Atoi(numStr)
	return num
}

// parseStorageSize parses storage size strings like "50GB", "8192MB", "1TB".
// Returns the size in GB.
func parseStorageSize(s string) int {
	s = strings.TrimSpace(strings.ToUpper(s))
	if s == "" {
		return 0
	}

	var num int
	var unit string

	// Extract number and unit
	for i, c := range s {
		if c >= '0' && c <= '9' {
			continue
		}
		num, _ = strconv.Atoi(s[:i])
		unit = s[i:]
		break
	}

	// If no unit found, try parsing the whole string as a number
	if unit == "" {
		num, _ = strconv.Atoi(s)
		return num // Assume GB if no unit
	}

	switch unit {
	case "TB":
		return num * 1024
	case "GB":
		return num
	case "MB":
		return num / 1024
	default:
		return num // Assume GB for unknown units
	}
}

// getTotalRAM returns the total system RAM in bytes.
// Platform-specific implementations are in resource_*.go files.
func getTotalRAM() uint64 {
	// This is a simple implementation using Go's runtime
	// Real implementation would use platform-specific syscalls
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	// memStats.Sys is the total memory obtained from the OS
	// For a more accurate system RAM, we need platform-specific code
	return getTotalSystemRAM()
}
