package devicereadiness_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/devicereadiness"
)

// The microarchitecture level decides whether a released module can run on a
// node at all (a module built for x86-64-v3 does not start on a v2 CPU), so the
// flag rules are held exactly.
func TestAMD64LevelFollowsTheABIFlagRules(t *testing.T) {
	v1 := []string{"lm", "sse2", "fpu"}
	v2 := append(append([]string{}, v1...), "cx16", "lahf_lm", "popcnt", "sse4_1", "sse4_2", "ssse3")
	v3 := append(append([]string{}, v2...), "avx", "avx2", "bmi1", "bmi2", "f16c", "fma", "abm", "movbe", "xsave")
	v4 := append(append([]string{}, v3...), "avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl")

	for name, tc := range map[string]struct {
		arch  string
		flags []string
		want  int
	}{
		"baseline x86-64":                   {"x86_64", v1, 1},
		"v2":                                {"x86_64", v2, 2},
		"v3":                                {"x86_64", v3, 3},
		"v4":                                {"x86_64", v4, 4},
		"avx2 without lzcnt stays at v2":    {"x86_64", without(v3, "abm"), 2},
		"one v2 flag missing stays at v1":   {"x86_64", without(v2, "popcnt"), 1},
		"avx512 without v3 does not skip":   {"x86_64", append(append([]string{}, v2...), "avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl"), 2},
		"no long mode is no reading":        {"x86_64", without(v3, "lm"), 0},
		"no flags is no reading":            {"x86_64", nil, 0},
		"another architecture has no level": {"aarch64", v3, 0},
		"flag names are case insensitive":   {"x86_64", upper(v3), 3},
	} {
		if got := devicereadiness.AMD64Level(tc.arch, tc.flags); got != tc.want {
			t.Errorf("%s: level = %d, want %d", name, got, tc.want)
		}
	}
}

func without(flags []string, drop string) []string {
	out := make([]string, 0, len(flags))
	for _, flag := range flags {
		if flag != drop {
			out = append(out, flag)
		}
	}
	return out
}

func upper(flags []string) []string {
	out := make([]string, len(flags))
	for i, flag := range flags {
		out[i] = strings.ToUpper(flag)
	}
	return out
}

const miniPCHardware = `{
  "schemaVersion": "techstack.device-readiness/v1",
  "hardware": {
    "observed": true,
    "cpu": {"arch": "x86_64", "cores": 4, "flags": ["lm","cx16","lahf_lm","popcnt","sse4_1","sse4_2","ssse3","avx","avx2","bmi1","bmi2","f16c","fma","abm","movbe","xsave"]},
    "gpus": [
      {"slot": "0000:00:02.0", "vendorId": "8086", "driver": "i915"},
      {"slot": "0000:01:00.0", "vendorId": "10de", "driver": "nvidia"},
      {"slot": "0000:07:00.0", "vendorId": "1a03", "driver": "ast"}
    ],
    "nvidiaDriverVersion": "550.54.14",
    "renderNodes": [{"name": "renderD128", "vendorId": "8086", "driver": "i915"}],
    "systemDisk": {"device": "nvme0n1", "rotational": false},
    "chassisType": 35
  },
  "probeErrors": []
}`

