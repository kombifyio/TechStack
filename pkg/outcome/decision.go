// Package outcome owns the structured, user-facing result contract shared by
// lifecycle APIs, jobs, and durable resource projections.
package outcome

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Status is the bounded availability state rendered by operator surfaces.
type Status string

const (
	StatusAvailable Status = "available"
	StatusDisabled  Status = "disabled"
	StatusBlocked   Status = "blocked"
	StatusPending   Status = "pending"
	StatusDegraded  Status = "degraded"
	StatusFailed    Status = "failed"
)

// Step is one concrete action offered to the operator.
type Step struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Kind        string `json:"kind"`
	Href        string `json:"href,omitempty"`
	Description string `json:"description,omitempty"`
}

// Guidance is the human-readable explanation and next action for a decision.
type Guidance struct {
	Title     string `json:"title"`
	Body      string `json:"body"`
	NextSteps []Step `json:"next_steps"`
}

// Decision is the canonical backend counterpart of the ServerOutcome frontend
// model. It deliberately carries only secret-free support context.
type Decision struct {
	Status           Status   `json:"status"`
	ErrorCode        string   `json:"error_code,omitempty"`
	ReasonCode       string   `json:"reason_code,omitempty"`
	Capability       string   `json:"capability,omitempty"`
	ProviderID       string   `json:"provider_id,omitempty"`
	RequiredFeatures []string `json:"required_features,omitempty"`
	MissingFeatures  []string `json:"missing_features,omitempty"`
	Retryable        bool     `json:"retryable"`
	// RetryAfter is the earliest time an external authority (for example an
	// ACME certificate authority rate limit) admits the next attempt. A retry
	// before it cannot succeed. It is only valid on a retryable outcome.
	RetryAfter          *time.Time     `json:"retry_after,omitempty"`
	UserGuidance        *Guidance      `json:"user_guidance,omitempty"`
	Remediation         string         `json:"remediation,omitempty"`
	ProviderDiagnostics map[string]any `json:"provider_diagnostics,omitempty"`
	OccurredAt          time.Time      `json:"occurred_at"`
	RequestID           string         `json:"request_id,omitempty"`
	SupportContext      map[string]any `json:"support_context,omitempty"`
}

// Normalize trims, clones, timestamps, and validates one decision before it is
// returned or persisted. Actionable states fail closed without guidance.
func Normalize(value Decision, occurredAt time.Time) (Decision, error) {
	value.Status = Status(strings.ToLower(strings.TrimSpace(string(value.Status))))
	value.ErrorCode = strings.TrimSpace(value.ErrorCode)
	value.ReasonCode = strings.TrimSpace(value.ReasonCode)
	value.Capability = strings.TrimSpace(value.Capability)
	value.ProviderID = strings.TrimSpace(value.ProviderID)
	value.Remediation = strings.TrimSpace(value.Remediation)
	value.RequestID = strings.TrimSpace(value.RequestID)
	value.RequiredFeatures = compactStrings(value.RequiredFeatures)
	value.MissingFeatures = compactStrings(value.MissingFeatures)
	value.ProviderDiagnostics = cloneMap(value.ProviderDiagnostics)
	value.SupportContext = cloneMap(value.SupportContext)
	value.UserGuidance = cloneGuidance(value.UserGuidance)
	if value.RetryAfter != nil {
		retryAfter := value.RetryAfter.UTC()
		value.RetryAfter = &retryAfter
	}
	if value.OccurredAt.IsZero() {
		value.OccurredAt = occurredAt.UTC()
	} else {
		value.OccurredAt = value.OccurredAt.UTC()
	}
	if err := Validate(value); err != nil {
		return Decision{}, err
	}
	return value, nil
}

// Validate enforces the stable public contract and a bounded, secret-free
// payload. Callers may wrap the error; they must not persist an invalid value.
func Validate(value Decision) error {
	if err := validateStatus(value.Status); err != nil {
		return err
	}
	if value.OccurredAt.IsZero() {
		return errors.New("outcome occurred_at is required")
	}
	if err := validateBoundedFields(value); err != nil {
		return err
	}
	if err := validateActionable(value); err != nil {
		return err
	}
	if value.RetryAfter != nil && (value.RetryAfter.IsZero() || !value.Retryable) {
		return errors.New("outcome retry_after requires a retryable outcome with a concrete time")
	}
	if err := validateGuidance(value.UserGuidance); err != nil {
		return err
	}
	if err := validateFeatureLists(value.RequiredFeatures, value.MissingFeatures); err != nil {
		return err
	}
	if err := validateSecretFree(value.ProviderDiagnostics, "provider_diagnostics"); err != nil {
		return err
	}
	if err := validateSecretFree(value.SupportContext, "support_context"); err != nil {
		return err
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode outcome: %w", err)
	}
	if len(encoded) > 64*1024 {
		return errors.New("outcome exceeds its bounded size")
	}
	return nil
}

func validateStatus(status Status) error {
	switch status {
	case StatusAvailable, StatusDisabled, StatusBlocked, StatusPending, StatusDegraded, StatusFailed:
		return nil
	default:
		return errors.New("outcome status is not supported")
	}
}

