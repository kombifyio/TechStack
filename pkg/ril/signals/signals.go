// Package signals defines Techstack's stable RIL signal producer contract and
// durable delivery boundary. It deliberately has no Mastra or Gateway runtime
// dependency.
package signals

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

type Source string

const (
	SourceHealth    Source = "health"
	SourceDrift     Source = "drift"
	SourceBackup    Source = "backup"
	SourceCert      Source = "cert"
	SourceDeploy    Source = "deploy"
	SourceResource  Source = "resource"
	SourceConnector Source = "connector"
)

type Severity string

const (
	SeverityInfo     Severity = "info"
	SeverityLow      Severity = "low"
	SeverityMedium   Severity = "medium"
	SeverityHigh     Severity = "high"
	SeverityCritical Severity = "critical"
)

type Priority string

const (
	PriorityLow    Priority = "low"
	PriorityNormal Priority = "normal"
	PriorityHigh   Priority = "high"
	PriorityUrgent Priority = "urgent"
)

var (
	ErrInvalidSignal      = errors.New("ril signals: invalid signal")
	ErrServerUnauthorized = errors.New("ril signals: server is not authorized for tenant")
	ErrDedupeConflict     = errors.New("ril signals: dedupe key payload conflict")
	ErrOutboxEmpty        = errors.New("ril signals: no claimable outbox item")
	ErrClaimLost          = errors.New("ril signals: outbox claim lost")
	stableIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,191}$`)
	tenantIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:|@-]{0,255}$`)
	userIDPattern         = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:|@-]{0,255}$`)
	claimOwnerPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,191}$`)
)

// ConnectorRequirement is a redacted authority requirement, never a token or
// credential. Gateway remains authoritative for current grant state.
type ConnectorRequirement struct {
	ConnectorID      string   `json:"connectorId"`
	ConnectorGrantID string   `json:"connectorGrantId"`
	RequiredScopes   []string `json:"requiredScopes"`
	BindingScope     string   `json:"bindingScope"`
}

// Observation is the source-adapter input for all seven canonical signal
// families. Priority is derived centrally from Severity and cannot be chosen
// by a producer.
type Observation struct {
	SignalID          string
	DedupeKey         string
	TenantID          string
	UserID            string
	ServerID          string
	Source            Source
	Severity          Severity
	RecommendedAction string
	TraceID           string
	AuditID           string
	ReceivedAt        time.Time
	Connector         *ConnectorRequirement
}

// Envelope is the stable producer wire. Its JSON names match the public-beta
// RIL ingest contract while retaining tenant and trace correlation for Gateway.
type Envelope struct {
	SignalID          string   `json:"signalId"`
	ActionCardID      string   `json:"actionCardId"`
	TenantID          string   `json:"tenantId"`
	UserID            string   `json:"userId,omitempty"`
	ServerID          string   `json:"serverId"`
	Source            Source   `json:"source"`
	Severity          Severity `json:"severity"`
	Priority          Priority `json:"priority"`
	ReceivedAt        string   `json:"receivedAt"`
	RecommendedAction string   `json:"recommendedAction,omitempty"`
	TraceID           string   `json:"traceId"`
	AuditID           string   `json:"auditId"`
	ConnectorID       string   `json:"connectorId,omitempty"`
	ConnectorGrantID  string   `json:"connectorGrantId,omitempty"`
	RequiredScopes    []string `json:"requiredScopes,omitempty"`
	BindingScope      string   `json:"bindingScope,omitempty"`
}

func ClassifyPriority(severity Severity) (Priority, error) {
	switch severity {
	case SeverityInfo, SeverityLow:
		return PriorityLow, nil
	case SeverityMedium:
		return PriorityNormal, nil
	case SeverityHigh:
		return PriorityHigh, nil
	case SeverityCritical:
		return PriorityUrgent, nil
	default:
		return "", ErrInvalidSignal
	}
}

