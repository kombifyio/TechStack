package httpx

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
)

// ConditionalSuccess writes the success envelope of an authenticated GET read
// with a strong validator, and answers 304 with no body when the caller already
// holds that exact state.
//
// The ETag is the quoted sha256 of the JSON bytes of the payload. It is not the
// hash of the whole envelope: meta carries a per-request id and timestamp that
// differ between identical states and would make every validator unique. A
// payload field that is only a function of the clock (an age counter derived
// from a timestamp the payload also carries) would defeat revalidation the same
// way, so the caller passes the payload with those fields cleared as validator;
// nil validates the payload itself. A state change always changes the ETag,
// because every field that is not a pure elapsed-time counter stays hashed.
//
// Call it only after authentication and tenant scoping succeeded and only for a
// successful read: an error response never carries a validator, and the
// response is private because it depends on the caller's Authorization.
func ConditionalSuccess(e *Event, payload, validator any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return InternalError(e, "Failed to encode response")
	}
	validated := body
	if validator != nil {
		if validated, err = json.Marshal(validator); err != nil {
			return InternalError(e, "Failed to encode response")
		}
	}
	sum := sha256.Sum256(validated)
	etag := `"` + hex.EncodeToString(sum[:]) + `"`

	header := e.Response.Header()
	header.Set("ETag", etag)
	header.Set("Cache-Control", "private, no-cache")
	header.Add("Vary", "Authorization")
	if ifNoneMatchHits(e.Request.Header.Values("If-None-Match"), etag) {
		e.Response.WriteHeader(http.StatusNotModified)
		e.MarkResponseStreamed()
		return nil
	}
	return Success(e, http.StatusOK, json.RawMessage(body))
}

// ifNoneMatchHits reports whether an If-None-Match field (every header line,
// each a comma-separated list, `*` or `W/` tags) matches etag. GET and HEAD use
// the weak comparison, so a `W/` prefix on either side is ignored.
func ifNoneMatchHits(headerValues []string, etag string) bool {
	want := strings.TrimPrefix(strings.TrimSpace(etag), "W/")
	for _, line := range headerValues {
		for _, candidate := range strings.Split(line, ",") {
			candidate = strings.TrimSpace(candidate)
			if candidate == "*" || (candidate != "" && strings.TrimPrefix(candidate, "W/") == want) {
				return true
			}
		}
	}
	return false
}
