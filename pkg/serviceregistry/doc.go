// Package serviceregistry defines Techstack's tenant-scoped service state
// vocabulary.
//
// # Authority
//
// The vocabulary is authoritative for the three independent service
// dimensions: user-owned desired state, measured observed state, and measured
// health state. It pins them to the StackKits runtime-observation contract so
// the control plane, the agent, and the database CHECK constraints agree on
// one set of legal values.
//
// # Non-authority
//
// It is not lease, provider-control, migration-workflow, or access-policy
// authority. The legacy services.status column additionally carries
// control-plane workflow states; this package only derives the observed
// projection of that column and never decides workflow state.
//
// # Side effects
//
// The vocabulary performs no I/O. Repository implementations apply accepted
// commands.
//
// # Persistence
//
// Persistence is owned by pkg/controlplane: one accepted service event commits
// the aggregate head and its change-only transition rows in one transaction.
//
// # Secrets
//
// State values are enumerations and never carry credentials.
package serviceregistry