func validateBoundedFields(value Decision) error {
	for name, field := range map[string]string{
		"error_code": value.ErrorCode, "reason_code": value.ReasonCode,
		"capability": value.Capability, "provider_id": value.ProviderID,
		"request_id": value.RequestID, "remediation": value.Remediation,
	} {
		limit := 256
		if name == "remediation" {
			limit = 4096
		}
		if len(field) > limit {
			return fmt.Errorf("outcome %s exceeds its bounded length", name)
		}
	}
	return nil
}

func validateActionable(value Decision) error {
	if value.Status != StatusAvailable {
		if value.ErrorCode == "" && value.ReasonCode == "" {
			return errors.New("actionable outcome requires a stable error or reason code")
		}
		if value.UserGuidance == nil || value.UserGuidance.Title == "" || value.UserGuidance.Body == "" || len(value.UserGuidance.NextSteps) == 0 {
			return errors.New("actionable outcome requires title, body, and next steps")
		}
	}
	return nil
}

func validateGuidance(guidance *Guidance) error {
	if guidance != nil {
		if len(guidance.Title) > 256 || len(guidance.Body) > 4096 || len(guidance.NextSteps) > 16 {
			return errors.New("outcome guidance exceeds its bounded size")
		}
		for _, step := range guidance.NextSteps {
			if strings.TrimSpace(step.ID) == "" || strings.TrimSpace(step.Label) == "" || strings.TrimSpace(step.Kind) == "" ||
				len(step.ID) > 128 || len(step.Label) > 512 || len(step.Kind) > 64 || len(step.Href) > 2048 || len(step.Description) > 1024 {
				return errors.New("outcome guidance step is incomplete or exceeds its bounded size")
			}
			switch step.Kind {
			case "note", "link", "external", "retry", "connect", "handoff", "open-oneliner":
			default:
				return errors.New("outcome guidance step kind is not supported")
			}
		}
	}
	return nil
}

func validateFeatureLists(required, missing []string) error {
	if len(required) > 32 || len(missing) > 32 {
		return errors.New("outcome feature list exceeds its bounded size")
	}
	for _, feature := range append(append([]string(nil), required...), missing...) {
		if len(feature) > 256 {
			return errors.New("outcome feature identifier exceeds its bounded length")
		}
	}
	return nil
}

// Clone returns a deep copy suitable for crossing store boundaries.
func Clone(value *Decision) *Decision {
	if value == nil {
		return nil
	}
	copy := *value
	copy.RequiredFeatures = append([]string(nil), value.RequiredFeatures...)
	copy.MissingFeatures = append([]string(nil), value.MissingFeatures...)
	copy.ProviderDiagnostics = cloneMap(value.ProviderDiagnostics)
	copy.SupportContext = cloneMap(value.SupportContext)
	copy.UserGuidance = cloneGuidance(value.UserGuidance)
	if value.RetryAfter != nil {
		retryAfter := *value.RetryAfter
		copy.RetryAfter = &retryAfter
	}
	return &copy
}

func compactStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

func cloneGuidance(value *Guidance) *Guidance {
	if value == nil {
		return nil
	}
	copy := *value
	copy.Title = strings.TrimSpace(copy.Title)
	copy.Body = strings.TrimSpace(copy.Body)
	copy.NextSteps = append([]Step(nil), value.NextSteps...)
	for i := range copy.NextSteps {
		copy.NextSteps[i].ID = strings.TrimSpace(copy.NextSteps[i].ID)
		copy.NextSteps[i].Label = strings.TrimSpace(copy.NextSteps[i].Label)
		copy.NextSteps[i].Kind = strings.TrimSpace(copy.NextSteps[i].Kind)
		copy.NextSteps[i].Href = strings.TrimSpace(copy.NextSteps[i].Href)
		copy.NextSteps[i].Description = strings.TrimSpace(copy.NextSteps[i].Description)
	}
	return &copy
}

func cloneMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return nil
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		return value
	}
	var copy map[string]any
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return value
	}
	return copy
}

func validateSecretFree(value any, path string) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("outcome %s is not JSON: %w", path, err)
	}
	var normalized any
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		return fmt.Errorf("outcome %s is not JSON: %w", path, err)
	}
	return walkSecretFree(normalized, path)
}

func walkSecretFree(value any, path string) error {
	switch typed := value.(type) {
	case map[string]any:
		for key, nested := range typed {
			normalized := strings.ToLower(strings.TrimSpace(key))
			for _, forbidden := range []string{
				"password", "passwd", "secret", "token", "credential", "private_key",
				"authorization", "api_key", "apikey", "access_key",
			} {
				if strings.Contains(normalized, forbidden) {
					return fmt.Errorf("outcome %s.%s is not secret-free", path, key)
				}
			}
			if err := walkSecretFree(nested, path+"."+key); err != nil {
				return err
			}
		}
	case []any:
		for index, nested := range typed {
			if err := walkSecretFree(nested, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case string:
		lower := strings.ToLower(strings.TrimSpace(typed))
		if strings.Contains(lower, "-----begin ") && strings.Contains(lower, "private key-----") {
			return fmt.Errorf("outcome %s contains private key material", path)
		}
	}
	return nil
}