// The seam between the shell probe and the control plane: what the probe read
// has to come out as classified facts with their provenance, and the facts the
// machine did not expose have to stay out.
func TestHardwareObservationClassifiesWhatTheProbeRead(t *testing.T) {
	observedAt := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	facts, err := devicereadiness.DecodeFacts([]byte(miniPCHardware), observedAt)
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	observation := facts.Hardware.Observation(observedAt)
	if observation == nil {
		t.Fatal("a probe that read hardware produced no observation")
	}
	if observation.AMD64Level != 3 {
		t.Errorf("amd64 level = %d, want 3", observation.AMD64Level)
	}
	if gpu := observation.GPU; gpu == nil || gpu.Vendor != "nvidia" || gpu.Driver != "nvidia" || gpu.DriverMajor != 550 {
		t.Errorf("gpu = %+v, want the discrete nvidia card with driver major 550 (not the iGPU, not the BMC)", gpu)
	}
	if media := observation.Media; media == nil || media.Class != "hardware-transcode" || media.Source != "guard-probe:sysfs" {
		t.Errorf("media = %+v, want hardware-transcode from the probe", media)
	}
	if disk := observation.DiskClass; disk == nil || disk.Class != "nvme" || disk.Source != "guard-probe:sysfs" {
		t.Errorf("disk class = %+v, want nvme from the probe", disk)
	}
	if power := observation.PowerClass; power == nil || power.Class != "low" || power.Source != "estimate:dmi-chassis" {
		t.Errorf("power class = %+v, want a low estimate labelled as such", power)
	}
}

// A fact the machine does not expose is absent, never zero and never guessed.
func TestHardwareObservationLeavesUnreadableFactsOut(t *testing.T) {
	const vps = `{
	  "schemaVersion": "techstack.device-readiness/v1",
	  "hardware": {
	    "observed": true,
	    "cpu": {"arch": "aarch64", "cores": 2},
	    "gpus": [{"slot": "0000:00:01.0", "vendorId": "1af4", "driver": "virtio-pci"}],
	    "systemDisk": {"device": "vda", "rotational": true},
	    "chassisType": 1
	  }
	}`
	facts, err := devicereadiness.DecodeFacts([]byte(vps), time.Now())
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	if observation := facts.Hardware.Observation(time.Now()); observation != nil {
		t.Fatalf("a virtual machine exposing nothing classifiable produced facts: %+v", observation)
	}

	// Nouveau is an nvidia GPU without a version of its own: the vendor is
	// known, the driver major is not.
	const nouveau = `{"schemaVersion":"techstack.device-readiness/v1","hardware":{"observed":true,
	  "cpu":{"arch":"x86_64","cores":8},
	  "gpus":[{"slot":"0000:01:00.0","vendorId":"10de","driver":"nouveau"}],
	  "systemDisk":{"device":"sda","rotational":true},"chassisType":3}}`
	facts, err = devicereadiness.DecodeFacts([]byte(nouveau), time.Now())
	if err != nil {
		t.Fatalf("decode failed: %v", err)
	}
	observation := facts.Hardware.Observation(time.Now())
	if observation == nil || observation.GPU == nil || observation.GPU.Vendor != "nvidia" || observation.GPU.DriverMajor != 0 {
		t.Fatalf("gpu = %+v, want nvidia with no driver major", observation)
	}
	if observation.AMD64Level != 0 {
		t.Errorf("a CPU that exposed no flags was given level %d", observation.AMD64Level)
	}
	if observation.DiskClass == nil || observation.DiskClass.Class != "hdd" || observation.PowerClass == nil || observation.PowerClass.Class != "medium" {
		t.Errorf("disk %+v power %+v, want hdd and a medium desktop estimate", observation.DiskClass, observation.PowerClass)
	}
}

// The hardware scope has to print valid JSON on any machine, including one
// where none of the files exist: the Guard parses it blind every few minutes.
func TestHardwareProbeScopePrintsDecodableFacts(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no POSIX shell on this host")
	}
	command := exec.CommandContext(context.Background(), "sh", "-s")
	command.Env = append(command.Environ(), "KOMBIFY_PROBE_SCOPE=hardware")
	command.Stdin = strings.NewReader(devicereadiness.ProbeScript)
	output, err := command.Output()
	if err != nil && len(output) == 0 {
		t.Fatalf("the hardware probe did not run: %v", err)
	}
	if _, err := devicereadiness.DecodeFacts(output, time.Now()); err != nil {
		t.Fatalf("the hardware probe printed output the decoder rejects: %v\n%s", err, output)
	}
}
