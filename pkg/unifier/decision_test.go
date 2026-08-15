package unifier

import (
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
)

func TestLoader_LoadInputEnvelopeBytes(t *testing.T) {
	loader := NewLoader()
	input, decisionContext, envelope, err := loader.LoadInputEnvelopeBytes([]byte(`{
		"spec": {
			"name": "env-stack",
			"stackkit": "basement-kit",
			"metadata": {
				"wizard_type": "techie"
			}
		},
		"decision_context": {
			"channel": "wizard:techie",
			"operator": {
				"score": 7,
				"band": "techie",
				"source": "test"
			}
		}
	}`))
	if err != nil {
		t.Fatalf("LoadInputEnvelopeBytes() failed: %v", err)
	}
	if !envelope {
		t.Fatal("expected envelope=true")
	}
	if input == nil || input.Name == nil || *input.Name != "env-stack" {
		t.Fatalf("unexpected input: %#v", input)
	}
	if decisionContext == nil || decisionContext.Operator == nil {
		t.Fatalf("expected decision context with operator, got %#v", decisionContext)
	}
	if decisionContext.Operator.Score != 7 {
		t.Fatalf("operator score = %d, want 7", decisionContext.Operator.Score)
	}
}

func TestPipelineDecisionContext_NoRegisteredServerAddsEnvironmentGap(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	pipeline := NewPipeline(engine)

	result := pipeline.ExecuteInput(&core.InputSpec{
		Name:     stringPtr("easy-stack"),
		StackKit: stringPtr(StackKitBase),
		Nodes: []core.InputNodeSpec{{
			Name: stringPtr("main"),
			Role: stringPtr("standalone"),
		}},
		Metadata: map[string]string{
			"created_by":                "wizard",
			"wizard_type":               "easy",
			"server_provisioning_mode":  "install-command",
			"operator_capability_score": "1",
		},
	})
	if !result.Success {
		t.Fatalf("pipeline failed: %s - %s", result.FailedStep, result.ErrorMessage)
	}
	if result.RequirementsSpec == nil {
		t.Fatal("expected requirements spec")
	}
	if !hasEnvironmentGap(result.RequirementsSpec.EnvironmentGaps, "foundation_node_or_managed_runtime_required") {
		t.Fatalf("expected foundation node gap, got %#v", result.RequirementsSpec.EnvironmentGaps)
	}
	if result.DecisionContext == nil || result.DecisionContext.Operator == nil {
		t.Fatal("expected decision context operator")
	}
	if result.DecisionContext.Operator.Band != "oneclick" {
		t.Fatalf("operator band = %q, want oneclick", result.DecisionContext.Operator.Band)
	}
}

func TestPipelineDecisionContext_RetiredHAKitFailsPreValidation(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("failed to create engine: %v", err)
	}
	pipeline := NewPipeline(engine)

	result := pipeline.Execute(&core.KombinationSpec{
		Name: "ha-stack",
		Kit:  StackKitHA,
		Nodes: []core.NodeSpec{{
			Name:     "main",
			Type:     "main",
			Provider: "local",
			SSH:      &core.SSHConfig{Host: "server.example.com", User: "root"},
		}},
		Metadata: map[string]string{
			"created_by":                "manual",
			"operator_capability_score": "10",
		},
	})
	if result.Success {
		t.Fatal("expected retired HA kit pipeline to fail")
	}
	if result.FailedStep != "pre-validation" {
		t.Fatalf("failed step = %q, want pre-validation (message: %s, validation: %#v)", result.FailedStep, result.ErrorMessage, result.ValidationResult)
	}
}

func stringPtr(value string) *string {
	return &value
}

func hasEnvironmentGap(gaps []core.EnvironmentGap, code string) bool {
	for _, gap := range gaps {
		if gap.Code == code {
			return true
		}
	}
	return false
}
