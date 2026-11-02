package jobs

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// StackKitReasonACMERateLimited is the StackKits outcome class for a public
// certificate the ACME certificate authority refused under its rate limit.
// A new server for the same address requests the same certificate, so neither
// a retry nor a replacement can succeed before the authority's retry-after.
const StackKitReasonACMERateLimited = "acme_rate_limited"

// StackKitReasonUpgradeKitUnsupported is the StackKits refusal of a fresh
// upgrade for a kit whose CUE upgrade policy declares support "unsupported".
// StackKits refuses before it resolves a release or changes the node.
const StackKitReasonUpgradeKitUnsupported = "upgrade_kit_unsupported"

// StackKitLifecycleFailedError is a lifecycle command that the pinned
// StackKits ended with its typed actionable error. The job keeps that reason
// instead of the generic error category.
type StackKitLifecycleFailedError struct {
	Operation string
	Failure   typedStackKitFailure
}

func (e *StackKitLifecycleFailedError) Error() string {
	if e.Failure.ReasonCode == StackKitReasonUpgradeKitUnsupported {
		return "StackKits refused the upgrade: the StackKit's upgrade policy does not support an in-place upgrade (" +
			StackKitReasonUpgradeKitUnsupported + ")"
	}
	return fmt.Sprintf("StackKits %s stopped: %s", e.Operation, e.Failure.ReasonCode)
}

// typedStackKitFailure is the closed recovery fact a pinned StackKits command
// reported in its actionable-error envelope. It never carries message text.
type typedStackKitFailure struct {
	ReasonCode string
	Retryable  bool
	RetryAfter time.Time
}

var stackKitReasonCodePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_]{0,63}$`)

// typedStackKitFailureFromResult reads the stackkit.actionable-error/v1 detail
// of a failed command result. An absent, foreign, or malformed detail yields
// the zero value: the orchestrator then keeps its generic operation reason.
func typedStackKitFailureFromResult(result *agentpb.StackKitResult) typedStackKitFailure {
	if result == nil || result.Success || len(result.CommandResultJson) == 0 {
		return typedStackKitFailure{}
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			SchemaVersion string `json:"schemaVersion"`
			ReasonCode    string `json:"reasonCode"`
			Retryable     bool   `json:"retryable"`
			RetryAfter    string `json:"retryAfter"`
		} `json:"data"`
	}
	if json.Unmarshal(result.CommandResultJson, &envelope) != nil || envelope.Status != "failed" ||
		envelope.Data.SchemaVersion != "stackkit.actionable-error/v1" ||
		!stackKitReasonCodePattern.MatchString(envelope.Data.ReasonCode) {
		return typedStackKitFailure{}
	}
	failure := typedStackKitFailure{ReasonCode: envelope.Data.ReasonCode, Retryable: envelope.Data.Retryable}
	if raw := strings.TrimSpace(envelope.Data.RetryAfter); raw != "" && failure.Retryable {
		if parsed, err := time.Parse(time.RFC3339, raw); err == nil {
			failure.RetryAfter = parsed.UTC()
		}
	}
	return failure
}

// JobResultRetryAfter returns the authority-named retry time a failed job
// recorded in its outcome envelope or typed StackKits rollout failure, if any.
func JobResultRetryAfter(result map[string]interface{}) (time.Time, bool) {
	if retryAfter, found := retryAfterValue(result["retry_after"]); found {
		return retryAfter, true
	}
	rollout, _ := result["typed_stackkit_rollout"].(map[string]interface{})
	return retryAfterValue(rollout["retry_after"])
}

func retryAfterValue(candidate interface{}) (time.Time, bool) {
	raw := ""
	switch value := candidate.(type) {
	case string:
		raw = value
	case time.Time:
		return value.UTC(), !value.IsZero()
	case *time.Time:
		if value == nil || value.IsZero() {
			return time.Time{}, false
		}
		return value.UTC(), true
	}
	parsed, err := time.Parse(time.RFC3339, strings.TrimSpace(raw))
	if err != nil || parsed.IsZero() {
		return time.Time{}, false
	}
	return parsed.UTC(), true
}
