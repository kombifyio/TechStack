package agent

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

func TestStackKitResultNormalizesSuccessfulEnvelopeAfterPostProcessFailure(t *testing.T) {
	command := &agentpb.StackKitCommand{Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_APPLY}
	result := finishStackKitResult(&agentpb.StackKitResult{}, command, []byte(`{"schemaVersion":"stackkit.command-result/v1","command":"stackkit apply","status":"success","data":{"status":"failed","outcomes":{"overall":"failed","units":[{"ref":"cloud-core","outcome":"failed"}]}}}`), nil, errors.New("post-process failed"), time.Now())
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			Outcomes struct {
				Units []struct {
					Ref     string `json:"ref"`
					Outcome string `json:"outcome"`
				} `json:"units"`
			} `json:"outcomes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil || result.Success || result.ExitCode == 0 || envelope.Status != "failed" {
		t.Fatalf("result transport remained inconsistent: success=%v exit=%d status=%q", result.Success, result.ExitCode, envelope.Status)
	}
	if len(envelope.Data.Outcomes.Units) == 0 || envelope.Data.Outcomes.Units[0].Ref != "cloud-core" || envelope.Data.Outcomes.Units[0].Outcome != "failed" {
		t.Fatal("process failure discarded typed per-unit Apply evidence")
	}
}
