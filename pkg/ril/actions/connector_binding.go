package actions

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"
)

var (
	connectorBindingIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
	connectorBindingHashPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
)

// ConnectorBindingExpectation is the stable Gateway projection identity saved
// on a connector-dependent action card. It contains no connector credential.
type ConnectorBindingExpectation struct {
	BindingID   string `json:"binding_id"`
	BindingHash string `json:"binding_hash"`
}

// ConnectorBindingProjection is the exact, current Gateway-minted projection
// consumed at execution. The HTTP adapter accepts it only on a verified edge
// hop; the authority independently binds every field to the persisted card.
type ConnectorBindingProjection struct {
	GrantID      string
	BindingID    string
	BindingHash  string
	ConnectorID  string
	BindingScope string
	ResourceID   string
	ServerID     string
	Scopes       []string
	Status       string
}

func validateConnectorBindingExpectation(expected *ConnectorBindingExpectation) error {
	if expected == nil {
		return nil
	}
	if !connectorBindingIDPattern.MatchString(expected.BindingID) ||
		!connectorBindingHashPattern.MatchString(expected.BindingHash) {
		return fmt.Errorf("ril actions: invalid connector binding expectation")
	}
	return nil
}

func validateConnectorBinding(card *GovernedCard, input BeginExecution) error {
	expected := card.Template.ConnectorBinding
	if expected == nil {
		if input.ConnectorProjection != nil {
			return ErrCardConflict
		}
		return nil
	}
	projection := input.ConnectorProjection
	if projection == nil || strings.TrimSpace(expected.BindingID) == "" ||
		projection.BindingID != expected.BindingID || projection.BindingHash != expected.BindingHash ||
		projection.Status != "active" || projection.BindingScope != "action-card" ||
		projection.ResourceID != card.ID || projection.ServerID != card.ServerID ||
		len(projection.Scopes) == 0 || !strictSortedConnectorScopes(projection.Scopes) {
		return ErrConnectorBindingRequired
	}
	computed := connectorBindingDigest(input.TenantID, input.OwnerSubjectID, *projection)
	if projection.BindingHash != computed {
		return ErrConnectorBindingRequired
	}
	if connectorMutationOperation(card.Template.Primitive.OperationClass) && !connectorProjectionCanMutate(projection.Scopes) {
		return ErrConnectorGrantInsufficient
	}
	return nil
}

func connectorBindingDigest(tenantID, subjectID string, projection ConnectorBindingProjection) string {
	canonical := strings.Join([]string{
		"gateway.connector-binding/v1",
		tenantID,
		subjectID,
		projection.BindingID,
		projection.GrantID,
		projection.ConnectorID,
		projection.BindingScope,
		projection.ResourceID,
		projection.ServerID,
		strings.Join(projection.Scopes, ","),
	}, "\x00")
	sum := sha256.Sum256([]byte(canonical))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func strictSortedConnectorScopes(scopes []string) bool {
	for index, scope := range scopes {
		if strings.TrimSpace(scope) != scope || scope == "" || strings.Contains(scope, ",") {
			return false
		}
		if index > 0 && scopes[index-1] >= scope {
			return false
		}
	}
	return true
}

func connectorMutationOperation(operationClass string) bool {
	switch operationClass {
	case "product-apply", "product-upgrade", "product-rollback", "mutation":
		return true
	default:
		return false
	}
}

func connectorProjectionCanMutate(scopes []string) bool {
	for _, scope := range scopes {
		if strings.HasSuffix(scope, ":write") || strings.HasSuffix(scope, ":manage") ||
			strings.HasSuffix(scope, ":execute") || scope == "mcp:tools" || scope == "connectors:homelab" {
			return true
		}
	}
	return false
}
