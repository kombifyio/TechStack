package devicereadiness_test

import (
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/devicereadiness"
)

// The behaviours protected here are the ones the whole capability rests on: an
// unobserved fact must never read as healthy, a device that is only repairable
// must not be offered as ready, and a hypervisor must not be routed into host
// repair. Everything else about a report is presentation and is free to change.

func healthyFacts() devicereadiness.Facts {
	yes := true
	skew := 0.4
	return devicereadiness.Facts{
		SchemaVersion: devicereadiness.SchemaVersion,
		ObservedAt:    time.Now().UTC(),
		OS:            devicereadiness.OSFacts{Observed: true, ID: "ubuntu", VersionID: "24.04", Pretty: "Ubuntu 24.04.4 LTS"},
		Kernel:        devicereadiness.KernelFacts{Observed: true, Release: "6.8.0-51-generic", Arch: "x86_64"},
		Interface: []devicereadiness.NetInterface{
			{Name: "lo", OperState: "unknown", Virtual: true, Addresses: []string{"127.0.0.1/8"}},
			{Name: "enp3s0", OperState: "up", Driver: "e1000e", Addresses: []string{"192.0.2.10/24"}},
		},
		Routes:   devicereadiness.RouteFacts{Observed: true, DefaultGateway: "192.0.2.1", DefaultDevice: "enp3s0"},
		Netplan:  devicereadiness.NetplanFacts{Observed: true, Present: true, Files: []string{"/etc/netplan/50-cloud-init.yaml"}, Renderers: []string{"networkd"}, ConfiguredInterfaces: []string{"enp3s0"}, RendererAvailable: &yes},
		Resolver: devicereadiness.ResolverFacts{Observed: true, Servers: []string{"192.0.2.1"}},
		Reachability: []devicereadiness.ReachProbe{
			{Endpoint: "control-plane", Result: devicereadiness.ReachOK},
			{Endpoint: "package-mirror", Result: devicereadiness.ReachOK},
			{Endpoint: "image-registry", Result: devicereadiness.ReachOK},
		},
		Clock:      devicereadiness.ClockFacts{Observed: true, Synchronized: &yes, SkewSeconds: &skew},
		Packages:   devicereadiness.PackageFacts{Observed: true, SourcesConfigured: &yes},
		CloudInit:  devicereadiness.CloudInitFacts{Observed: true, Present: true, Status: "done"},
		Hypervisor: devicereadiness.HypervisorFacts{Observed: true},
	}
}

func checkStatus(t *testing.T, report devicereadiness.Report, id string) devicereadiness.Status {
	t.Helper()
	for _, check := range report.Checks {
		if check.ID == id {
			return check.Status
		}
	}
	t.Fatalf("report carries no check %q", id)
	return ""
}

func TestHealthyDeviceIsReady(t *testing.T) {
	report := devicereadiness.Evaluate(healthyFacts())

	if report.Readiness != devicereadiness.ReadinessReady {
		t.Fatalf("healthy device: readiness = %q, want ready", report.Readiness)
	}
	if len(report.Blocking()) != 0 {
		t.Fatalf("healthy device reports blocking checks: %v", report.Blocking())
	}
}

func TestUnobservedFactsNeverPass(t *testing.T) {
	// A probe that reached the device but could measure nothing must not
	// produce a report that looks clean. This is the rule the whole capability
	// depends on, because a silently failed probe would otherwise admit a
	// device nobody measured.
	report := devicereadiness.Evaluate(devicereadiness.Facts{SchemaVersion: devicereadiness.SchemaVersion})

	if report.Readiness == devicereadiness.ReadinessReady {
		t.Fatal("an unmeasured device was reported as ready")
	}
	for _, check := range report.Checks {
		if check.Status == devicereadiness.StatusPass {
			t.Fatalf("check %q passed without any observation", check.ID)
		}
	}
}

