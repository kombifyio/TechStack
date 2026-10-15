package httpguard

import "testing"

// A user-run container the manifest never declared is reported as its own
// recognized application, with the one address its published ports prove and
// the version its image label states.
func TestDiscoveredContainerGetsItsApplicationAddressAndVersion(t *testing.T) {
	service, ok := dockerContainerService(dockerContainer{
		ID: "abc", Names: "hermes-webui", Image: "ghcr.io/nesquena/hermes-webui:latest", State: "running",
		Labels: "org.opencontainers.image.version=1.4.2,maintainer=someone",
		Ports:  "0.0.0.0:3000->3000/tcp, :::3000->3000/tcp, 0.0.0.0:8787->8787/tcp, 9999/tcp, 0.0.0.0:5353->5353/udp",
	})
	if !ok {
		t.Fatal("container was dropped")
	}
	service = finalizeDiscoveredService(service, Host{PrivateIP: "192.168.1.20"})
	if service.ApplicationKey != "hermes" || service.ApplicationDisplayName != "Hermes Agent" {
		t.Fatalf("application = %q / %q", service.ApplicationKey, service.ApplicationDisplayName)
	}
	if service.InternalAddress != "http://192.168.1.20:8787" {
		t.Fatalf("internal address = %q, want the catalog web port mapping", service.InternalAddress)
	}
	if service.ImageVersion != "1.4.2" {
		t.Fatalf("image version = %q", service.ImageVersion)
	}

	unexposed, _ := dockerContainerService(dockerContainer{ID: "def", Names: "worker", Image: "acme/worker:3", State: "running", Ports: "9999/tcp"})
	if address := finalizeDiscoveredService(unexposed, Host{PrivateIP: "192.168.1.20"}).InternalAddress; address != "" {
		t.Fatalf("a port that is not published must not become an address: %q", address)
	}
}
