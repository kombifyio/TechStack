package httpguard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Incomplete external observations must never authorize a claim of no impact.
func TestDiscoveryCoverageKeepsPartialEvidenceAndRejectsIncompleteReads(t *testing.T) {
	valid := `{"ID":"c0ffee","Names":"app","Image":"app:1","State":"running","Status":"Up"}`
	for _, test := range []struct {
		name, output     string
		failed, complete bool
	}{
		{"empty_success", "", false, true},
		{"known_service", valid, false, true},
		{"malformed_partial", valid + "\nnot json", false, false},
		{"refused", "", true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			probes := &fakeHostProbes{docker: test.output}
			if test.failed {
				probes.dockerErr = errors.New("probe refused")
			}
			outcome := discoveryWith(t, probes).discover(t.Context())
			if outcome.Complete != test.complete {
				t.Fatalf("complete=%v", outcome.Complete)
			}
			if test.name == "malformed_partial" && len(outcome.Services) == 0 {
				t.Fatal("known affected service discarded on incomplete probe")
			}
		})
	}
	t.Run("verified_absence", func(t *testing.T) {
		outcome := discoveryWith(t, &fakeHostProbes{dockerErr: exec.ErrNotFound}).discover(t.Context())
		if !outcome.Complete {
			t.Fatal("verified absent Docker rejected complete systemd-only discovery")
		}
	})
	t.Run("bounded_partial", func(t *testing.T) {
		probe := discoveryWith(t, &fakeHostProbes{docker: valid + "\n" + `{"ID":"other","Names":"other","Image":"app:1","State":"running","Status":"Up"}`})
		probe.cfg.MaxServices = 1
		outcome := probe.discover(t.Context())
		if outcome.Complete || len(outcome.Services) == 0 {
			t.Fatal("truncation lost known service or claimed complete coverage")
		}
	})
	t.Run("merged_manifest_bound", func(t *testing.T) {
		snapshot := Snapshot{DiscoveryComplete: true}
		for i := 0; i < 150; i++ {
			snapshot.runtimeServices = append(snapshot.runtimeServices, accessManifestRuntimeService{ServiceKey: fmt.Sprintf("managed-%d", i), ApplicationKey: "app"})
		}
		discovered := []Service{}
		for i := 0; i < 100; i++ {
			discovered = append(discovered, Service{Key: fmt.Sprintf("observed-%d", i), Status: "running"})
		}
		merged := reconcileManifestRuntimeServices(snapshot, discovered)
		if merged.DiscoveryComplete || len(merged.Services) == 0 {
			t.Fatal("final merged truncation claimed full coverage or discarded all evidence")
		}
	})
	t.Run("enabled_template_instance", func(t *testing.T) {
		probe := discoveryWith(t, &fakeHostProbes{systemctl: `[{"unit":"app@home.service","active":"active","sub":"running"}]`}, "app@.service")
		dir := probe.cfg.SystemdUnitDir[0]
		wants := filepath.Join(dir, "multi-user.target.wants")
		if err := os.Mkdir(wants, 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(wants, "app@home.service"), []byte("[Unit]\n"), 0600); err != nil {
			t.Fatal(err)
		}
		outcome := probe.discover(t.Context())
		if !outcome.Complete {
			t.Fatal("supported template instance could not establish full discovery")
		}
		serviceByKey(t, outcome.Services, discoveryServiceKey(discoveryPlatformSystemd, "app@home"))
	})
}
