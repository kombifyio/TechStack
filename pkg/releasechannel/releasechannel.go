// Package releasechannel decides which deployment runs a homelab's work
// (CI-CD-PLATFORM-STANDARD §4.6 "Channel model").
//
// Stable runs the work of every tenant. Early Access is the same product at
// the Stable address with production configuration and database; it runs no
// shared or background work, so it owns no tenant here.
package releasechannel

import (
	"errors"

	"github.com/kombifyio/techstack/pkg/config"
)

var ErrTenantNotOwned = errors.New("deployment does not own tenant execution")

// Running is the channel this deployment serves.
func Running() string { return config.ReleaseChannel() }

// OwnsSubject reports whether this deployment runs subject's homelab work.
func OwnsSubject(string) bool { return !config.SecondaryChannel() }

// OwnsTenant reports whether this deployment runs tenant's work.
func OwnsTenant(string) bool { return !config.SecondaryChannel() }
