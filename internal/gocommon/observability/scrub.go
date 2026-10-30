package observability

// Scrubbing shared by the Sentry wiring (observability.go) and the logger
// (logging.go). It has no Sentry dependency, so it is also part of the Full
// Local build (build tag kombify_full_local).

import (
	"net/url"
	"strings"
)

func scrubURL(value string) string {
	parsed, err := url.Parse(value)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return value
	}
	parsed.User = nil
	parsed.RawQuery = ""
	parsed.ForceQuery = false
	parsed.Fragment = ""
	return parsed.String()
}

func sensitiveMetadataKey(key string) bool {
	name := strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(key))
	for _, sensitive := range []string{"authorization", "cookie", "password", "secret", "token", "signature", "query", "body", "email", "ipaddress", "requestdata", "env", "xkombify", "xuser", "xorg"} {
		if strings.Contains(name, sensitive) {
			return true
		}
	}
	return false
}
