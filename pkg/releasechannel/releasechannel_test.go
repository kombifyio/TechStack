package releasechannel

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/config"
)

// No tenant may be worked by both channels: stable reads Cloud and hands
// people over, Preview acts only on a fresh hand-over, and stable takes a
// person back only HandBack after they left its list or after its own boot.
func TestHandOverNeverLetsBothChannelsWorkOnePerson(t *testing.T) {
	clock := time.Unix(1_800_000_000, 0)
	cloud := []string{}
	cloudDown := false
	newStable := func() *Directory {
		d := NewDirectory(func(context.Context) ([]string, bool, error) {
			if cloudDown {
				return nil, false, errors.New("cloud unavailable")
			}
			return append([]string(nil), cloud...), true, nil
		})
		d.now, d.bootedAt = func() time.Time { return clock }, clock
		return d
	}
	stable := newStable()
	previewCutOff := false
	preview := NewDirectory(func(context.Context) ([]string, bool, error) {
		if previewCutOff {
			return nil, false, errors.New("stable unreachable")
		}
		subjects, ok := stable.Honored()
		if !ok {
			return nil, false, errors.New("stable has no list yet")
		}
		return subjects, true, nil
	})
	preview.now = func() time.Time { return clock }

	people := []string{"auth0|alice", "auth0|bob"}
	step := func(stage string, d time.Duration) {
		clock = clock.Add(d)
		_ = stable.Refresh(context.Background())
		_ = preview.Refresh(context.Background())
		for _, person := range people {
			if stable.owns(person, false) && preview.owns(person, true) {
				t.Fatalf("%s: %s is worked by both channels", stage, person)
			}
		}
	}
	owner := func(person string) string {
		switch {
		case stable.owns(person, false):
			return "stable"
		case preview.owns(person, true):
			return "preview"
		}
		return "none"
	}

	step("boot", 0)
	if owner("auth0|bob") != "none" {
		t.Fatal("stable worked a personal tenant during its boot hand-back")
	}
	step("after boot", HandBack)
	if owner("auth0|bob") != "stable" {
		t.Fatal("stable must own everyone who did not choose Preview")
	}

	cloud = []string{"auth0|alice"}
	step("alice chose Preview", 15*time.Second)
	if owner("auth0|alice") != "none" {
		t.Fatal("stable hands a person over only after Settle")
	}
	step("alice settled", Settle)
	if owner("auth0|alice") != "preview" || owner("auth0|bob") != "stable" {
		t.Fatalf("after hand-over: alice=%s bob=%s", owner("auth0|alice"), owner("auth0|bob"))
	}

	cloudDown = true
	step("cloud outage", 10*time.Minute)
	if owner("auth0|alice") != "preview" {
		t.Fatal("stable's last honored list must keep holding during a Cloud outage")
	}
	cloudDown = false

	// Preview cannot reach stable while alice switches back: it keeps acting
	// on its last hand-over until FreshFor, and stable waits longer.
	previewCutOff = true
	cloud = []string{}
	for elapsed := time.Duration(0); elapsed < HandBack+time.Minute; elapsed += 15 * time.Second {
		step("alice back to Stable", 15*time.Second)
	}
	previewCutOff = false
	if owner("auth0|alice") != "stable" {
		t.Fatal("stable must take alice back after HandBack")
	}

	cloud = []string{"auth0|alice"}
	step("alice chose Preview again", 15*time.Second)
	stable = newStable() // a stable restart forgets its list and its hand-backs
	for elapsed := time.Duration(0); elapsed < HandBack+time.Minute; elapsed += 15 * time.Second {
		step("stable restarted", 15*time.Second)
	}
	if owner("auth0|alice") != "preview" || owner("auth0|bob") != "stable" {
		t.Fatalf("after restart: alice=%s bob=%s", owner("auth0|alice"), owner("auth0|bob"))
	}
}

func TestProcessDefaultsWithoutDirectory(t *testing.T) {
	t.Cleanup(func() { Install(nil) })
	Install(nil)
	t.Setenv(config.EnvReleaseChannel, "stable")
	if !OwnsTenant("usr:auth0|alice") || !OwnsTenant("org_shared") || !Settled() {
		t.Fatal("stable without a directory must own every tenant")
	}
	t.Setenv(config.EnvReleaseChannel, "preview")
	if OwnsTenant("usr:auth0|alice") || OwnsTenant("org_shared") {
		t.Fatal("Preview without a directory must own no tenant")
	}
	if TenantSubject("usr:auth0|alice") != "auth0|alice" || TenantSubject("auth0|alice") != "auth0|alice" || TenantSubject("org_shared") != "" {
		t.Fatal("tenant owner resolution changed")
	}
}
