package stackkitcommand

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// ServiceActionEvidence consumes the pinned CLI's existing service-control
// result. Its observation is taken by that controller after the action.
type ServiceActionEvidence struct {
	APIVersion    string `json:"apiVersion"`
	Action        string `json:"action"`
	ServiceKey    string `json:"serviceKey"`
	DesiredState  string `json:"desiredState"`
	ObservedState string `json:"observedState"`
	Revision      uint64 `json:"revision"`
	EvidenceRef   string `json:"evidenceRef"`
	ReasonCode    string `json:"reasonCode,omitempty"`
	Checks        []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	} `json:"checks"`
}

func ParseServiceActionEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (ServiceActionEvidence, error) {
	var evidence ServiceActionEvidence
	if err := ValidateCommand(command); err != nil {
		return evidence, err
	}
	if err := ValidateResult(result, command); err != nil {
		return evidence, err
	}
	action := ""
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_START:
		action = "start"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_SERVICE_STOP:
		action = "stop"
	default:
		return evidence, fmt.Errorf("service mutation command is required")
	}
	var envelope struct {
		Data struct {
			Output string `json:"output"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return evidence, err
	}
	if err := json.Unmarshal([]byte(envelope.Data.Output), &evidence); err != nil {
		return evidence, fmt.Errorf("decode StackKits service action evidence: %w", err)
	}
	if !result.Success || evidence.APIVersion != "stackkit.service-action-result/v1" || evidence.Action != action || evidence.ServiceKey != command.ServiceKey || evidence.ReasonCode != "" || evidence.Revision == 0 {
		return evidence, fmt.Errorf("StackKits service action evidence does not match the dispatched service")
	}
	if !strings.HasPrefix(evidence.EvidenceRef, "stackkit-evidence://service-control/sha256:") || !sha256Pattern.MatchString(strings.TrimPrefix(evidence.EvidenceRef, "stackkit-evidence://service-control/sha256:")) {
		return evidence, fmt.Errorf("StackKits service action evidence reference is invalid")
	}
	wantState := "running"
	if action == "stop" {
		wantState = "stopped"
	}
	if evidence.DesiredState != wantState || evidence.ObservedState != wantState {
		return evidence, fmt.Errorf("StackKits service action did not observe the intended %s state", wantState)
	}
	checks := map[string]string{}
	for _, check := range evidence.Checks {
		if _, duplicate := checks[check.Name]; duplicate {
			return evidence, fmt.Errorf("StackKits service action checks are ambiguous")
		}
		checks[check.Name] = check.Status
	}
	for _, name := range []string{"owner_approval", "runtime_action", "runtime_observation"} {
		if checks[name] != "ok" {
			return evidence, fmt.Errorf("StackKits service action %s check is not successful", name)
		}
	}
	return evidence, nil
}