func TestUnclaimedControllerBlocksAndOffersAnApplicableFix(t *testing.T) {
	facts := healthyFacts()
	// The reported failure: the controller is on the bus, no driver bound, so
	// the device has no interface to configure and no way out.
	facts.Interface = []devicereadiness.NetInterface{{Name: "lo", OperState: "unknown", Virtual: true}}
	facts.UnclaimedNICs = []devicereadiness.NetController{{Slot: "0000:02:00.0", VendorID: "10ec", DeviceID: "8125", Description: "Realtek 2.5GbE Controller"}}
	facts.Routes = devicereadiness.RouteFacts{Observed: true}
	facts.Reachability = []devicereadiness.ReachProbe{
		{Endpoint: "control-plane", Result: devicereadiness.ReachUnreachable},
		{Endpoint: "package-mirror", Result: devicereadiness.ReachUnreachable},
		{Endpoint: "image-registry", Result: devicereadiness.ReachUnreachable},
	}

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckDriver); got != devicereadiness.StatusBlocked {
		t.Fatalf("driver check = %q, want blocked", got)
	}
	if report.Readiness != devicereadiness.ReadinessRepairable {
		t.Fatalf("readiness = %q, want repairable", report.Readiness)
	}
	if !offersApplyFor(report, devicereadiness.CheckDriver) {
		t.Fatal("a blocked driver check offered no fix that can be carried out")
	}
}

func TestDeclaredConfigNamingAMissingInterfaceBlocks(t *testing.T) {
	facts := healthyFacts()
	// Interface names change with hardware and firmware; the declaration was
	// written for a name this device no longer has, so it configures nothing.
	facts.Netplan.ConfiguredInterfaces = []string{"eth0"}
	facts.Interface = []devicereadiness.NetInterface{
		{Name: "lo", OperState: "unknown", Virtual: true},
		{Name: "enp3s0", OperState: "up", Driver: "e1000e"},
	}
	facts.Routes = devicereadiness.RouteFacts{Observed: true}

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckNetplan); got != devicereadiness.StatusBlocked {
		t.Fatalf("declared-config check = %q, want blocked", got)
	}
	if report.Readiness != devicereadiness.ReadinessRepairable {
		t.Fatalf("readiness = %q, want repairable", report.Readiness)
	}
}

func TestWildcardMatchIsNotTreatedAsAMissingInterface(t *testing.T) {
	// A pattern that selects nothing is indistinguishable here from one that
	// selects everything. Guessing would turn a working device into a blocked
	// one, so a pattern is never counted as orphaned.
	facts := healthyFacts()
	facts.Netplan.ConfiguredInterfaces = []string{"en*"}

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckNetplan); got != devicereadiness.StatusPass {
		t.Fatalf("declared-config check = %q, want pass for a wildcard match", got)
	}
}

func TestLargeClockSkewBlocks(t *testing.T) {
	// A device this far out cannot complete a TLS handshake, and every other
	// symptom then reads as a network fault. Naming it is the whole point.
	facts := healthyFacts()
	skew := -4200.0
	facts.Clock.SkewSeconds = &skew
	facts.Reachability = []devicereadiness.ReachProbe{
		{Endpoint: "control-plane", Result: devicereadiness.ReachTLSFailed},
		{Endpoint: "package-mirror", Result: devicereadiness.ReachTLSFailed},
		{Endpoint: "image-registry", Result: devicereadiness.ReachTLSFailed},
	}

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckClock); got != devicereadiness.StatusBlocked {
		t.Fatalf("clock check = %q, want blocked", got)
	}
	if !offersApplyFor(report, devicereadiness.CheckClock) {
		t.Fatal("a blocked clock offered no fix that can be carried out")
	}
}

func TestHypervisorIsRoutedToSubstrateNotToRepair(t *testing.T) {
	facts := healthyFacts()
	facts.Hypervisor = devicereadiness.HypervisorFacts{Observed: true, Kind: "proxmox", Version: "9.2.2", Bridges: []string{"vmbr0"}}

	report := devicereadiness.Evaluate(facts)

	if report.Readiness != devicereadiness.ReadinessSubstrate {
		t.Fatalf("readiness = %q, want substrate", report.Readiness)
	}
}

