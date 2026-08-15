package monitoring

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql"
)

// AlertRule defines a PromQL-based alerting rule.
type AlertRule struct {
	Name     string            `json:"name"`
	Expr     string            `json:"expr"`     // PromQL expression that fires when result > 0
	For      time.Duration     `json:"for"`      // How long condition must hold before firing
	Severity string            `json:"severity"` // info, warning, critical
	Labels   map[string]string `json:"labels,omitempty"`
	Message  string            `json:"message"` // Go-style message template (use %v for value)
}

// AlertState tracks the current state of an alert rule evaluation.
type AlertState struct {
	Rule        AlertRule  `json:"rule"`
	Active      bool       `json:"active"`       // Currently firing
	Value       float64    `json:"value"`        // Last evaluated value
	ActiveSince *time.Time `json:"active_since"` // When condition first became true
	FiredAt     *time.Time `json:"fired_at"`     // When alert was actually sent (after For duration)
	LastEval    time.Time  `json:"last_eval"`
}

// AlertEngine evaluates PromQL alert rules against the active monitoring backend
// on a schedule.
type AlertEngine struct {
	backend  MetricsQueryBackend
	notifier Notifier
	rules    []AlertRule
	states   map[string]*AlertState // keyed by rule name
	interval time.Duration
	logger   *slog.Logger
	mu       sync.RWMutex
}

// AlertEngineConfig configures the alert evaluation engine.
type AlertEngineConfig struct {
	Interval time.Duration // Evaluation interval (default: 30s)
	Logger   *slog.Logger
}

// NewAlertEngine creates an alert engine that evaluates rules against the
// currently configured monitoring query backend.
func NewAlertEngine(backend MetricsQueryBackend, notifier Notifier, rules []AlertRule, cfg AlertEngineConfig) *AlertEngine {
	if cfg.Interval <= 0 {
		cfg.Interval = 30 * time.Second
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}

	states := make(map[string]*AlertState, len(rules))
	for _, r := range rules {
		states[r.Name] = &AlertState{Rule: r}
	}

	return &AlertEngine{
		backend:  backend,
		notifier: notifier,
		rules:    rules,
		states:   states,
		interval: cfg.Interval,
		logger:   cfg.Logger,
	}
}

// DefaultAlertRules returns built-in alert rules for common infrastructure conditions.
func DefaultAlertRules() []AlertRule {
	return []AlertRule{
		{
			Name:     "HighCPU",
			Expr:     `node_cpu_usage_percent{core="total"} > 90`,
			For:      5 * time.Minute,
			Severity: "critical",
			Message:  "CPU usage above 90%% on agent",
		},
		{
			Name:     "LowMemory",
			Expr:     `(node_memory_available_bytes / node_memory_total_bytes) * 100 < 10`,
			For:      5 * time.Minute,
			Severity: "critical",
			Message:  "Available memory below 10%%",
		},
		{
			Name:     "DiskFull",
			Expr:     `node_disk_usage_percent > 90`,
			For:      0,
			Severity: "warning",
			Message:  "Disk usage above 90%%",
		},
		{
			Name:     "ContainerDown",
			Expr:     `container_running == 0`,
			For:      1 * time.Minute,
			Severity: "warning",
			Message:  "Container is not running",
		},
		{
			Name:     "ProcessMissing",
			Expr:     `process_pattern_up == 0`,
			For:      2 * time.Minute,
			Severity: "warning",
			Message:  "Watched process not found",
		},
		{
			// server_registry_outbox is bounded by the registry sweeper's 30d
			// retention prune until the K6 projector lands; a growing estimate
			// means the prune is not keeping up with accepted revisions.
			Name:     "ServerRegistryOutboxBacklog",
			Expr:     `server_registry_outbox_rows_estimate > 500000`,
			For:      30 * time.Minute,
			Severity: "warning",
			Message:  "server_registry_outbox above 500k rows; retention prune is not keeping up (%v rows)",
		},
		{
			// Oldest row far beyond the 30d retention window means the prune
			// loop has stalled entirely.
			Name:     "ServerRegistryOutboxPruneStalled",
			Expr:     `server_registry_outbox_oldest_age_seconds > 3456000`,
			For:      1 * time.Hour,
			Severity: "warning",
			Message:  "server_registry_outbox oldest row is older than 40 days; retention prune stalled (%v seconds)",
		},
	}
}

// Run starts the alert evaluation loop. Blocks until ctx is canceled.
func (e *AlertEngine) Run(ctx context.Context) {
	ticker := time.NewTicker(e.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			e.evaluate(ctx)
		}
	}
}

