package devicereadiness_test

import (
	"context"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/devicereadiness"
)

// Apply is where observing stops and changing a machine begins. Three
// boundaries make that defensible, and all three live in the package rather
// than in its callers, so they are tested here.

func TestOnlyCatalogEntriesCanBeApplied(t *testing.T) {
	// The caller names a resolution; it never hands one over. A caller that
	// could pass a Resolution could pass any command, and the catalog would
	// stop being the boundary it exists to be. This is checked before any
	// connection, which is why an unconnected session is the right subject.
	var session devicereadiness.Session

	_, err := session.Apply(context.Background(), "rm-everything", devicereadiness.Parameters{})

	if err == nil {
		t.Fatal("a resolution outside the catalog was accepted")
	}
	if !strings.Contains(err.Error(), "catalog") {
		t.Fatalf("error does not say why it refused: %v", err)
	}
}

func TestAdviceCannotBeCarriedOut(t *testing.T) {
	// A hint exists because something outside our authority has to change.
	// Executing one would mean acting on a decision nobody made.
	var advice string
	for _, resolution := range devicereadiness.Resolutions() {
		if resolution.Mode == devicereadiness.ModeHint {
			advice = resolution.ID
			break
		}
	}
	if advice == "" {
		t.Skip("the catalog currently carries no advice")
	}

	var session devicereadiness.Session
	_, err := session.Apply(context.Background(), advice, devicereadiness.Parameters{})

	if err == nil {
		t.Fatalf("advice %q was carried out", advice)
	}
}

func TestAFixNeedingAWayOutIsRefusedWhenNoneIsOpen(t *testing.T) {
	// Installing a driver on a device with no route is the case this whole
	// capability exists for. Attempting it without the borrowed route would
	// fail slowly and confusingly instead of saying what is missing.
	var needsEgress string
	for _, resolution := range devicereadiness.Resolutions() {
		if resolution.RequiresEgress {
			needsEgress = resolution.ID
			break
		}
	}
	if needsEgress == "" {
		t.Skip("no catalog entry currently needs a way out")
	}

	var session devicereadiness.Session
	_, receiptErr := session.Apply(context.Background(), needsEgress, devicereadiness.Parameters{})

	if receiptErr == nil {
		t.Fatalf("%q ran without the way out it declares it needs", needsEgress)
	}
}

func TestApplyAlwaysReturnsAReceiptEvenWhenItRefuses(t *testing.T) {
	// The receipt is the answer to "what did we do to this machine", and a
	// refusal is part of that answer.
	var session devicereadiness.Session

	receipt, err := session.Apply(context.Background(), "not-a-resolution", devicereadiness.Parameters{})

	if err == nil {
		t.Fatal("expected a refusal")
	}
	if receipt.ResolutionID != "not-a-resolution" || receipt.Succeeded {
		t.Fatalf("refusal produced no usable receipt: %+v", receipt)
	}
	if receipt.Failure == "" {
		t.Fatal("the receipt does not record why nothing happened")
	}
}

func TestAnInterfaceThatIsNotAnInterfaceNameNeverReachesTheDevice(t *testing.T) {
	// The interface is the one request value a catalog command carries to the
	// device's root shell. A value that smuggles an option or the kernel
	// expansion the quoting deliberately passes through must be refused
	// before any connection is used, which is why an unconnected session is
	// the right subject.
	var session devicereadiness.Session
	for _, iface := range []string{"eth0$(uname -r)$(id)", "-oProxyCommand=id", "eth0;reboot", "eth0 up; id"} {
		receipt, err := session.Apply(context.Background(), devicereadiness.ResolutionLinkUp, devicereadiness.Parameters{Interface: iface})
		if err == nil || len(receipt.Commands) != 0 {
			t.Fatalf("interface %q was sent to the device: err=%v commands=%d", iface, err, len(receipt.Commands))
		}
	}
}
