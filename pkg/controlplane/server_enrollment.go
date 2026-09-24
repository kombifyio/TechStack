package controlplane

import (
	"context"
	"fmt"
	"strings"
)

// ServerEnrollment binds an optional registering worker and the
// control-plane-owned node before the canonical server event may reference
// either. Guard observations never use this seam.
type ServerEnrollment struct {
	Event  ServerEvent
	Node   Node
	Worker *Worker
}

type ServerEnrollmentResult struct {
	*ServerEventResult
	Worker *Worker
}

type ServerEnrollmentStore interface {
	ApplyServerEnrollment(context.Context, ServerEnrollment) (*ServerEnrollmentResult, error)
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
	var worker *Worker
	if command.Worker != nil {
		preparedWorker := *command.Worker
		preparedWorker.ID = strings.TrimSpace(preparedWorker.ID)
		preparedWorker.TenantID = strings.TrimSpace(preparedWorker.TenantID)
		preparedWorker.InstanceID = strings.TrimSpace(preparedWorker.InstanceID)
		preparedWorker.StackID = strings.TrimSpace(preparedWorker.StackID)
		preparedWorker.OwnerSubjectID = strings.TrimSpace(preparedWorker.OwnerSubjectID)
		if preparedWorker.ID != node.WorkerID || preparedWorker.TenantID != node.TenantID ||
			preparedWorker.InstanceID != node.InstanceID || preparedWorker.StackID != node.StackID ||
			preparedWorker.OwnerSubjectID != event.Runtime.OwnerSubjectID {
			return ServerEnrollment{}, fmt.Errorf("%w: registering worker does not match server enrollment", ErrConflict)
		}
		worker = cloneWorker(preparedWorker)
	}
	return ServerEnrollment{Event: event, Node: node, Worker: worker}, nil
}

func validateExistingEnrollmentNode(existing, requested Node) error {
	if existing.TenantID != requested.TenantID || existing.ID != requested.ID ||
		existing.InstanceID != requested.InstanceID || existing.StackID != requested.StackID ||
		existing.WorkerID != requested.WorkerID {
		return fmt.Errorf("%w: enrollment node is already bound to another control-plane identity", ErrConflict)
	}
	return nil
}