func normalizeObservation(input Observation) (Observation, Envelope, error) {
	input.TenantID = strings.TrimSpace(input.TenantID)
	input.ServerID = strings.TrimSpace(input.ServerID)
	input.UserID = strings.TrimSpace(input.UserID)
	input.DedupeKey = strings.TrimSpace(input.DedupeKey)
	input.TraceID = strings.TrimSpace(input.TraceID)
	input.AuditID = strings.TrimSpace(input.AuditID)
	input.RecommendedAction = strings.TrimSpace(input.RecommendedAction)
	if !validSource(input.Source) || !validTenantID(input.TenantID) || !stableID(input.ServerID) ||
		!stableID(input.DedupeKey) || !stableID(input.TraceID) || !stableID(input.AuditID) ||
		(input.UserID != "" && !userIDPattern.MatchString(input.UserID)) || len(input.RecommendedAction) > 1024 {
		return input, Envelope{}, ErrInvalidSignal
	}
	priority, err := ClassifyPriority(input.Severity)
	if err != nil {
		return input, Envelope{}, err
	}
	if input.ReceivedAt.IsZero() || input.ReceivedAt.Location() != time.UTC {
		return input, Envelope{}, fmt.Errorf("%w: UTC received time required", ErrInvalidSignal)
	}
	if input.SignalID == "" {
		digest := sha256.Sum256([]byte(strings.Join([]string{input.TenantID, input.ServerID, string(input.Source), input.DedupeKey}, "\x00")))
		input.SignalID = "ril-signal:" + hex.EncodeToString(digest[:16])
	}
	if !stableID(input.SignalID) {
		return input, Envelope{}, ErrInvalidSignal
	}
	actionCardID := "ril-action-card:" + input.SignalID
	connector, err := normalizeConnector(input.Connector)
	if err != nil {
		return input, Envelope{}, err
	}
	if input.Source == SourceConnector && connector == nil {
		return input, Envelope{}, fmt.Errorf("%w: connector source requires delegated grant context", ErrInvalidSignal)
	}
	input.Connector = connector
	envelope := Envelope{
		SignalID: input.SignalID, ActionCardID: actionCardID,
		TenantID: input.TenantID, UserID: input.UserID,
		ServerID: input.ServerID, Source: input.Source, Severity: input.Severity,
		Priority: priority, ReceivedAt: input.ReceivedAt.Format(time.RFC3339Nano),
		RecommendedAction: input.RecommendedAction, TraceID: input.TraceID,
		AuditID: input.AuditID,
	}
	if connector != nil {
		envelope.ConnectorID = connector.ConnectorID
		envelope.ConnectorGrantID = connector.ConnectorGrantID
		envelope.RequiredScopes = append([]string(nil), connector.RequiredScopes...)
		envelope.BindingScope = connector.BindingScope
	}
	return input, envelope, nil
}

func normalizeConnector(input *ConnectorRequirement) (*ConnectorRequirement, error) {
	if input == nil {
		return nil, nil
	}
	copy := *input
	copy.ConnectorID = strings.TrimSpace(copy.ConnectorID)
	copy.ConnectorGrantID = strings.TrimSpace(copy.ConnectorGrantID)
	copy.BindingScope = strings.TrimSpace(copy.BindingScope)
	copy.RequiredScopes = append([]string(nil), copy.RequiredScopes...)
	sort.Strings(copy.RequiredScopes)
	if !stableID(copy.ConnectorID) || !stableID(copy.ConnectorGrantID) || len(copy.RequiredScopes) == 0 || len(copy.RequiredScopes) > 32 {
		return nil, ErrInvalidSignal
	}
	if copy.BindingScope != "server" && copy.BindingScope != "signal" {
		return nil, ErrInvalidSignal
	}
	for index, scope := range copy.RequiredScopes {
		if strings.TrimSpace(scope) != scope || scope == "" || len(scope) > 128 || strings.Contains(scope, ",") ||
			(index > 0 && copy.RequiredScopes[index-1] == scope) {
			return nil, ErrInvalidSignal
		}
	}
	return &copy, nil
}

func validSource(source Source) bool {
	switch source {
	case SourceHealth, SourceDrift, SourceBackup, SourceCert, SourceDeploy, SourceResource, SourceConnector:
		return true
	default:
		return false
	}
}

func stableID(value string) bool {
	return stableIDPattern.MatchString(value) && !strings.Contains(value, "://")
}

func validTenantID(value string) bool {
	return tenantIDPattern.MatchString(value) && !strings.Contains(value, "://")
}

func validClaimOwner(value string) bool {
	return claimOwnerPattern.MatchString(value) && !strings.Contains(value, "://")
}

func RetryDelay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	delay := time.Second * time.Duration(1<<min(attempt, 9))
	if delay > 5*time.Minute {
		return 5 * time.Minute
	}
	return delay
}
