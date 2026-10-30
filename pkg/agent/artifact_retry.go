package agent

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ArtifactHTTPError preserves the origin's bounded retry instruction without
// persisting a request URL, bearer credential or response body.
type ArtifactHTTPError struct {
	Operation  string
	StatusCode int
	RetryAfter time.Duration
}

func (e *ArtifactHTTPError) Error() string {
	return fmt.Sprintf("%s returned HTTP %d", e.Operation, e.StatusCode)
}

// ArtifactRetryAfter accepts the HTTP seconds/date forms, with a finite one-day
// maximum. An absent, invalid or elapsed instruction does not invent a delay.
func ArtifactRetryAfter(value string, now time.Time) time.Duration {
	value = strings.TrimSpace(value)
	if seconds, err := strconv.ParseInt(value, 10, 64); err == nil {
		if seconds <= 0 {
			return 0
		}
		if seconds > 86400 {
			return 24 * time.Hour
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(value); err == nil {
		return max(0, min(date.Sub(now), 24*time.Hour))
	}
	return 0
}
