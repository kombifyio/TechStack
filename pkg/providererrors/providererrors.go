// Package providererrors extracts stable, supportable signals from provider
// control-plane errors without making the providers part of TechStack state.
package providererrors

import (
	"encoding/json"
	"regexp"
	"strings"
)

const (
	CategoryProviderAuth     = "provider_auth"
	CategoryProviderConflict = "provider_conflict"
	CategoryProviderQuota    = "provider_quota"
	CategoryProviderThrottle = "provider_throttle"
	CategoryProviderError    = "provider_error"

	RetryHintContactSupport = "contact_provider_support"
	RetryHintFreeResources  = "free_provider_resources"
	RetryHintRetryLater     = "retry_after_provider_cooldown"
)

var (
	providerCodePattern = regexp.MustCompile(`\b[A-Z][A-Z0-9]+-\d+(?:-\d+)+\b`)
	providerPattern     = regexp.MustCompile(`(?i)\b(ionos|centron)-managed\b`)
)

type Info struct {
	Provider  string
	Code      string
	Category  string
	Summary   string
	RetryHint string
	Terminal  bool
}

func (i Info) HasSignal() bool {
	return i.Provider != "" || i.Code != "" || i.Category != "" || i.RetryHint != ""
}

func Classify(err error) Info {
	if err == nil {
		return Info{}
	}
	return ClassifyMessage(err.Error())
}

func ClassifyMessage(message string) Info {
	message = strings.TrimSpace(message)
	if message == "" {
		return Info{}
	}
	summary := providerSummary(message)
	lower := strings.ToLower(message + "\n" + summary)
	info := Info{
		Provider: providerFromMessage(message),
		Code:     providerCodePattern.FindString(message),
		Summary:  summary,
	}
	if info.Code == "" {
		info.Code = providerCodePattern.FindString(summary)
	}

	switch {
	case containsAny(lower,
		"vdc-5-1091",
		"too many recent create and delete operations",
		"too many recent create/delete operations",
		"rate limit",
		"rate-limit",
		"ratelimit",
		"throttl",
	):
		info.Category = CategoryProviderThrottle
		info.RetryHint = RetryHintRetryLater
		info.Terminal = true
	case containsAny(lower,
		"vdc-5-1051",
		"would be exhausted",
		"personal limit",
		"quota exceeded",
		"quota exhausted",
		"insufficient quota",
	):
		info.Category = CategoryProviderQuota
		info.RetryHint = RetryHintFreeResources
		info.Terminal = true
	case containsAny(lower,
		"unauthorized",
		"forbidden",
		"invalid credential",
		"invalid api key",
		"authentication failed",
	):
		info.Category = CategoryProviderAuth
		info.RetryHint = RetryHintContactSupport
		info.Terminal = true
	case containsAny(lower,
		"service conflict",
		"already exists",
		"name is already in use",
		"resource conflict",
	):
		info.Category = CategoryProviderConflict
		info.RetryHint = RetryHintContactSupport
		info.Terminal = true
	case info.Provider != "" || info.Code != "":
		info.Category = CategoryProviderError
	}

	if info.RetryHint == "" && strings.Contains(lower, "contact support") {
		info.RetryHint = RetryHintContactSupport
	}
	return info
}

func IsTerminal(err error) bool {
	return Classify(err).Terminal
}

func providerSummary(message string) string {
	if nested := nestedProviderMessage(message); nested != "" {
		message = nested
	}
	message = stripKnownPrefixes(message)
	return strings.TrimSpace(message)
}

func nestedProviderMessage(message string) string {
	start := strings.Index(message, "{")
	end := strings.LastIndex(message, "}")
	if start < 0 || end <= start {
		return ""
	}
	var payload struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal([]byte(message[start:end+1]), &payload); err != nil {
		return ""
	}
	if strings.TrimSpace(payload.Error.Message) != "" {
		return payload.Error.Message
	}
	return strings.TrimSpace(payload.Message)
}

func stripKnownPrefixes(message string) string {
	message = strings.TrimSpace(message)
	for {
		before := message
		for _, marker := range []string{
			"simulate enroll returned",
			"create ionos-managed node:",
			"create centron-managed node:",
			"ionos create server request failed:",
			"centron create server request failed:",
		} {
			message = stripPrefixCaseInsensitive(message, marker)
		}
		if message == before {
			return message
		}
	}
}

func stripPrefixCaseInsensitive(message, prefix string) string {
	trimmed := strings.TrimSpace(message)
	lower := strings.ToLower(trimmed)
	prefixLower := strings.ToLower(prefix)
	if !strings.HasPrefix(lower, prefixLower) {
		return trimmed
	}
	rest := strings.TrimSpace(trimmed[len(prefix):])
	if prefixLower == "simulate enroll returned" {
		if colon := strings.Index(rest, ":"); colon >= 0 {
			rest = strings.TrimSpace(rest[colon+1:])
		}
	}
	return rest
}

func providerFromMessage(message string) string {
	match := providerPattern.FindString(strings.ToLower(message))
	if match != "" {
		return match
	}
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "ionos"):
		return "ionos-managed"
	case strings.Contains(lower, "centron"):
		return "centron-managed"
	default:
		return ""
	}
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}
