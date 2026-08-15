// Package detection provides detection engines that analyze aggregated
// server data and produce action cards for proactive intelligence.
// Engines run in the Techstack-Core backend, not on the server agent.
package detection

import (
	"context"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

// Engine is the interface all detection engines implement.
type Engine interface {
	// Name returns a stable identifier (e.g. "update-scanner").
	Name() string

	// Evaluate inspects the provided server data and returns zero or more
	// action card proposals. The caller persists them via the actions.Store.
	Evaluate(ctx context.Context, input *EvaluationInput) ([]*actions.ActionCard, error)
}

// EvaluationInput is the aggregated data a detection engine inspects.
type EvaluationInput struct {
	ServerID string
	OwnerID  string

	// OSPackages lists installed packages with available updates.
	OSPackages []PackageInfo

	// ErrorPatterns contains recent error log patterns grouped by service.
	ErrorPatterns map[string][]ErrorPattern

	// ResourceMetrics holds the latest resource usage snapshot.
	ResourceMetrics *ResourceSnapshot

	// DriftEntries lists known config-drift items (from Pillar 3).
	DriftEntries []DriftEntry

	// SecurityFindings contains CVE/port/cert findings.
	SecurityFindings []SecurityFinding
}

// PackageInfo describes an installed package and its available update.
type PackageInfo struct {
	Name           string
	CurrentVersion string
	LatestVersion  string
	CVESeverity    string // "", "low", "medium", "high", "critical"
	CVEIDs         []string
}

// ErrorPattern describes a recurring error in service logs.
type ErrorPattern struct {
	Pattern   string
	Count     int
	FirstSeen string
	LastSeen  string
	Service   string
}

// ResourceSnapshot holds resource usage at a point in time.
type ResourceSnapshot struct {
	CPUPercent  float64
	MemPercent  float64
	DiskPercent float64
}

// DriftEntry describes a single config drift between spec and actual.
type DriftEntry struct {
	Path     string
	Expected string
	Actual   string
	Service  string
}

// SecurityFinding describes a single security issue.
type SecurityFinding struct {
	Type        string // "cve", "open_port", "expired_cert"
	Severity    string // "low", "medium", "high", "critical"
	Description string
	Remediation string
}
