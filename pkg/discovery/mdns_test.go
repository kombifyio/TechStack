package discovery

import (
	"context"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/grandcat/zeroconf"
)

func TestListenMDNS_FindLocalService(t *testing.T) {
	if runtime.GOOS == "windows" ||
		os.Getenv("GITHUB_ACTIONS") == "true" ||
		os.Getenv("CIRCLECI") == "true" ||
		os.Getenv("KOMBIFY_TEST_IN_DOCKER") == "1" {
		t.Skip("skipping mDNS test in CI/headless environments without reliable multicast loopback")
	}

	// Start a local zeroconf service for the test
	server, err := zeroconf.Register("ks-mdns-test", "_workstation._tcp", "local.", 9988, []string{"test=1"}, nil)
	if err != nil {
		t.Fatalf("failed to register test mdns service: %v", err)
	}
	defer server.Shutdown()

	// give the service a short moment to register on the local mDNS responder
	time.Sleep(200 * time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Second)
	defer cancel()

	found := make(chan bool, 1)
	err = ListenMDNS(ctx, func(e *zeroconf.ServiceEntry) {
		if e == nil {
			return
		}
		if e.Instance == "ks-mdns-test" {
			found <- true
		}
	})
	if err != nil {
		t.Fatalf("ListenMDNS returned error: %v", err)
	}

	select {
	case <-found:
		// success
	case <-ctx.Done():
		t.Fatal("did not discover the test mdns service in time")
	}
}
