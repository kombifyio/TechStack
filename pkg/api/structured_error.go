package api

import "net/http"

// UserGuidance is the actionable owner-facing part of a structured error:
// what happened and what the owner can do next.
type UserGuidance struct {
	Title     string
	Body      string
	NextSteps []string
}

// StructuredError is the one fail-closed error envelope every denial and
// unavailability surface emits, so clients parse exactly one shape:
// error_code, reason_code, required_features, missing_features, retryable,
// user_guidance{title, body, next_steps}, plus phase/phase_label when the
// failure belongs to a named flow phase.
type StructuredError struct {
	Status           int
	Message          string
	ErrorCode        string
	ReasonCode       string
	Phase            string
	PhaseLabel       string
	RequiredFeatures []string
	MissingFeatures  []string
	Retryable        bool
	Guidance         UserGuidance
}

// Code maps the HTTP status to the standardized API error code.
func (s StructuredError) Code() ErrorCode {
	switch s.Status {
	case http.StatusBadRequest:
		return ErrCodeBadRequest
	case http.StatusUnauthorized:
		return ErrCodeUnauthorized
	case http.StatusForbidden:
		return ErrCodeForbidden
	case http.StatusNotFound:
		return ErrCodeNotFound
	case http.StatusConflict:
		return ErrCodeConflict
	case http.StatusTooManyRequests:
		return ErrCodeRateLimited
	case http.StatusServiceUnavailable:
		return ErrCodeUnavailable
	}
	if s.Status >= http.StatusInternalServerError {
		return ErrCodeInternal
	}
	return ErrCodeBadRequest
}

// Details builds the canonical details map carried in ErrorDetail.Details.
func (s StructuredError) Details() map[string]any {
	nextSteps := s.Guidance.NextSteps
	if nextSteps == nil {
		nextSteps = []string{}
	}
	details := map[string]any{
		"error_code":        s.ErrorCode,
		"reason_code":       s.ReasonCode,
		"required_features": nonNilStrings(s.RequiredFeatures),
		"missing_features":  nonNilStrings(s.MissingFeatures),
		"retryable":         s.Retryable,
		"user_guidance": map[string]any{
			"title":      s.Guidance.Title,
			"body":       s.Guidance.Body,
			"next_steps": nextSteps,
		},
	}
	if s.Phase != "" {
		details["phase"] = s.Phase
		details["phase_label"] = s.PhaseLabel
	}
	return details
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
