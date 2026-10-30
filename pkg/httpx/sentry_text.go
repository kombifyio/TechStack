package httpx

import (
	"fmt"
	"strings"
)

const sentryErrorDetailsMaxBytes = 16 * 1024

func truncateSentryDetails(value string) string {
	if len(value) <= sentryErrorDetailsMaxBytes {
		return value
	}
	omitted := len(value) - sentryErrorDetailsMaxBytes
	return strings.TrimRight(value[:sentryErrorDetailsMaxBytes], "\x00\r\n\t ") +
		fmt.Sprintf("... [truncated %d bytes]", omitted)
}
