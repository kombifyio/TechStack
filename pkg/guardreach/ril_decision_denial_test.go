package guardreach

import "testing"

func TestRILDecisionDenialReasonRemainsBounded(t *testing.T) {
	denial := parseGatewayDenial(403, []byte(`{"allowed":false,"reason":"ril_decision_not_allowed"}`))
	if denial.Status != 403 || denial.ReasonCode != "ril_decision_not_allowed" {
		t.Fatal("canonical RIL denial was not preserved")
	}
	for _, body := range []string{`{"reason":"secret payload: do not log"}`, `{"reason":"RIL_DECISION_NOT_ALLOWED"}`} {
		if d := parseGatewayDenial(403, []byte(body)); d.ReasonCode != "" {
			t.Fatal("unbounded reason was admitted")
		}
	}
}
