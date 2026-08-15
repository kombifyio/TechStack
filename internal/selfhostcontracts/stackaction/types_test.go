package stackaction

import "testing"

func TestVerifyRolloutRequestValidate(t *testing.T) {
	request := VerifyRolloutRequest{
		Action:  ActionVerifyRollout,
		StackID: "stack-compat",
		RuntimeTarget: RuntimeTarget{
			Host: "server.example",
			User: "root",
		},
	}
	if err := request.Validate(); err != nil {
		t.Fatalf("valid self-host rollout verification request rejected: %v", err)
	}
}
