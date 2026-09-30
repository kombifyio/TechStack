package controlplane

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
)

// OperatorSelfDisclosure is one principal's persisted voluntary
// self-disclosure (CREATION-EXPERIENCE-STANDARD §4). Disclosure holds the raw,
// already normalized answers; the capability profile is derived from them by
// the Unifier and is never stored.
type OperatorSelfDisclosure struct {
	ID             string
	TenantID       string
	OwnerSubjectID string
	Source         string
	Disclosure     core.OperatorSelfDisclosure
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

// OperatorSelfDisclosureStore persists self-disclosures per tenant and
// principal. Upsert replaces the whole disclosure: the operator's latest
// answers are the truth, and an unanswered question means "not told".
type OperatorSelfDisclosureStore interface {
	GetOperatorSelfDisclosure(ctx context.Context, tenantID, ownerSubjectID string) (*OperatorSelfDisclosure, error)
	UpsertOperatorSelfDisclosure(ctx context.Context, disclosure OperatorSelfDisclosure) (*OperatorSelfDisclosure, error)
}

// Self-disclosure sources name the surface that captured the answers.
const (
	OperatorSelfDisclosureSourceCloud     = "cloud"
	OperatorSelfDisclosureSourceTechstack = "techstack"
)

// normalizeOperatorSelfDisclosure validates the record and returns a trimmed
// copy whose answers are in closed-vocabulary form. The input is not modified.
func normalizeOperatorSelfDisclosure(disclosure OperatorSelfDisclosure) (OperatorSelfDisclosure, error) {
	out := *cloneOperatorSelfDisclosure(disclosure)
	out.ID = strings.TrimSpace(out.ID)
	out.TenantID = strings.TrimSpace(out.TenantID)
	out.OwnerSubjectID = strings.TrimSpace(out.OwnerSubjectID)
	if out.ID == "" {
		return out, fmt.Errorf("controlplane: self-disclosure id required")
	}
	if out.TenantID == "" {
		return out, fmt.Errorf("controlplane: tenant id required")
	}
	if out.OwnerSubjectID == "" {
		return out, fmt.Errorf("controlplane: owner subject id required")
	}
	switch out.Source {
	case OperatorSelfDisclosureSourceCloud, OperatorSelfDisclosureSourceTechstack:
	default:
		return out, fmt.Errorf("controlplane: self-disclosure source %q is not valid", out.Source)
	}
	if err := out.Disclosure.Normalize(); err != nil {
		return out, fmt.Errorf("controlplane: self-disclosure: %w", err)
	}
	return out, nil
}

func cloneOperatorSelfDisclosure(disclosure OperatorSelfDisclosure) *OperatorSelfDisclosure {
	out := disclosure
	out.Disclosure.Goals = append([]string(nil), disclosure.Disclosure.Goals...)
	out.Disclosure.Hardware = append([]string(nil), disclosure.Disclosure.Hardware...)
	out.Disclosure.Motivations = append([]string(nil), disclosure.Disclosure.Motivations...)
	return &out
}