func TestBlockingWithoutAnApplicableFixNeedsTheOwner(t *testing.T) {
	// A device with no package sources is fully reachable and perfectly
	// healthy on the network, and still cannot be enrolled. Nothing in the
	// catalog may claim to fix it: where a machine's software comes from is
	// the owner's decision, so the verdict has to say so rather than offer a
	// repair that would only look like progress.
	facts := healthyFacts()
	sourcesMissing := false
	facts.Packages.SourcesConfigured = &sourcesMissing

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckPackages); got != devicereadiness.StatusBlocked {
		t.Fatalf("package-manager check = %q, want blocked", got)
	}
	if report.Readiness != devicereadiness.ReadinessNeedsOwner {
		t.Fatalf("readiness = %q, want needs_owner", report.Readiness)
	}
}

func TestDownLinkStillCountsAsRepairable(t *testing.T) {
	// An interface with a driver bound that nobody ever brought up is the
	// commonest version of "no network", and bringing it up is a real action
	// worth offering. The verdict is repairable even though the same symptom
	// can also mean a cable, which the catalog says as well.
	facts := healthyFacts()
	facts.Interface = []devicereadiness.NetInterface{
		{Name: "lo", OperState: "unknown", Virtual: true},
		{Name: "enp3s0", OperState: "down", Driver: "e1000e"},
	}
	facts.Routes = devicereadiness.RouteFacts{Observed: true}
	facts.Reachability = []devicereadiness.ReachProbe{
		{Endpoint: "control-plane", Result: devicereadiness.ReachUnreachable},
		{Endpoint: "package-mirror", Result: devicereadiness.ReachUnreachable},
		{Endpoint: "image-registry", Result: devicereadiness.ReachUnreachable},
	}

	report := devicereadiness.Evaluate(facts)

	if report.Readiness != devicereadiness.ReadinessRepairable {
		t.Fatalf("readiness = %q, want repairable", report.Readiness)
	}
	if !offersApplyFor(report, devicereadiness.CheckLink) {
		t.Fatal("a down link offered no fix that can be carried out")
	}
}

func TestEveryResolutionBindsToARealCheckAndDeclaresItsRisk(t *testing.T) {
	// The catalog is the executor's entire authority against an unenrolled
	// device, so each entry must name the check that justifies it and must not
	// claim it is safe to run unattended while needing a reboot, a way out, or
	// a decision.
	known := map[string]bool{}
	for _, check := range devicereadiness.Evaluate(devicereadiness.Facts{}).Checks {
		known[check.ID] = true
	}

	for _, resolution := range devicereadiness.Resolutions() {
		if !known[resolution.AppliesTo] {
			t.Errorf("resolution %q binds to unknown check %q", resolution.ID, resolution.AppliesTo)
		}
		if resolution.Mode == devicereadiness.ModeHint && (len(resolution.Files) > 0 || len(resolution.Commands) > 0) {
			t.Errorf("resolution %q is advice but carries changes", resolution.ID)
		}
		if resolution.Mode == devicereadiness.ModeApply && len(resolution.Files) == 0 && len(resolution.Commands) == 0 {
			t.Errorf("resolution %q claims it can be carried out but changes nothing", resolution.ID)
		}
		if resolution.AutoEligible {
			if !resolution.Reversible || resolution.RequiresReboot || resolution.RequiresEgress || resolution.Mode != devicereadiness.ModeApply {
				t.Errorf("resolution %q offers itself for unattended use but is not safe unattended", resolution.ID)
			}
		}
	}
}

func offersApplyFor(report devicereadiness.Report, checkID string) bool {
	for _, resolution := range report.Resolutions {
		if resolution.AppliesTo == checkID && resolution.Mode == devicereadiness.ModeApply {
			return true
		}
	}
	return false
}
