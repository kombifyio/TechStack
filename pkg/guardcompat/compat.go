// Package guardcompat is the frozen command/enrollment compatibility contract
// between the Techstack control plane and kombify Guard (Guard G01,
// kombify-Techstack-b57.12.1).
//
// It names the contract versions both sides speak and every policy limit the
// first-delivery plan asks G01 to settle: bootstrap and redemption lifetimes,
// key-rotation overlap, command admission lifetime (which is also the upper
// bound on unobserved offline revocation), result clock margin, journal
// entry/payload/disk limits, the deduplication horizon, control-plane
// retention and the connection freshness targets. The owning packages take
// their values from here, so a change to one limit is a reviewed change to the
// contract rather than a drifting local default.
//
// The package imports only the standard library so the control plane, the
// Guard daemon, its journal and its privileged executor can all depend on it.
// Mixed-version admission (what a peer that does not speak these versions may
// still do) is enforced in guardbinding.AdmitMutatingDispatch.
package guardcompat

import "time"

// Contract versions. A change to any of them is a wire change: add a new
// version next to the old one, never edit one in place.
const (
	// BindingContractVersion names the command binding and outcome-report
	// contract carried in every bound offer.
	BindingContractVersion = "guard.command-binding/v1"
	// BindingCapability is advertised by a Guard that validates bindings,
	// journals intent and result and reports revisions. Only such a Guard is
	// sent mutating work.
	BindingCapability = "guard.command-binding.v1"
	// LegacyContractVersion is stored for a read-only command dispatched to a
	// Guard that does not advertise BindingCapability.
	LegacyContractVersion = "legacy"
	// EnrollCapabilityVersion is printed by `techstack agent enroll --check`;
	// the installer uses it to tell a Guard that can redeem a host-key
	// enrollment from one that cannot.
	EnrollCapabilityVersion = "kombify-guard-enroll/1"
	// JournalSchemaVersion is the highest on-disk journal schema this contract
	// admits. A Guard denies new effects on a newer (or unreadable) schema
	// instead of rewriting it.
	JournalSchemaVersion = 1
)

// Enrollment and credential lifetimes.
const (
	// BootstrapCapabilityTTL bounds how long a provider-issued first-boot
	// capability may wait to be redeemed (image pulls and provider queues can
	// delay a first boot; an abandoned provisioning attempt must not enroll a
	// host days later).
	BootstrapCapabilityTTL = 72 * time.Hour
	// BootstrapRedemptionWindow bounds how long after its first issue the same
	// capability and attempt may replace an unconfirmed credential. The
	// capability's own expiry may close it sooner.
	BootstrapRedemptionWindow = 30 * time.Minute
	// KeyRotationOverlap bounds how long the predecessor host key stays valid
	// after a proven rotation; it only covers requests already in flight. An
	// emergency rotation uses no overlap.
	KeyRotationOverlap = 2 * time.Minute
	// LegacyKeyUpgradeWindow is how long a pending key bind of a bearer-only
	// worker waits for owner approval before it becomes replaceable.
	LegacyKeyUpgradeWindow = 24 * time.Hour
)

// Command admission and deduplication.
const (
	// CommandAuthorityLifetime is how long a typed command stays admissible
	// from its enqueue. The Guard checks it before the effect starts, so it is
	// also the longest a command can be started without the Guard having
	// observed a revocation that happened after dispatch.
	CommandAuthorityLifetime = 15 * time.Minute
	// LegacyAuthorityLifetime is the execution window a Guard grants an
	// offer from a control plane that predates the binding contract.
	LegacyAuthorityLifetime = 10 * time.Minute
	// ResultClockMargin is the skew tolerated between the effect start a Guard
	// reports and the control-plane authority expiry.
	ResultClockMargin = 5 * time.Minute
	// JournalClockMargin extends the Guard's deduplication horizon beyond the
	// authority expiry of a command.
	JournalClockMargin = 15 * time.Minute
	// DeduplicationHorizon is the shortest time a Guard keeps a command ID
	// (entry or tombstone) after dispatch: an expired re-offer is always
	// denied, so the ID must outlive every admissible offer of it.
	DeduplicationHorizon = CommandAuthorityLifetime + JournalClockMargin
	// MinTombstoneAfterAck is the shortest time a deduplication tombstone
	// outlives the acknowledgement of its command.
	MinTombstoneAfterAck = time.Hour
	// SettledCommandRetention is how long the control plane keeps a settled
	// (completed or failed) command row after its authority expired, so a late
	// identical report is acknowledged instead of mistaken for a new one.
	SettledCommandRetention = 24 * time.Hour
	// ReofferAfter throttles re-offering a dispatched command that the Guard
	// did not list as held in its journal.
	ReofferAfter = 10 * time.Second
	// MaxKnownCommands bounds the in-flight command IDs a Guard lists in one
	// poll.
	MaxKnownCommands = 64
	// MaxReasonBytes bounds every free-text reason carried on the wire.
	MaxReasonBytes = 512
	// MaxResultRevision bounds the revision number of one command's outcome.
	MaxResultRevision = 1_000
)

// Journal storage limits (Guard side). Unacknowledged and uncertain work is
// never pruned to stay inside them; a journal that cannot reserve space denies
// new effects instead.
const (
	// JournalBudgetBytes is the disk budget of one Guard's journal log.
	JournalBudgetBytes int64 = 256 << 20
	// MaxResultBytes bounds one persisted result; a slot of this size is
	// reserved before a command is admitted (oversized output is truncated
	// into a bounded receipt).
	MaxResultBytes int64 = 24 << 20
	// MaxIntentBytes bounds one persisted intent record (the command payload
	// and its binding).
	MaxIntentBytes int64 = 64 << 10
	// CompactAfterBytes triggers journal compaction.
	CompactAfterBytes int64 = 8 << 20
)

// Connection freshness (engineering targets from the Guard target §7, not
// availability SLAs).
const (
	HeartbeatInterval = 30 * time.Second
	StaleAfter        = 90 * time.Second
	OfflineAfter      = 5 * time.Minute
	ReconnectTarget   = 60 * time.Second
)

// Compile-time invariants of the contract: each expression underflows an
// unsigned constant, and so fails the build, when its relation breaks.
const (
	// A redemption retry fits inside the capability lifetime.
	_ = uint64(BootstrapCapabilityTTL - BootstrapRedemptionWindow)
	// A legacy offer never outlives a bound one.
	_ = uint64(CommandAuthorityLifetime - LegacyAuthorityLifetime)
	// The Guard's dedup margin covers the result clock skew.
	_ = uint64(JournalClockMargin - ResultClockMargin)
	// A dispatched command's ID outlives every admissible offer of it.
	_ = uint64(DeduplicationHorizon - CommandAuthorityLifetime - ResultClockMargin)
	// The control plane remembers a settled command at least as long as a
	// Guard may still present it.
	_ = uint64(SettledCommandRetention - DeduplicationHorizon)
	// A rotation overlap ends well before a command admitted under the old key
	// could have expired, so the predecessor key never extends authority.
	_ = uint64(CommandAuthorityLifetime - KeyRotationOverlap)
	// One admitted command (intent plus its reserved result slot) fits the
	// journal budget with room to compact.
	_ = uint64(JournalBudgetBytes - MaxResultBytes - MaxIntentBytes - CompactAfterBytes)
	// At least two heartbeats are missed before a worker reads stale, and
	// stale precedes offline.
	_ = uint64(StaleAfter - 2*HeartbeatInterval)
	_ = uint64(OfflineAfter - StaleAfter)
)
