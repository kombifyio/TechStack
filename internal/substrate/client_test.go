package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// These tests use the private transport constructor only to avoid depending
// on Windows ACL emulation for a Linux-only token file. Assertions exercise
// real HTTP public API effects at the sensitive provider-control boundary.
func fixtureClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return &Client{cfg: Config{Node: "pve", MinGuestID: 1000, MaxGuestID: 1999}, base: server.URL + "/api2/json", http: server.Client(), credential: func() (string, error) { return "test@pve!guard=not-a-secret", nil }}
}

func TestLostCreateResponseRecoversExactGuestWithoutAnotherCreate(t *testing.T) {
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "snippets"), 0700); err != nil {
		t.Fatal(err)
	}
	image, _ := DefaultImage("ubuntu-24.04")
	spec := GuestSpec{GuestIdentity: GuestIdentity{ID: 1100, Name: "kombify-home", OperationTag: "kombify-op-" + strings.Repeat("a", 32)}, ProfileID: "ubuntu-24.04", SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest", CPU: 2, MemoryMiB: 4096, DiskGiB: 32, Storage: "local", Bridge: "vmbr0", ImageImport: ImageImport{Image: image, Storage: "local", TaskRef: "UPID:pve:import"}}
	created := false
	machine, hvm := "aarch64", 1
	calls := 0
	var config map[string]any
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch {
		case r.URL.Path == "/api2/json/storage/local":
			data = map[string]any{"type": "dir", "path": directory, "content": "images,import,snippets"}
		case r.URL.Path == "/api2/json/access/permissions":
			data = map[string]any{"/vms/1100": map[string]int{"VM.Audit": 1}}
		case r.Method == http.MethodPost:
			calls++
			_ = r.ParseForm()
			created = true
			config = map[string]any{"name": r.Form.Get("name"), "tags": r.Form.Get("tags"), "description": r.Form.Get("description"), "scsi0": "local:vm-1100-disk-0"}
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			_ = connection.Close()
			return
		case strings.HasSuffix(r.URL.Path, "/qemu"):
			if created {
				data = []map[string]any{{"vmid": 1100, "name": spec.Name, "status": "stopped"}}
			} else {
				data = []any{}
			}
		case strings.HasSuffix(r.URL.Path, "/config"):
			data = config
		case strings.Contains(r.URL.Path, "/tasks/"):
			data = map[string]any{"status": "stopped", "exitstatus": "OK"}
		case strings.HasSuffix(r.URL.Path, "/version"):
			data = map[string]any{"version": "9.0"}
		case strings.HasSuffix(r.URL.Path, "/status"):
			data = map[string]any{"current-kernel": map[string]string{"machine": machine}, "cpuinfo": map[string]int{"cpus": 8, "hvm": hvm}, "memory": map[string]uint64{"available": 32 << 30}}
		case strings.HasSuffix(r.URL.Path, "/storage"):
			data = []map[string]any{{"storage": "local", "content": "images,import", "active": 1, "avail": 128 << 30}}
		case strings.HasSuffix(r.URL.Path, "/network"):
			data = []map[string]any{{"iface": "vmbr0", "type": "bridge", "active": 1}}
		default:
			t.Errorf("unexpected request %s", r.URL)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	if _, err := client.SubmitCreate(t.Context(), spec); !errors.Is(err, ErrCapability) || calls != 0 {
		t.Fatal("amd64 guest reached unsupported native architecture")
	}
	machine, hvm = "x86_64", 0
	if _, err := client.SubmitCreate(t.Context(), spec); !errors.Is(err, ErrCapability) || calls != 0 {
		t.Fatal("guest reached host without hardware virtualization")
	}
	hvm = 1
	if _, err := client.SubmitCreate(context.Background(), spec); err == nil {
		t.Fatal("lost response must not report submission success")
	}
	spec.ImageImport.TaskRef = "UPID:pve:transport-recovered"
	result, err := client.SubmitCreate(context.Background(), spec)
	if err != nil || !result.Recovered || result.GuestID != spec.ID || calls != 1 {
		t.Fatalf("recovery=%+v err=%v creates=%d", result, err, calls)
	}
	spec.MemoryMiB = 8192
	if _, err := client.SubmitCreate(context.Background(), spec); !errors.Is(err, ErrConflict) || calls != 1 {
		t.Fatalf("changed request must not adopt existing guest: %v", err)
	}
}

func TestObservedGuestCannotBeDeletedByNamingItManaged(t *testing.T) {
	mutations := 0
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			mutations++
			w.WriteHeader(500)
			return
		}
		var data any
		switch {
		case strings.HasSuffix(r.URL.Path, "/permissions"):
			data = map[string]any{"/vms/1100": map[string]int{"VM.Audit": 1}}
		case strings.HasSuffix(r.URL.Path, "/qemu"):
			data = []map[string]any{{"vmid": 1100, "status": "stopped"}}
		default:
			data = map[string]any{"name": "kombify-home", "tags": "customer-owned"}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	identity := GuestIdentity{ID: 1100, Name: "kombify-home", OperationTag: "kombify-op-" + strings.Repeat("a", 32)}
	if _, err := client.SubmitLifecycle(context.Background(), identity, "delete"); !errors.Is(err, ErrOwnership) {
		t.Fatalf("ownership denial missing: %v", err)
	}
	if mutations != 0 {
		t.Fatal("an observed VM was mutated")
	}
}

func TestNativeClientRefusesRemoteCredentialDestination(t *testing.T) {
	if _, err := NewClient(Config{Endpoint: "https://example.com:8006"}); err == nil {
		t.Fatal("remote Proxmox token destination accepted")
	}
}

// The provider accepted the download but its response vanished. Recovery must
// observe its immutable native task instead of starting another download.
func TestLostImageDownloadResponseRecoversNativeTask(t *testing.T) {
	image, _ := DefaultImage("ubuntu-24.04")
	submitted := false
	calls := 0
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch {
		case r.Method == http.MethodPost:
			submitted = true
			calls++
			connection, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			connection.Close()
			return
		case strings.HasSuffix(r.URL.Path, "/tasks"):
			data = []any{}
			if submitted {
				data = []map[string]any{{"upid": "UPID:pve:download", "type": "download", "id": downloadFilename("local", image), "user": "test@pve!guard"}}
			}
		case strings.HasSuffix(r.URL.Path, "/version"):
			data = map[string]any{"version": "9.0"}
		case strings.HasSuffix(r.URL.Path, "/status"):
			data = map[string]any{"current-kernel": map[string]string{"machine": "x86_64"}, "cpuinfo": map[string]int{"cpus": 8, "hvm": 1}, "memory": map[string]uint64{"available": 32 << 30}}
		case strings.HasSuffix(r.URL.Path, "/storage"):
			data = []map[string]any{{"storage": "local", "content": "import", "active": 1, "avail": 128 << 30}}
		default:
			data = []any{}
		}
		json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	if _, err := client.SubmitImageImport(t.Context(), "local", image); err == nil {
		t.Fatal("ambiguous submission reported success")
	}
	imported, err := client.SubmitImageImport(t.Context(), "local", image)
	if err != nil || imported.TaskRef != "UPID:pve:download" || calls != 1 {
		t.Fatalf("native task recovery failed: %v, submissions=%d", err, calls)
	}
}
