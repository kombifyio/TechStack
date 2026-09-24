package homeassistant

import (
	"context"
	"encoding/json"
	"github.com/moby/moby/client"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Sensitive source control: inspect the immutable grant before every power
// operation, retain the original container, and refuse drift and device access.
func TestDockerSourceRetainsExactContainerAndRejectsDrift(t *testing.T) {
	id := strings.Repeat("a", 64)
	image := "sha256:" + strings.Repeat("b", 64)
	running, privileged, drift := true, false, false
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/json") {
			name := "/ha"
			if drift {
				name = "/different"
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": id, "Name": name, "Image": image, "Config": map[string]any{"Labels": map[string]string{"owned": "original"}}, "State": map[string]any{"Running": running}, "HostConfig": map[string]any{"Privileged": privileged}})
			return
		}
		if r.Method != "POST" || !strings.Contains(r.URL.Path, id) {
			t.Error("ungranted request")
			w.WriteHeader(403)
			return
		}
		switch {
		case strings.HasSuffix(r.URL.Path, "/stop"):
			running = false
		case strings.HasSuffix(r.URL.Path, "/start"):
			running = true
		default:
			t.Error("ungranted mutation")
			w.WriteHeader(403)
			return
		}
		mutations++
		w.WriteHeader(204)
	}))
	defer server.Close()
	cli, err := client.New(client.WithHost(server.URL), client.WithAPIVersion("1.52"))
	if err != nil {
		t.Fatal(err)
	}
	defer cli.Close()
	d := &DockerSource{cli: cli, grant: DockerSourceGrant{ContainerID: id, Name: "ha", ImageID: image, Labels: map[string]string{"owned": "original"}}}
	ctx := context.Background()
	if done, err := d.Stop(ctx); err != nil || !done {
		t.Fatalf("stop: %v", err)
	}
	if done, err := d.Stop(ctx); err != nil || !done {
		t.Fatalf("stop retry: %v", err)
	}
	if done, err := d.Start(ctx); err != nil || !done {
		t.Fatalf("return: %v", err)
	}
	if mutations != 2 {
		t.Fatal("power retry repeated mutation")
	}
	drift = true
	if _, err := d.Stop(ctx); err == nil || mutations != 2 {
		t.Fatal("drifted source mutated")
	}
	drift = false
	privileged = true
	if err := d.VerifyNoRadios(ctx); err == nil {
		t.Fatal("implicit device access accepted")
	}
}
