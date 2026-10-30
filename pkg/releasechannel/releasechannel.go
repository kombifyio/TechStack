// Package releasechannel decides which deployment runs a homelab's work
// (CI-CD-PLATFORM-STANDARD §4.6 "Channel model").
//
// Stable never knows Preview: it reads no list of Preview people, hands no
// tenant over and sends nobody to Preview. Preview is the internal development
// preview at its own address, so stable runs the work of every tenant and
// Preview runs none by default. Preview uses the non-production Stage database
// with synthetic data; ordinary tenant agents still connect to Stable.
// The governed Stage database can bind its one verified synthetic owner through
// native product admission; it never runs shared/global work.
package releasechannel

import "github.com/kombifyio/techstack/pkg/config"

// Running is the channel this deployment serves.
func Running() string { return config.ReleaseChannel() }

// OwnsSubject reports whether this deployment runs subject's homelab work:
// stable runs everyone; Preview can run its verified Stage owner only.
func OwnsSubject(subject string) bool { return !config.SecondaryChannel() || ownsStageSubject(subject) }

// OwnsTenant reports whether this deployment runs tenant's work: stable runs
// every tenant; Preview can run its verified Stage owner only.
func OwnsTenant(tenant string) bool { return !config.SecondaryChannel() || ownsStageTenant(tenant) }
