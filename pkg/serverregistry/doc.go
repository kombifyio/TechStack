// Package serverregistry defines Techstack's tenant-scoped server aggregate.
//
// # Authority
//
// The registry is authoritative for durable server identity, lifecycle
// transitions, ownership bindings, accepted desired-state projection, and
// secret-free inventory projections.
//
// # Non-authority
//
// It is not lease, provider-control, billing, access-policy, or desired-state
// decision authority.
// Guard events own only connection, health, heartbeat, service, and inventory
// dimensions and cannot resurrect or mutate a decommissioned aggregate.
//
// # Side effects
//
// The aggregate vocabulary itself performs no external I/O; repository
// implementations apply accepted commands and emit secret-free outbox state.
//
// # Persistence
//
// Production persistence applies an event, aggregate head, transition,
// inventory snapshot, and outbox record atomically in PostgreSQL.
//
// # Concurrency
//
// Expected revisions plus generation, retained source-epoch history, and a
// monotone source sequence fence concurrent and stale events. Projection work
// uses random token, owner, generation, heartbeat, expiry, and DB-time-bound
// claims; an expired claim cannot complete.
//
// # Secrets
//
// Reads remain tenant- and access-scoped. Registry records and outbox payloads
// never contain credentials, SSH private keys, passwords, or bearer tokens.
package serverregistry
