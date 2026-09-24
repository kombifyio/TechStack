package controlplane

import (
	"time"

	"github.com/kombifyio/techstack/pkg/outcome"
	"github.com/kombifyio/techstack/pkg/serverregistry"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

func cloneStack(stack Stack) *Stack {
	stack.Config = cloneMap(stack.Config)
	stack.Services = cloneSliceOfMaps(stack.Services)
	stack.RuntimeSummary = cloneMap(stack.RuntimeSummary)
	stack.DriftCheckedAt = cloneTime(stack.DriftCheckedAt)
	stack.DeletedAt = cloneTime(stack.DeletedAt)
	return &stack
}

func cloneJob(job Job) *Job {
	job.Logs = cloneSliceOfMaps(job.Logs)
	job.Result = cloneMap(job.Result)
	job.Payload = cloneMap(job.Payload)
	job.StartedAt = cloneTime(job.StartedAt)
	job.CompletedAt = cloneTime(job.CompletedAt)
	return &job
}

func cloneWorker(worker Worker) *Worker {
	worker.ApprovedAt = cloneTime(worker.ApprovedAt)
	worker.LastSeenAt = cloneTime(worker.LastSeenAt)
	worker.Tags = cloneMap(worker.Tags)
	worker.Capabilities = cloneMap(worker.Capabilities)
	worker.Resources = cloneMap(worker.Resources)
	return &worker
}

func clonePairingToken(token PairingToken) *PairingToken {
	token.ExpiresAt = cloneTime(token.ExpiresAt)
	token.UsedAt = cloneTime(token.UsedAt)
	token.Metadata = cloneMap(token.Metadata)
	return &token
}

func cloneNode(node Node) *Node {
	node.Metadata = cloneMap(node.Metadata)
	return &node
}

func cloneService(service Service) *Service {
	service.Metadata = cloneMap(service.Metadata)
	return &service
}

func cloneServiceRuntime(service ServiceRuntime) *ServiceRuntime {
	service.ObservedAt = cloneTime(service.ObservedAt)
	service.MutationLock.ChangedAt = cloneTime(service.MutationLock.ChangedAt)
	service.Placement = serviceregistry.ClonePlacement(service.Placement)
	service.Access = cloneMap(service.Access)
	service.Metadata = cloneMap(service.Metadata)
	service.Capabilities = append([]string(nil), service.Capabilities...)
	return &service
}

func cloneRILServer(server RILServer) *RILServer {
	server.Health = cloneMap(server.Health)
	server.Inventory = cloneMap(server.Inventory)
	server.LastSeenAt = cloneTime(server.LastSeenAt)
	return &server
}

func cloneServerRuntime(server ServerRuntime) *ServerRuntime {
	server.LastHeartbeatAt = cloneTime(server.LastHeartbeatAt)
	server.DecommissionedAt = cloneTime(server.DecommissionedAt)
	server.OutcomeChangedAt = cloneTime(server.OutcomeChangedAt)
	server.RuntimeTarget = serverregistry.CloneRuntimeTarget(server.RuntimeTarget)
	server.Metadata = cloneMap(server.Metadata)
	server.Channels = cloneServerChannels(server.Channels)
	server.LastOutcome = outcome.Clone(server.LastOutcome)
	return &server
}

func cloneServerChannels(in []ServerChannel) []ServerChannel {
	if len(in) == 0 {
		return nil
	}
	out := make([]ServerChannel, len(in))
	for i, channel := range in {
		out[i] = channel
		out[i].ObservedAt = cloneTime(channel.ObservedAt)
		out[i].Metadata = cloneMap(channel.Metadata)
	}
	return out
}

func cloneRILCommand(command RILCommand) *RILCommand {
	command.Request = cloneMap(command.Request)
	command.Result = cloneMap(command.Result)
	command.CompletedAt = cloneTime(command.CompletedAt)
	return &command
}

func cloneRILActionCard(card RILActionCard) *RILActionCard {
	card.Action = cloneMap(card.Action)
	card.Decision = cloneMap(card.Decision)
	card.ResolvedAt = cloneTime(card.ResolvedAt)
	return &card
}

func cloneRILHealEvent(event RILHealEvent) *RILHealEvent {
	event.Details = cloneMap(event.Details)
	return &event
}

func cloneWalletItem(item WalletItem) *WalletItem {
	item.Metadata = cloneMap(item.Metadata)
	return &item
}

func cloneActivityEvent(event ActivityEvent) *ActivityEvent {
	event.Details = cloneMap(event.Details)
	return &event
}

func cloneMap(in map[string]any) map[string]any {
	if len(in) == 0 {
		return map[string]any{}
	}
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneSliceOfMaps(in []map[string]any) []map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make([]map[string]any, len(in))
	for i, item := range in {
		out[i] = cloneMap(item)
	}
	return out
}

func cloneTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}
