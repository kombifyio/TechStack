package controlplane

import (
	"context"
	"fmt"
	"strings"
)

// ServerEnrollment binds the control-plane-owned node before the canonical
// server event may reference it. Guard observations never use this seam.
type ServerEnrollment struct {
	Event ServerEvent
	Node  Node
}

type ServerEnrollmentStore interface {
	ApplyServerEnrollment(context.Context, ServerEnrollment) (*ServerEventResult, error)
}

func prepareServerEnrollment(command ServerEnrollment) (ServerEnrollment, error) {
	event, node := command.Event, command.Node
	node.ID = strings.TrimSpace(node.ID)
	node.TenantID = strings.TrimSpace(node.TenantID)
	node.InstanceID = strings.TrimSpace(node.InstanceID)
	node.StackID = strings.TrimSpace(node.StackID)
	node.WorkerID = strings.TrimSpace(node.WorkerID)
	node.Name = strings.TrimSpace(node.Name)
	node.Role = firstNonEmpty(strings.TrimSpace(node.Role), "foundation")
	node.Status = firstNonEmpty(strings.TrimSpace(node.Status), "pending")
	if event.Authority != ServerEventAuthorityControlPlane || node.Status != "pending" ||
		node.ID == "" || node.TenantID == "" || node.WorkerID == "" ||
		event.ServerID != node.ID || event.TenantID != node.TenantID || event.Runtime.NodeID != node.ID ||
		event.Runtime.StackID != node.StackID || event.Runtime.WorkerID != node.WorkerID ||
		event.Runtime.InstanceID != node.InstanceID {
		return ServerEnrollment{}, fmt.Errorf("%w: control-plane enrollment node binding is invalid", ErrConflict)
	}
	if err := validateSecretFreeObservation(node.Metadata, "server_enrollment.node.metadata"); err != nil {
		return ServerEnrollment{}, err
	}
	node.Metadata = cloneMap(node.Metadata)
	return ServerEnrollment{Event: event, Node: node}, nil
}

func validateExistingEnrollmentNode(existing, requested Node) error {
	if existing.TenantID != requested.TenantID || existing.ID != requested.ID ||
		existing.InstanceID != requested.InstanceID || existing.StackID != requested.StackID ||
		existing.WorkerID != requested.WorkerID {
		return fmt.Errorf("%w: enrollment node is already bound to another control-plane identity", ErrConflict)
	}
	return nil
}
