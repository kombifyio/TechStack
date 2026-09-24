package devicereadiness_test

import (
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/devicereadiness"
)

// The seam between the shell probe and the evaluation is a cross-language
// contract, and getting it wrong is silent: a field the decoder drops becomes
// an unobserved fact, and an unobserved fact that was actually measured turns a
// blocked device into an unknown one. These tests hold that seam.

func TestDecodeCarriesMeasurementsThroughToTheVerdict(t *testing.T) {
	// A device whose network card has no driver: the probe found the
	// controller on the bus, no usable interface, and nothing reachable.
	raw := `{
	  "schemaVersion": "techstack.device-readiness/v1",
	  "os": {"observed": true, "id": "ubuntu", "versionId": "24.04", "pretty": "Ubuntu 24.04.4 LTS"},
	  "kernel": {"observed": true, "release": "6.8.0-51-generic", "arch": "x86_64", "extraModulesInstalled": false},
	  "interfaces": [{"name": "lo", "operState": "unknown", "virtual": true, "addresses": ["127.0.0.1/8"]}],
	  "unclaimedNics": [{"slot": "0000:02:00.0", "vendorId": "10ec", "deviceId": "8125"}],
	  "routes": {"observed": true, "defaultGateway": "", "defaultDevice": ""},
	  "netplan": {"observed": true, "present": true, "configuredInterfaces": ["enp2s0"]},
	  "resolver": {"observed": true, "servers": ["192.0.2.1"]},
	  "reachability": [{"endpoint": "control-plane", "result": "unreachable"}],
	  "proxy": {"observed": true, "environment": false, "apt": false},
	  "clock": {"observed": true, "deviceEpoch": 1788373100, "synchronized": true},
	  "packages": {"observed": true, "locked": false, "sourcesConfigured": true},
	  "cloudInit": {"observed": true, "present": true, "status": "done"},
	  "hypervisor": {"observed": true},
	  "probeErrors": []
	}`

	facts, err := devicereadiness.DecodeFacts([]byte(raw), time.Unix(1788373100, 0))
	if err != nil {
		t.Fatalf("decoding a well-formed probe payload failed: %v", err)
	}

	report := devicereadiness.Evaluate(facts)
	if got := checkStatus(t, report, devicereadiness.CheckDriver); got != devicereadiness.StatusBlocked {
		t.Fatalf("driver check = %q, want blocked: the unclaimed controller did not survive decoding", got)
	}
	if got := checkStatus(t, report, devicereadiness.CheckOS); got != devicereadiness.StatusPass {
		t.Fatalf("operating-system check = %q, want pass: the measured system did not survive decoding", got)
	}
}

func TestClockSkewIsMeasuredAgainstTheExecutor(t *testing.T) {
	// A device with no time source cannot know it is wrong, so the executor
	// measures the difference rather than asking the device for it.
	raw := `{"schemaVersion":"techstack.device-readiness/v1","clock":{"observed":true,"deviceEpoch":1788330000}}`

	facts, err := devicereadiness.DecodeFacts([]byte(raw), time.Unix(1788373100, 0))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if facts.Clock.SkewSeconds == nil {
		t.Fatal("no skew was measured from a device that reported its time")
	}

	report := devicereadiness.Evaluate(facts)
	if got := checkStatus(t, report, devicereadiness.CheckClock); got != devicereadiness.StatusBlocked {
		t.Fatalf("clock check = %q, want blocked for a device twelve hours out", got)
	}
}

func TestADeviceThatReportedNoTimeGetsNoInventedSkew(t *testing.T) {
	raw := `{"schemaVersion":"techstack.device-readiness/v1","clock":{"observed":false}}`

	facts, err := devicereadiness.DecodeFacts([]byte(raw), time.Unix(1788373100, 0))
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if facts.Clock.SkewSeconds != nil {
		t.Fatal("a skew was invented for a device that reported no time")
	}
}

func TestShellNoiseBeforeThePayloadIsTolerated(t *testing.T) {
	// A login shell prints banners, and a device that works must not be
	// reported as unmeasurable because of one.
	raw := "Welcome to Ubuntu 24.04.4 LTS\n*** System restart required ***\n" +
		`{"schemaVersion":"techstack.device-readiness/v1","os":{"observed":true,"id":"ubuntu","pretty":"Ubuntu"}}`

	facts, err := devicereadiness.DecodeFacts([]byte(raw), time.Now())
	if err != nil {
		t.Fatalf("a banner made a valid probe unreadable: %v", err)
	}
	if !facts.OS.Observed {
		t.Fatal("the payload after the banner was not decoded")
	}
}

func TestAProbeThatProducedNothingIsAnErrorNotACleanReport(t *testing.T) {
	// The caller has to be able to tell "the device is in a bad state" from
	// "we failed to look at it". Returning empty facts would make an unread
	// device indistinguishable from an unmeasurable one.
	for _, raw := range []string{"", "   \n", "connection closed by remote host"} {
		if _, err := devicereadiness.DecodeFacts([]byte(raw), time.Now()); err == nil {
			t.Fatalf("decoding %q succeeded; a failed probe must not read as facts", raw)
		}
	}
}

func TestProbeEnvironmentRefusesUnsafeEndpoints(t *testing.T) {
	// The probe command is built for a device that is not trusted yet, so an
	// endpoint is either obviously safe to interpolate into a shell or it is
	// not sent at all.
	env := devicereadiness.ProbeEnvironment{
		ControlPlaneHost:  "techstack.example.com:443",
		PackageMirrorHost: "archive.ubuntu.com; rm -rf /",
		ImageRegistryHost: "ghcr.io",
	}

	command := env.Command()

	if !strings.Contains(command, "techstack.example.com:443") {
		t.Fatal("a safe endpoint was dropped")
	}
	if strings.Contains(command, "rm -rf") {
		t.Fatalf("an unsafe endpoint reached the command line: %q", command)
	}
}
