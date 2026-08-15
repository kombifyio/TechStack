package httpguard

import (
	"strings"
	"testing"
)

// A bare status code cannot tell a benign race from a permanently rejected
// agent. The rejection envelope must reach the agent journal so a fail-closed
// conflict is diagnosable from the host it happens on.
func TestCompactResponseDetail(t *testing.T) {
	for _, test := range []struct {
		name    string
		payload string
		want    string
	}{
		{
			name:    "conflict envelope with reason",
			payload: `{"error":{"code":"CONFLICT","message":"Guard inventory conflicts with the accepted source position","details":{"reason":"Guard inventory canonical server provider binding is missing or different"}}}`,
			want:    "CONFLICT: Guard inventory conflicts with the accepted source position: Guard inventory canonical server provider binding is missing or different",
		},
		{
			name:    "envelope without details",
			payload: `{"error":{"code":"UNAUTHORIZED","message":"Runtime agent tenant is required"}}`,
			want:    "UNAUTHORIZED: Runtime agent tenant is required",
		},
		{name: "empty body", payload: "", want: ""},
		{name: "non json body", payload: "<html>502</html>", want: ""},
		{name: "unrelated json", payload: `{"data":{"ok":true}}`, want: ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := compactResponseDetail([]byte(test.payload)); got != test.want {
				t.Fatalf("detail = %q, want %q", got, test.want)
			}
		})
	}
}

// The journal line stays bounded no matter what the peer returns.
func TestCompactResponseDetailIsBounded(t *testing.T) {
	payload := `{"error":{"code":"CONFLICT","message":"` + strings.Repeat("x", 4000) + `"}}`
	if got := compactResponseDetail([]byte(payload)); len(got) != 300 {
		t.Fatalf("detail length = %d, want it clamped to 300", len(got))
	}
}
