package substrate

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// GPU passthrough writes a hookscript that runs as root on the node before
// every start, and hands a whole PCI device to one guest. What is protected
// here is the provider-control boundary: the guest is created with exactly the
// passthrough configuration the GPU needs, and a device another guest holds or
// that shares its IOMMU group is refused before anything is created.

func gpuNode(t *testing.T, directory string, otherHostpci string, extra ...PCIDevice) (*Client, *url.Values) {
	t.Helper()
	devices := append([]PCIDevice{
		{ID: "0000:08:00.0", Class: "0x030000", Vendor: "0x10de", Device: "0x2503", IOMMUGroup: 7},
		{ID: "0000:08:00.1", Class: "0x040300", Vendor: "0x10de", Device: "0x228e", IOMMUGroup: 7},
		{ID: "0000:66:00.0", Class: "0x030000", Vendor: "0x1002", Device: "0x15bf", IOMMUGroup: 20},
	}, extra...)
	created := &url.Values{}
	client := fixtureClient(t, func(w http.ResponseWriter, r *http.Request) {
		var data any
		switch {
		case r.URL.Path == "/api2/json/storage/local":
			data = map[string]any{"type": "dir", "path": directory, "content": "images,import,snippets"}
		case r.URL.Path == "/api2/json/access/permissions":
			data = map[string]any{"/vms/1100": map[string]int{"VM.Audit": 1}}
		case r.Method == http.MethodPost:
			_ = r.ParseForm()
			*created = r.PostForm
			data = "UPID:pve:create"
		case strings.HasSuffix(r.URL.Path, "/qemu/230/config"):
			data = map[string]any{"name": "local-ai", "hostpci0": otherHostpci}
		case strings.HasSuffix(r.URL.Path, "/qemu"):
			data = []map[string]any{{"vmid": 230, "name": "local-ai", "status": "running"}}
		case strings.HasSuffix(r.URL.Path, "/hardware/pci"):
			data = devices
		case strings.Contains(r.URL.Path, "/tasks/"):
			data = map[string]any{"status": "stopped", "exitstatus": "OK"}
		case strings.HasSuffix(r.URL.Path, "/version"):
			data = map[string]any{"version": "9.2.2"}
		case strings.HasSuffix(r.URL.Path, "/status"):
			data = map[string]any{"current-kernel": map[string]string{"machine": "x86_64"}, "cpuinfo": map[string]int{"cpus": 12, "hvm": 1}, "memory": map[string]uint64{"available": 48 << 30}}
		case strings.HasSuffix(r.URL.Path, "/storage"):
			data = []map[string]any{{"storage": "local", "content": "images,import,snippets", "active": 1, "avail": 512 << 30}}
		case strings.HasSuffix(r.URL.Path, "/network"):
			data = []map[string]any{{"iface": "vmbr0", "type": "bridge", "active": 1}}
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL)
			w.WriteHeader(500)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
	})
	return client, created
}

func gpuSpec(t *testing.T) GuestSpec {
	t.Helper()
	image, _ := DefaultImage("ubuntu-26.04")
	return GuestSpec{
		GuestIdentity: GuestIdentity{ID: 1100, Name: "kombify-local-ai", OperationTag: "kombify-op-" + strings.Repeat("b", 32)},
		ProfileID:     "ubuntu-26.04", SSHPublicKey: "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAITest",
		CPU: 4, MemoryMiB: 8192, DiskGiB: 64, Storage: "local", Bridge: "vmbr0",
		ImageImport: ImageImport{Image: image, Storage: "local", TaskRef: "UPID:pve:import"},
		Accelerator: &PCIPassthrough{Address: "0000:08:00", RootPort: "0000:00:03.1", Subtree: "0000:06:00.0", ThunderboltUniqueID: "be030000-0080-7d08-2326-1917aa211122"},
	}
}

func snippetsDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Mkdir(filepath.Join(directory, "snippets"), 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestAcceleratorGuestIsCreatedWithItsWholeGPUAndHookscript(t *testing.T) {
	directory := snippetsDir(t)
	client, created := gpuNode(t, directory, "0000:01:00,pcie=1")
	spec := gpuSpec(t)

	if _, err := client.SubmitCreate(t.Context(), spec); err != nil {
		t.Fatal(err)
	}

	for key, want := range map[string]string{"hostpci0": "0000:08:00,pcie=1", "balloon": "0", "machine": "q35", "bios": "ovmf"} {
		if got := created.Get(key); got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	if !strings.Contains(created.Get("efidisk0"), "pre-enrolled-keys=0") {
		t.Errorf("efidisk0 = %q: enrolled keys would refuse the unsigned guest driver", created.Get("efidisk0"))
	}
	volume := created.Get("hookscript")
	if !strings.HasPrefix(volume, "local:snippets/") {
		t.Fatalf("hookscript = %q, want a snippet volume", volume)
	}
	path := filepath.Join(directory, "snippets", strings.TrimPrefix(volume, "local:snippets/"))
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("the hookscript the guest names was not written: %v", err)
	}
	if string(body) != AcceleratorHookscript(spec.ID, *spec.Accelerator) {
		t.Fatal("the written hookscript is not the one rendered for this guest's GPU")
	}
}

func TestAcceleratorAnotherGuestHoldsIsRefusedBeforeCreation(t *testing.T) {
	for _, held := range []string{"0000:08:00,pcie=1", "08:00.0;08:00.1", "host=0000:08:00.0,pcie=1"} {
		t.Run(held, func(t *testing.T) {
			client, created := gpuNode(t, snippetsDir(t), held)

			_, err := client.SubmitCreate(t.Context(), gpuSpec(t))

			if !errors.Is(err, ErrConflict) {
				t.Fatalf("a GPU held as %q by guest 230 was not refused: %v", held, err)
			}
			if len(*created) != 0 {
				t.Fatal("a guest was created for a GPU another guest holds")
			}
		})
	}
}

func TestAcceleratorSharingItsIOMMUGroupIsRefusedBeforeCreation(t *testing.T) {
	// Everything in the group would go to the guest with the GPU. A network
	// card in that group would take the node off its own network.
	client, created := gpuNode(t, snippetsDir(t), "", PCIDevice{ID: "0000:09:00.0", Class: "0x020000", IOMMUGroup: 7})

	if _, err := client.SubmitCreate(t.Context(), gpuSpec(t)); err == nil || len(*created) != 0 {
		t.Fatalf("a GPU sharing its IOMMU group was passed through (err=%v)", err)
	}
}
