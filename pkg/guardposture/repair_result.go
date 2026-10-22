package guardposture

import (
	"encoding/json"
	"fmt"
	"strings"

	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// RepairResultSchema is the document `stackkit host security repair` returns.
const RepairResultSchema = "stackkit.host-security-repair/v1"

type hostSecurityRepairDocument struct {
	SchemaVersion string `json:"schema_version"`
	Outcome       string `json:"outcome"`
	Steps         []struct {
		Control    string `json:"control"`
		Status     string `json:"status"`
		ReasonCode string `json:"reason_code"`
	} `json:"steps"`
}

// ParseRepairResult reads the stackkit.host-security-repair/v1 document
// out of the command result. A document is bounded and closed to the known
// vocabulary before it is stored; free text from the host never is.
func ParseRepairResult(result *agentpb.StackKitResult) (RepairResult, error) {
	var envelope struct {
		Status string          `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return RepairResult{}, fmt.Errorf("decode host security repair result: %w", err)
	}
	var document hostSecurityRepairDocument
	if json.Unmarshal(envelope.Data, &document) != nil || document.SchemaVersion != RepairResultSchema {
		return RepairResult{}, fmt.Errorf("host security repair returned no %s document (exit %d)", RepairResultSchema, result.ExitCode)
	}
	steps := make([]RepairStep, 0, len(document.Steps))
	for _, step := range document.Steps {
		if len(steps) == len(stackkitcommand.HostSecurityControlIDs) || !stackkitcommand.ValidHostSecurityControl(step.Control) {
			continue
		}
		status := strings.ToLower(strings.TrimSpace(step.Status))
		switch status {
		case "planned", "applied", "noop", "manual", "blocked", "failed":
		default:
			status = "failed"
		}
		reason := strings.ToLower(strings.TrimSpace(step.ReasonCode))
		if !outcomeToken.MatchString(reason) {
			reason = ""
		}
		steps = append(steps, RepairStep{Control: step.Control, Status: status, ReasonCode: reason})
	}
	switch envelope.Status {
	case "success", "failed":
		return RepairResult{Outcome: document.Outcome, Steps: steps}, nil
	case "denied":
		code := "refused"
		for _, step := range steps {
			if step.Status == "blocked" && step.ReasonCode != "" {
				code = step.ReasonCode
				break
			}
		}
		return RepairResult{}, &RepairRefusal{Code: code, Steps: steps}
	default:
		return RepairResult{}, fmt.Errorf("host security repair status %q is unknown", envelope.Status)
	}
}
