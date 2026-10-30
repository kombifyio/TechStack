package jobs

import "fmt"

// jobPanicError carries a panic recovered from a job handler to the finisher of
// withJobSentryScope. recover() works only in the deferred function itself, so the
// caller recovers and passes the panic on as this error value.
type jobPanicError struct {
	value any
	stack []byte
}

func (p *jobPanicError) Error() string { return fmt.Sprintf("job panic: %v", p.value) }
