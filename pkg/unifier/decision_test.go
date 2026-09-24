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
		StackKit: stringPtr(StackKitBasement),
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
	// The environment gap is decided before kit validation, so this test does
	// not depend on a StackKits tree being present. Overall pipeline success
	// belongs to the integration lane that supplies the pinned kit.
	if result.RequirementsSpec == nil {
		t.Fatalf("expected requirements spec, pipeline stopped at %s: %s", result.FailedStep, result.ErrorMessage)
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

	result := pipeline.ExecuteInput(&core.InputSpec{
		Name:     stringPtr("ha-stack"),
		StackKit: stringPtr(StackKitHA),
		Nodes: []core.InputNodeSpec{{
			Name:     stringPtr("main"),
			Type:     stringPtr("main"),
			Provider: stringPtr("local"),
			SSH: &core.InputSSHConfig{
				Host: stringPtr("server.example.com"),
				User: stringPtr("root"),
			},
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
