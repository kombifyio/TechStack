package guardposture

import "time"

// Policy holds every bound the control plane applies to posture. The defaults
// are conservative; none of them can be widened by a Guard or by a request.
type Policy struct {
	// BackupMaxAge is how old the newest snapshot receipt may be before the
	// backup is stale.
	BackupMaxAge time.Duration
	// RestoreDrillMaxAge is how old the newest verified staged-restore receipt
	// may be before the drill is stale.
	RestoreDrillMaxAge time.Duration
	// VerifyInterval is how often a Guard is asked to observe the host.
	VerifyInterval time.Duration
	// PendingSecurityAlertAfter and RebootAlertAfter delay the alert of a
	// control that StackKits judges drifted at once: unattended upgrades get
	// this long to clear it before the owner is told.
	PendingSecurityAlertAfter time.Duration
	RebootAlertAfter          time.Duration
	Repair                    RepairPolicy
}

// RepairPolicy bounds the repair of a drifted control.
type RepairPolicy struct {
	// Expiry is how long an admitted repair may wait for its Guard.
	Expiry time.Duration
	// ControlCooldown is the minimum time between two repairs of one control.
	ControlCooldown time.Duration
	// MaxAttempts is how many repairs of one control are admitted per day.
	MaxAttempts int
	// BreakerFailures consecutive failed repairs of one server open its circuit
	// breaker; it stays open for BreakerOpenFor.
	BreakerFailures int
	BreakerOpenFor  time.Duration
	// AutoRepairMaxTTL bounds how long an owner approval of automatic repair
	// lives; it never renews itself.
	AutoRepairMaxTTL time.Duration
}

// DefaultPolicy returns the shipped bounds.
func DefaultPolicy() Policy {
	return Policy{
		BackupMaxAge:              36 * time.Hour,
		RestoreDrillMaxAge:        35 * 24 * time.Hour,
		VerifyInterval:            15 * time.Minute,
		PendingSecurityAlertAfter: 72 * time.Hour,
		RebootAlertAfter:          7 * 24 * time.Hour,
		Repair: RepairPolicy{
			Expiry:           10 * time.Minute,
			ControlCooldown:  30 * time.Minute,
			MaxAttempts:      3,
			BreakerFailures:  3,
			BreakerOpenFor:   6 * time.Hour,
			AutoRepairMaxTTL: 90 * 24 * time.Hour,
		},
	}
}

// Normalize replaces a non-positive bound with its default, so a partially
// configured policy is never unbounded.
func (p Policy) Normalize() Policy {
	d := DefaultPolicy()
	pick := func(v, def time.Duration) time.Duration {
		if v <= 0 {
			return def
		}
		return v
	}
	p.BackupMaxAge = pick(p.BackupMaxAge, d.BackupMaxAge)
	p.RestoreDrillMaxAge = pick(p.RestoreDrillMaxAge, d.RestoreDrillMaxAge)
	p.VerifyInterval = pick(p.VerifyInterval, d.VerifyInterval)
	p.PendingSecurityAlertAfter = pick(p.PendingSecurityAlertAfter, d.PendingSecurityAlertAfter)
	p.RebootAlertAfter = pick(p.RebootAlertAfter, d.RebootAlertAfter)
	p.Repair.Expiry = pick(p.Repair.Expiry, d.Repair.Expiry)
	p.Repair.ControlCooldown = pick(p.Repair.ControlCooldown, d.Repair.ControlCooldown)
	p.Repair.BreakerOpenFor = pick(p.Repair.BreakerOpenFor, d.Repair.BreakerOpenFor)
	p.Repair.AutoRepairMaxTTL = pick(p.Repair.AutoRepairMaxTTL, d.Repair.AutoRepairMaxTTL)
	if p.Repair.MaxAttempts <= 0 {
		p.Repair.MaxAttempts = d.Repair.MaxAttempts
	}
	if p.Repair.BreakerFailures <= 0 {
		p.Repair.BreakerFailures = d.Repair.BreakerFailures
	}
	return p
}
