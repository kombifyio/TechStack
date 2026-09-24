package homeassistant

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/substrate"
)

// Sensitive provider control: absent node authority must fail before even the
// admitted provisioner or either lifecycle adapter can perform an operation.
func TestUSBMigrationRejectsMissingNodeAuthorityAtRuntime(t *testing.T) {
	p := &ProxmoxMigrationRuntime{USB: &substrate.USBTransferGrant{}}
	for _, operation := range []struct {
		name string
		run  func(context.Context) (bool, error)
	}{
		{"prepare", p.PrepareIsolated}, {"stop-source", p.StopSource},
		{"start-source", p.StartSource}, {"stop-target", p.StopTarget},
		{"start-target", p.StartTarget}, {"transfer", p.TransferToTarget},
		{"return", p.TransferToSource},
	} {
		t.Run(operation.name, func(t *testing.T) {
			if ok, err := operation.run(t.Context()); err == nil || ok {
				t.Fatal("migration admitted USB without an enrolled node authority")
			}
		})
	}
}
