// Package providercatalog defines the stable provider identity accepted by
// fresh managed-runtime product writes.
//
// The package is authoritative only for provider identity syntax and agreement.
// It is not a provider catalog, adapter registry, execution-profile resolver,
// credential authority, or historical inventory translator. It performs no I/O,
// persists no state, owns no concurrency, and must never receive secrets.
package providercatalog
