package watchdog

import (
	"context"
	"fmt"
	"time"
)

const (
	tunnelReconnectName        = "tunnel-reconnect"
	heartbeatLostThreshold     = 90 * time.Second // 3 cycles at 30s
	tunnelReconnectEstDuration = 15 * time.Second
)

// TunnelReconnect detects when the agent's heartbeat to Core has been
// lost for longer than heartbeatLostThreshold and restarts the tunnel
// process (cloudflared or wireguard).
type TunnelReconnect struct {
	enabled bool
}

// NewTunnelReconnect creates an enabled TunnelReconnect recipe.
func NewTunnelReconnect() *TunnelReconnect {
	return &TunnelReconnect{enabled: true}
}

func (r *TunnelReconnect) Name() string      { return tunnelReconnectName }
func (r *TunnelReconnect) AutoExec() bool    { return true }
func (r *TunnelReconnect) Enabled() bool     { return r.enabled }
func (r *TunnelReconnect) SetEnabled(v bool) { r.enabled = v }

func (r *TunnelReconnect) Check(_ context.Context, state *SystemState) (*Condition, error) {
	if state == nil {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	if state.LastHeartbeatAt.IsZero() {
		// No heartbeat data yet — skip.
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	gap := state.CollectedAt.Sub(state.LastHeartbeatAt)
	if gap < heartbeatLostThreshold {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	return &Condition{
		Triggered: true,
		Recipe:    r.Name(),
		Reason:    fmt.Sprintf("heartbeat lost for %s (threshold %s)", gap.Round(time.Second), heartbeatLostThreshold),
		Severity:  "critical",
		Evidence: map[string]string{
			"last_heartbeat": state.LastHeartbeatAt.Format(time.RFC3339),
			"gap":            gap.String(),
		},
	}, nil
}

func (r *TunnelReconnect) DryRun(_ context.Context, cond *Condition) (*Plan, error) {
	return &Plan{
		Steps: []string{
			"kill cloudflared/wireguard process",
			"restart tunnel daemon",
		},
		DryRunOutput: "would kill + restart tunnel daemon",
		RollbackHint: "stop tunnel daemon",
		EstDuration:  tunnelReconnectEstDuration,
	}, nil
}

func (r *TunnelReconnect) Execute(_ context.Context, _ *Plan) (*Result, error) {
	// Stub: in production this would signal the tunnel process.
	return &Result{
		Success:  true,
		Output:   "tunnel daemon restarted (stub)",
		Duration: tunnelReconnectEstDuration,
		RollbackData: map[string]string{
			"action": "restart-tunnel",
		},
	}, nil
}

func (r *TunnelReconnect) Rollback(_ context.Context, _ *Result) (*Result, error) {
	return &Result{
		Success:  true,
		Output:   "tunnel daemon stopped (stub rollback)",
		Duration: 2 * time.Second,
	}, nil
}
