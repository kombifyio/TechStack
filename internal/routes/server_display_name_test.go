package routes

import "testing"

func TestQualifyCollidingServerNamesAddsProviderQualifier(t *testing.T) {
	got := qualifyCollidingServerNames(
		[]string{"t3-code-ryzen", "t3-code-ryzen", "ryzen-pve"},
		[]string{"hostinger", "self owned device", "local"},
	)
	if got[0] != "t3-code-ryzen · hostinger" || got[1] != "t3-code-ryzen · self owned device" || got[2] != "ryzen-pve" {
		t.Fatalf("qualifyCollidingServerNames = %#v", got)
	}
}
