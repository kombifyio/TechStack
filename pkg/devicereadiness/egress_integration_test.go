package devicereadiness

import (
	"context"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// The borrowed route is the one claim in this package that cannot be settled by
// reasoning: either a machine with no way out can install a package through the
// executor, or it cannot. So this test asks a real machine.
//
// It is skipped unless a target is named, because it needs one. Set:
//
//	KOMBIFY_READINESS_TARGET   host:port of a machine reachable over SSH
//	KOMBIFY_READINESS_USER     user to connect as, able to use sudo
//	KOMBIFY_READINESS_KEY      path to the private key
//
// The machine must have no default route, which is the situation being proven.
// A disposable container with its default route deleted is enough.

func integrationTarget(t *testing.T) (Target, Auth) {
	t.Helper()
	address := strings.TrimSpace(os.Getenv("KOMBIFY_READINESS_TARGET"))
	if address == "" {
		t.Skip("no readiness target named; set KOMBIFY_READINESS_TARGET to run this against a real machine")
	}
	host, portText, found := strings.Cut(address, ":")
	port := 22
	if found {
		parsed, err := strconv.Atoi(portText)
		if err != nil {
			t.Fatalf("KOMBIFY_READINESS_TARGET port is not a number: %v", err)
		}
		port = parsed
	}
	key, err := os.ReadFile(os.Getenv("KOMBIFY_READINESS_KEY"))
	if err != nil {
		t.Fatalf("cannot read KOMBIFY_READINESS_KEY: %v", err)
	}
	return Target{Host: host, Port: port, User: os.Getenv("KOMBIFY_READINESS_USER")}, Auth{PrivateKeyPEM: key}
}

func TestBorrowedRouteLetsARoutelessMachineInstallAPackage(t *testing.T) {
	target, auth := integrationTarget(t)
	ctx := context.Background()

	session, err := Connect(ctx, target, auth, HostKeyPolicy{Observe: func(string) error { return nil }})
	if err != nil {
		t.Fatalf("cannot reach the target: %v", err)
	}
	defer func() { _ = session.Close() }()

	facts, err := session.Probe(ctx, ProbeEnvironment{})
	if err != nil {
		t.Fatalf("probe failed: %v", err)
	}
	if facts.Routes.Observed && facts.Routes.DefaultGateway != "" {
		t.Skip("this target still has a default route, so it cannot prove anything about borrowing one")
	}

	egress, err := session.OpenEgress(ctx, DefaultEgressPolicy(""), facts.Proxy.Environment || facts.Proxy.APT)
	if err != nil {
		t.Fatalf("could not lend the machine a route: %v", err)
	}

	// This is the claim: the reported user could not update packages, and this
	// is that same command on a machine with no way out of its own.
	update, err := session.run(ctx, elevate("apt-get update", true), "apt-get update", nil, 4*time.Minute)
	if err != nil {
		t.Fatalf("apt-get update could not be run: %v", err)
	}
	if update.ExitCode != 0 {
		t.Fatalf("apt-get update failed through the borrowed route (exit %d):\n%s\n%s", update.ExitCode, update.Stdout, update.Stderr)
	}

	// The same route must not carry anything else, or it would be a relay
	// rather than a repair.
	outside, err := session.run(ctx,
		"curl -sS -x "+egress.ProxyURL()+" -o /dev/null -w '%{http_code}' https://example.com/ || true",
		"reach a host outside the allowlist", nil, time.Minute)
	if err != nil {
		t.Fatalf("could not test a host outside the allowlist: %v", err)
	}
	if strings.Contains(outside.Stdout, "200") {
		t.Fatalf("the borrowed route carried traffic to a host outside the allowlist: %q", outside.Stdout)
	}

	// Closing must leave nothing behind: a machine still pointing at a route
	// that no longer exists cannot install anything, which is worse than the
	// state it was found in.
	if err := egress.Close(); err != nil {
		t.Fatalf("closing the borrowed route reported a problem: %v", err)
	}
	leftover, err := session.run(ctx, elevate("ls "+shellQuote(aptProxyDropInPath), true), "look for the borrowed-route setting", nil, time.Minute)
	if err != nil {
		t.Fatalf("could not check what was left behind: %v", err)
	}
	if leftover.ExitCode == 0 {
		t.Fatal("the package-manager setting for the borrowed route is still on the machine")
	}
}
