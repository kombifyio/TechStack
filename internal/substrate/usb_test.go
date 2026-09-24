package substrate

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

// Sensitive device custody: observe native effects even when a PUT response
// disappears; an acknowledged but unapplied attachment is never proof.
func TestUSBTransferObservesExclusiveCustodyAndRecoversLostResponse(t *testing.T) {
	for _, mode := range []string{"lost-response", "unapplied"} {
		t.Run(mode, func(t *testing.T) {
			target := GuestIdentity{ID: 1101, Name: "kombify-target", OperationTag: "kombify-op-" + strings.Repeat("a", 32)}
			sourceConfig := map[string]any{"name": "source", "usb0": "host=1-2", "digest": "source-revision"}
			targetConfig := map[string]any{"name": target.Name, "tags": "kombify-managed;" + target.OperationTag, "digest": "target-revision"}
			writes := 0
			client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
				var data any
				switch {
				case strings.HasSuffix(r.URL.Path, "/permissions"):
					data = map[string]any{"/vms": map[string]int{"VM.Audit": 1}, "/vms/1100": map[string]int{"VM.Audit": 1}, "/vms/1101": map[string]int{"VM.Audit": 1}}
				case strings.HasSuffix(r.URL.Path, "/hardware/usb"):
					data = []map[string]string{{"usbpath": "1-2", "serial": "radio", "vendid": "1234", "prodid": "abcd"}}
				case strings.HasSuffix(r.URL.Path, "/qemu"):
					data = []map[string]any{{"vmid": 1100, "status": "stopped"}, {"vmid": 1101, "status": "stopped"}}
				case strings.HasSuffix(r.URL.Path, "/lxc"):
					data = []any{}
				case strings.HasSuffix(r.URL.Path, "/config"):
					config := sourceConfig
					if strings.Contains(r.URL.Path, "/1101/") {
						config = targetConfig
					}
					if r.Method == http.MethodPut {
						writes++
						_ = r.ParseForm()
						if r.Form.Get("delete") != "" {
							delete(config, "usb0")
						} else if mode != "unapplied" {
							config["usb0"] = r.Form.Get("usb0")
						}
						if mode == "lost-response" {
							conn, _, err := w.(http.Hijacker).Hijack()
							if err != nil {
								t.Error(err)
								return
							}
							_ = conn.Close()
							return
						}
					}
					data = config
				default:
					t.Errorf("unexpected request %s", r.URL)
					w.WriteHeader(500)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			})
			grant := USBTransferGrant{SourceID: 1100, SourceName: "source", SourceBaseDigest: USBSourceConfigDigest(Guest{Config: sourceConfig}, "usb0"), Slot: "usb0", HostPath: "1-2", Serial: "radio", VendorID: "1234", ProductID: "abcd", ExpiresAt: time.Now().Add(time.Hour)}
			power := ObservedGuestGrant{ID: 1100, Name: "source", ConfigDigest: GuestConfigDigest(Guest{Config: sourceConfig}), ExpiresAt: grant.ExpiresAt, AllowStart: true, AllowStop: true}
			grant.SourcePowerConfigDigest = power.ConfigDigest
			client.cfg.CertificateSHA256 = strings.Repeat("a", 64)
			other := *client
			for _, mismatch := range []string{"node", "api", "certificate", "power-digest", "source-id", "source-name", "base-digest"} {
				candidate, changed := other, grant
				switch mismatch {
				case "node":
					candidate.cfg.Node = "other"
				case "api":
					candidate.base += "/other"
				case "certificate":
					candidate.cfg.CertificateSHA256 = strings.Repeat("b", 64)
				case "power-digest":
					changed.SourcePowerConfigDigest = ""
				case "source-id":
					changed.SourceID++
				case "source-name":
					changed.SourceName = "other"
				case "base-digest":
					changed.SourceBaseDigest = "changed"
				}
				if _, err := client.VerifyUSBPowerSource(t.Context(), &candidate, changed, power); err == nil {
					t.Fatalf("%s admitted unbound source power authority", mismatch)
				}
			}
			if _, err := client.VerifyUSBPowerSource(t.Context(), &other, grant, power); err != nil {
				t.Fatalf("bound source refused: %v", err)
			}
			err := client.TransferUSB(t.Context(), grant, target, true)
			if mode == "unapplied" {
				if err == nil {
					t.Fatal("unapplied attachment reported exclusive custody")
				}
				return
			}
			if err != nil {
				t.Fatalf("lost response was not reconciled: %v", err)
			}
			sourceConfig["digest"] = "new-source-revision"
			if _, err = client.VerifyUSBPowerSource(t.Context(), &other, grant, power); err != nil {
				t.Fatalf("detached source cannot resume: %v", err)
			}
			if err = client.TransferUSB(t.Context(), grant, target, true); err != nil || writes != 2 {
				t.Fatalf("completed transfer repeated mutations: %v (%d)", err, writes)
			}
			if err = client.TransferUSB(t.Context(), grant, target, false); err != nil || sourceConfig["usb0"] != "host=1-2" || targetConfig["usb0"] != nil {
				t.Fatalf("return custody: %v", err)
			}
		})
	}
}
