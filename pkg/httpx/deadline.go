package httpx

import (
	"net/http"
	"time"
)

// ExtendWriteDeadline pushes the response write deadline forward by budget.
//
// Handlers whose legitimate work exceeds the server-wide WriteTimeout call it
// before that work starts: the wizard-run projection can spend minutes in the
// pinned-CLI validator, an SSE progress stream must outlive the default write
// budget, and a large artifact transfer needs a bounded extension. The budget
// is caller-owned and must stay finite; writers without deadline support
// (httptest.ResponseRecorder) are left unchanged and the returned error is
// advisory.
func ExtendWriteDeadline(w http.ResponseWriter, budget time.Duration) error {
	if w == nil || budget <= 0 {
		return nil
	}
	return http.NewResponseController(w).SetWriteDeadline(time.Now().Add(budget))
}
