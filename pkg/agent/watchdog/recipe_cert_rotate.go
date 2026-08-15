package watchdog

import (
	"context"
	"fmt"
	"time"
)

const (
	certRotateName        = "cert-rotate"
	certExpiryThreshold   = 7 * 24 * time.Hour // 7 days
	certRotateEstDuration = 30 * time.Second
)

// CertRotate detects when the agent's mTLS certificate is about to
// expire (< 7 days) and initiates a certificate rotation via Core.
type CertRotate struct {
	enabled bool
}

// NewCertRotate creates an enabled CertRotate recipe.
func NewCertRotate() *CertRotate {
	return &CertRotate{enabled: true}
}

func (r *CertRotate) Name() string      { return certRotateName }
func (r *CertRotate) AutoExec() bool    { return true }
func (r *CertRotate) Enabled() bool     { return r.enabled }
func (r *CertRotate) SetEnabled(v bool) { r.enabled = v }

func (r *CertRotate) Check(_ context.Context, state *SystemState) (*Condition, error) {
	if state == nil {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	if state.CertExpiresAt.IsZero() {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	remaining := time.Until(state.CertExpiresAt)
	if remaining > certExpiryThreshold {
		return &Condition{Triggered: false, Recipe: r.Name()}, nil
	}

	severity := "warning"
	if remaining < 24*time.Hour {
		severity = "critical"
	}

	return &Condition{
		Triggered: true,
		Recipe:    r.Name(),
		Reason:    fmt.Sprintf("mTLS cert expires in %s (threshold %s)", remaining.Round(time.Hour), certExpiryThreshold),
		Severity:  severity,
		Evidence: map[string]string{
			"expires_at": state.CertExpiresAt.Format(time.RFC3339),
			"remaining":  remaining.String(),
		},
	}, nil
}

func (r *CertRotate) DryRun(_ context.Context, cond *Condition) (*Plan, error) {
	return &Plan{
		Steps: []string{
			"generate CSR with current key parameters",
			"send CSR to Core for signing",
			"receive and validate new certificate",
			"atomic swap of cert + key files",
		},
		DryRunOutput: fmt.Sprintf("would rotate cert expiring at %s",
			cond.Evidence["expires_at"]),
		RollbackHint: "restore previous cert + key from backup",
		EstDuration:  certRotateEstDuration,
	}, nil
}

func (r *CertRotate) Execute(_ context.Context, _ *Plan) (*Result, error) {
	// Stub: in production this would generate a CSR, call Core, and
	// atomically swap the leaf certificate.
	return &Result{
		Success:  true,
		Output:   "certificate rotated (stub)",
		Duration: certRotateEstDuration,
		RollbackData: map[string]string{
			"previous_cert_path": "/etc/kombify/agent/tls/cert.pem.bak",
			"previous_key_path":  "/etc/kombify/agent/tls/key.pem.bak",
		},
	}, nil
}

func (r *CertRotate) Rollback(_ context.Context, result *Result) (*Result, error) {
	if result == nil || len(result.RollbackData) == 0 {
		return &Result{Success: true, Output: "nothing to rollback"}, nil
	}
	return &Result{
		Success:  true,
		Output:   "restored previous certificate (stub rollback)",
		Duration: 2 * time.Second,
	}, nil
}
