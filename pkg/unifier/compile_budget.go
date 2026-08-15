package unifier

import "sync"

// compileBudget serialises CUE schema compilation across the whole process.
//
// Compiling the StackKit base plus one kit allocates roughly 440 MiB and peaks
// near 335 MiB of heap, of which almost nothing survives the next collection
// (measured: 1.3 MiB live after GC). That profile is fine on its own — GOMEMLIMIT
// can hold a transient spike by collecting harder — but only while one compile
// is in flight. The job queue runs four workers, and the provision handler
// re-enters from the top on every 15 s resume while a managed provider builds a
// VM, so several compiles overlap routinely. Four concurrent compiles make the
// heap genuinely live, GOMEMLIMIT can no longer collect its way under the
// ceiling, and the 512 MiB container is OOM-killed. That happened four times in
// production on 2026-07-26, each time orphaning the provision job mid-flight and
// freezing its step at save_config forever.
//
// One compile at a time keeps the live set small enough for the soft memory
// limit to do its job. Compilation is a few seconds, and it is not on any
// latency-critical path.
//
// Deliberately NOT solved by caching the compiled kit: holding the loader and
// its compiled value retains 277 MiB permanently (measured), which would convert
// a transient spike into a permanent baseline the container cannot afford.
var compileBudget sync.Mutex
