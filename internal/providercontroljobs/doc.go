// Package providercontroljobs adapts TechStack job lifecycle commands to the
// native provider-control application boundary.
//
// # Authority
//
// The package owns no lifecycle or provider state. Lease Authority creates the
// resource generation, providercontrol owns operation custody, and the server
// registry owns runtime identity. This adapter only maps job requests.
//
// # Side effects and activation
//
// Every mutating entry point crosses MutationActivationGate before admission.
// Production currently injects ProductionBlockedGate, so this package cannot
// persist an operation or reach an adapter until the external activation
// controls are completed. Replaying an admission request may return the same
// durable operation, but it is never a provider-dispatch retry. This package
// exposes no dispatch-guard reset, execute-permit minting, or ambiguous-create
// retry surface. Crash-recoverable and at-most-once behavior are
// operation-specific paths under the same TechStack authority, not separate
// admission lanes.
//
// # Secrets
//
// Job metadata is reduced to an explicit public allowlist. Credentials,
// access material, and arbitrary metadata never enter leases or desired specs.
package providercontroljobs
