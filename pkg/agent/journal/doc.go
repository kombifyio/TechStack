// Package journal is the crash-safe, bounded store in which kombify Guard
// records what it was asked to do and what happened, so a restart, a lost
// connection or a lost acknowledgement can never turn into a second effect.
//
// Store choice. The journal is an append-only, checksummed record log that is
// fsynced before any record counts as durable, plus an atomic
// write-temp/fsync/rename compaction. A write-ahead SQL engine was the other
// candidate; it would add an engine and a C-compatible runtime to the Guard
// binary for a workload of a few records per command. The log gives the same
// guarantee with one file: a record is either fully present with a valid
// checksum or it is a torn tail that was never acknowledged to any caller and
// is discarded on open. Corruption anywhere before the tail is never repaired
// silently: the journal refuses new writes and keeps the evidence.
//
// Durable states of one command, in order:
//
//	intent        the admitted command identity and binding, before any effect
//	dispatched    the marker written before the executor is invoked
//	result        an immutable report (terminal, outcome_unknown or rejected)
//	acked         the control plane committed that report
//	tombstone     compacted; kept only to reject replay until authority expires
//
// An interruption after the dispatched marker and before a result is an
// outcome_unknown: the Guard reconciles it against observed state, it never
// runs the command again. outcome_unknown is non-terminal; a later revision
// resolves it and references the evidence it supersedes.
package journal
