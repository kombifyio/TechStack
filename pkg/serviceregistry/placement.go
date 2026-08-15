package serviceregistry

import (
	"fmt"
	"strings"
	"time"
)

// TargetKind identifies the durable runtime target of a service. A server is
// the only target kind that has a ServerID; provider-native workloads are
// intentionally modeled without a fake server aggregate.
type TargetKind string

const (
	TargetKindServer          TargetKind = "server"
	TargetKindManagedWorkload TargetKind = "managed_workload"
	TargetKindUnknown         TargetKind = "unknown"
)

// Placement is the evidence-backed service placement. Managed workload fields
// are data-model support for a future provider slice; they do not enable
// provider provisioning, SLA claims, backup execution, or cleanup today.
type Placement struct {
	TargetKind         TargetKind
	ProviderID         string
	ManagedTargetRef   string
	ProviderReceiptRef string
	SLAPolicyRef       string
	BackupPolicyRef    string
	EvidenceRef        string
	ObservedAt         *time.Time
}

// NormalizePlacement removes transport-only variation and derives the safe
// default only from an actual ServerID. Missing evidence never becomes local
// or managed.
func NormalizePlacement(serverID string, placement Placement) Placement {
	serverID = strings.TrimSpace(serverID)
	placement.TargetKind = TargetKind(normalizePlacementValue(string(placement.TargetKind)))
	placement.ProviderID = normalizePlacementValue(placement.ProviderID)
	placement.ManagedTargetRef = strings.TrimSpace(placement.ManagedTargetRef)
	placement.ProviderReceiptRef = strings.TrimSpace(placement.ProviderReceiptRef)
	placement.SLAPolicyRef = strings.TrimSpace(placement.SLAPolicyRef)
	placement.BackupPolicyRef = strings.TrimSpace(placement.BackupPolicyRef)
	placement.EvidenceRef = strings.TrimSpace(placement.EvidenceRef)
	if placement.ObservedAt != nil {
		observedAt := placement.ObservedAt.UTC()
		placement.ObservedAt = &observedAt
	}
	if placement.TargetKind == "" {
		if serverID != "" {
			placement.TargetKind = TargetKindServer
		} else {
			placement.TargetKind = TargetKindUnknown
		}
	}
	return placement
}

// ClonePlacement keeps runtime projections immutable to callers.
func ClonePlacement(placement Placement) Placement {
	if placement.ObservedAt != nil {
		observedAt := *placement.ObservedAt
		placement.ObservedAt = &observedAt
	}
	return placement
}

// PlacementIntentPresent distinguishes an omitted patch from an explicit
// placement update. TargetKindUnknown is an explicit fail-closed clear.
func PlacementIntentPresent(placement Placement) bool {
	return strings.TrimSpace(string(placement.TargetKind)) != "" ||
		strings.TrimSpace(placement.ProviderID) != "" ||
		strings.TrimSpace(placement.ManagedTargetRef) != "" ||
		strings.TrimSpace(placement.ProviderReceiptRef) != "" ||
		strings.TrimSpace(placement.SLAPolicyRef) != "" ||
		strings.TrimSpace(placement.BackupPolicyRef) != "" ||
		strings.TrimSpace(placement.EvidenceRef) != "" || placement.ObservedAt != nil
}

// ValidatePlacement enforces the disjoint placement shapes. In particular,
// Managed workloads cannot masquerade as servers and a server placement cannot
// exist without the exact ServerID to which it is bound.
func ValidatePlacement(serverID string, placement Placement) error {
	serverID = strings.TrimSpace(serverID)
	placement = NormalizePlacement(serverID, placement)
	managedFieldsEmpty := func() bool {
		return placement.ProviderID == "" && placement.ManagedTargetRef == "" &&
			placement.ProviderReceiptRef == "" && placement.SLAPolicyRef == "" &&
			placement.BackupPolicyRef == "" && placement.EvidenceRef == "" && placement.ObservedAt == nil
	}
	switch placement.TargetKind {
	case TargetKindServer:
		if serverID == "" || !managedFieldsEmpty() {
			return fmt.Errorf("server placement requires server_id and no managed-workload fields")
		}
		return nil
	case TargetKindManagedWorkload:
		if serverID != "" || placement.ProviderID == "" || placement.ManagedTargetRef == "" ||
			placement.ProviderReceiptRef == "" || placement.SLAPolicyRef == "" ||
			placement.BackupPolicyRef == "" || placement.EvidenceRef == "" ||
			placement.ObservedAt == nil || placement.ObservedAt.IsZero() {
			return fmt.Errorf("managed_workload requires no server_id plus provider target, receipt, SLA, backup, and observed evidence")
		}
		return nil
	case TargetKindUnknown:
		if serverID != "" || !managedFieldsEmpty() {
			return fmt.Errorf("unknown placement cannot carry server or managed-workload fields")
		}
		return nil
	default:
		return fmt.Errorf("unknown service target kind %q", placement.TargetKind)
	}
}

// PlacementEqual compares the persisted form, including evidence time.
func PlacementEqual(serverID string, left, right Placement) bool {
	left = NormalizePlacement(serverID, left)
	right = NormalizePlacement(serverID, right)
	if left.TargetKind != right.TargetKind || left.ProviderID != right.ProviderID ||
		left.ManagedTargetRef != right.ManagedTargetRef || left.ProviderReceiptRef != right.ProviderReceiptRef ||
		left.SLAPolicyRef != right.SLAPolicyRef || left.BackupPolicyRef != right.BackupPolicyRef ||
		left.EvidenceRef != right.EvidenceRef {
		return false
	}
	if left.ObservedAt == nil || right.ObservedAt == nil {
		return left.ObservedAt == right.ObservedAt
	}
	return left.ObservedAt.UTC().Equal(right.ObservedAt.UTC())
}

func normalizePlacementValue(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}