// ActiveAlerts returns all currently firing alerts.
func (e *AlertEngine) ActiveAlerts() []AlertState {
	e.mu.RLock()
	defer e.mu.RUnlock()

	var active []AlertState
	for _, s := range e.states {
		if s.Active && s.FiredAt != nil {
			active = append(active, *s)
		}
	}
	return active
}

// AllStates returns the current state of all alert rules.
func (e *AlertEngine) AllStates() []AlertState {
	e.mu.RLock()
	defer e.mu.RUnlock()

	all := make([]AlertState, 0, len(e.states))
	for _, s := range e.states {
		all = append(all, *s)
	}
	return all
}

// AddRule adds a new alert rule at runtime.
func (e *AlertEngine) AddRule(rule AlertRule) {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.rules = append(e.rules, rule)
	e.states[rule.Name] = &AlertState{Rule: rule}
}

// RemoveRule removes an alert rule by name.
func (e *AlertEngine) RemoveRule(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()

	for i, r := range e.rules {
		if r.Name == name {
			e.rules = append(e.rules[:i], e.rules[i+1:]...)
			delete(e.states, name)
			return true
		}
	}
	return false
}

func (e *AlertEngine) evaluate(ctx context.Context) {
	e.mu.Lock()
	defer e.mu.Unlock()

	now := time.Now()

	for _, rule := range e.rules {
		state := e.states[rule.Name]
		state.LastEval = now

		result, err := e.backend.InstantQuery(ctx, rule.Expr, now)
		if err != nil {
			e.logger.Debug("alert eval error", "rule", rule.Name, "error", err)
			continue
		}

		firing := isResultFiring(result)

		if firing {
			if !state.Active {
				// Transition: inactive -> pending
				state.Active = true
				state.ActiveSince = &now
				state.Value = extractValue(result)
				e.logger.Debug("alert pending", "rule", rule.Name, "value", state.Value)
			}

			// Check if For duration has elapsed
			if state.FiredAt == nil && state.ActiveSince != nil {
				if now.Sub(*state.ActiveSince) >= rule.For {
					state.FiredAt = &now
					state.Value = extractValue(result)
					e.logger.Info("alert fired", "rule", rule.Name, "severity", rule.Severity, "value", state.Value)

					// Send notification
					if e.notifier != nil {
						alert := Alert{
							RuleName: rule.Name,
							Severity: rule.Severity,
							Message:  rule.Message,
							Value:    state.Value,
							Labels:   alertLabels(rule.Labels, result),
							FiredAt:  now,
						}
						if err := e.notifier.Notify(ctx, alert); err != nil {
							e.logger.Warn("alert notify failed", "rule", rule.Name, "notifier", e.notifier.Name(), "error", err)
						}
					}
				}
			}
		} else if state.Active {
			// Transition: active -> resolved
			e.logger.Info("alert resolved", "rule", rule.Name)

			if e.notifier != nil && state.FiredAt != nil {
				resolved := Alert{
					RuleName:   rule.Name,
					Severity:   "resolved",
					Message:    fmt.Sprintf("RESOLVED: %s", rule.Message),
					Value:      0,
					Labels:     rule.Labels,
					FiredAt:    *state.FiredAt,
					ResolvedAt: &now,
				}
				if err := e.notifier.Notify(ctx, resolved); err != nil {
					e.logger.Warn("alert resolve notify failed", "rule", rule.Name, "error", err)
				}
			}

			state.Active = false
			state.ActiveSince = nil
			state.FiredAt = nil
			state.Value = 0
		}
	}
}

func alertLabels(ruleLabels map[string]string, result *QueryResult) map[string]string {
	merged := make(map[string]string, len(ruleLabels)+4)
	for key, value := range ruleLabels {
		merged[key] = value
	}
	if result == nil {
		return merged
	}
	if vec, ok := result.Value.(promql.Vector); ok && len(vec) > 0 {
		vec[0].Metric.Range(func(label labels.Label) {
			if _, exists := merged[label.Name]; !exists {
				merged[label.Name] = label.Value
			}
		})
	}
	return merged
}

// isResultFiring checks if a PromQL result indicates an active condition.
// A result is firing if it returns any non-empty vector.
func isResultFiring(result *QueryResult) bool {
	if result == nil || result.Value == nil {
		return false
	}
	str := result.Value.String()
	// Empty vector = no match = not firing
	return str != "" && !strings.Contains(str, "=> 0 @") && str != "{}"
}

// extractValue gets the first numeric value from a PromQL result.
func extractValue(result *QueryResult) float64 {
	if result == nil || result.Value == nil {
		return 0
	}

	// Try to extract from Vector type
	if vec, ok := result.Value.(promql.Vector); ok && len(vec) > 0 {
		return vec[0].F
	}

	return 0
}
