package stacks

import (
	"reflect"
	"testing"

	"github.com/kombifyio/techstack/pkg/specv2"
)

func TestWizardDesiredRequiredCapabilityAdmission(t *testing.T) {
	intent := specv2.WizardIntent{
		Goals:               []string{"ai"},
		UseCaseCapabilities: map[string]map[string]string{"ai": {"inference": "off", "observability": "off"}},
		UseCaseSettings:     map[string]map[string]any{"ai": {"model-pack-general": false}},
		UseCaseSetupAnswers: map[string]map[string][]string{"ai": {"tool-inference": {"llama-cpp"}, "extras": {}}},
	}
	effective, blocked, err := admitWizardDesiredCapabilities(intent)
	if err != nil || len(blocked) != 1 || blocked[0].UseCase != "ai" || blocked[0].Capability != "inference" || blocked[0].Selection != "llama-cpp" {
		t.Fatalf("recorded required choice was not admitted as blocked: blocked=%v err=%v", blocked, err)
	}
	if !reflect.DeepEqual(effective, intent) || intent.UseCaseCapabilities["ai"]["inference"] != "off" || intent.UseCaseCapabilities["ai"]["observability"] != "off" || len(intent.UseCaseSetupAnswers["ai"]["extras"]) != 0 {
		t.Fatal("desired pending/off/empty decisions changed")
	}
	if validateUseCaseCapabilitiesAgainstCatalog(effective) == nil {
		t.Fatal("the blocked intent must still fail strict execution admission")
	}
	for _, capabilities := range []map[string]map[string]string{nil, {"ai": {"observability": "off"}}} {
		answersOnly := intent
		answersOnly.UseCaseCapabilities = capabilities
		effective, blocked, err := admitWizardDesiredCapabilities(answersOnly)
		if err != nil || len(blocked) != 1 || blocked[0].Selection != "llama-cpp" || !reflect.DeepEqual(effective, answersOnly) {
			t.Fatalf("recorded guided choice was substituted when execution selection was absent: blocked=%v err=%v", blocked, err)
		}
	}
	for _, answer := range [][]string{nil, {}, {"not-in-the-catalog"}, {"ollama"}, {"llama-cpp", "ollama"}} {
		candidate := intent
		candidate.UseCaseSetupAnswers = map[string]map[string][]string{"ai": {"tool-inference": answer}}
		if _, _, err := admitWizardDesiredCapabilities(candidate); err == nil {
			t.Fatalf("required off accepted without exactly one recorded desired choice: %v", answer)
		}
	}
	conflicting := intent
	conflicting.UseCaseCapabilities = map[string]map[string]string{"ai": {"inference": "ollama"}}
	if _, _, err := admitWizardDesiredCapabilities(conflicting); err == nil {
		t.Fatal("a supported substitute conflicts with the recorded desired choice")
	}
}
