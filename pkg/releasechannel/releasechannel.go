// Package releasechannel decides which deployment runs a homelab's work
// (CI-CD-PLATFORM-STANDARD §4.6 "Channel model").
//
// Stable never knows Preview: it reads no list of Preview people, hands no
// tenant over and sends nobody to Preview. Preview is the internal development
// preview at its own address, so stable runs the work of every tenant and
// Preview runs none; both deployments share the production database (real users
// switch to Preview, D-2026-10-22-04), so no tenant's work ever runs twice.
package releasechannel

import "github.com/kombifyio/techstack/pkg/config"

// Running is the channel this deployment serves.
func Running() string { return config.ReleaseChannel() }

// OwnsSubject reports whether this deployment runs subject's homelab work:
// stable runs everyone, Preview and Early Access no one.
func OwnsSubject(string) bool { return !config.SecondaryChannel() }

// OwnsTenant reports whether this deployment runs tenant's work: stable runs
// every tenant, Preview and Early Access none.
func OwnsTenant(string) bool { return !config.SecondaryChannel() }
