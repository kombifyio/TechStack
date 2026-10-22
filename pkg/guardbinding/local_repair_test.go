package guardbinding

import (
	"encoding/json"
	"testing"
	"time"
)

func localGrantFixture(t *testing.T, issued time.Time) (LocalRepairGrant, *Signer) {
	t.Helper()
	signer, err := DeriveWorkerSigner([]byte("local-repair-test-base"), "tenant-1", "worker-1")
	if err != nil {
		t.Fatal(err)
	}
	grant := LocalRepairGrant{
		ContractVersion: LocalRepairContractVersion, PolicyID: "policy-1", PolicyVersion: 3,
		TenantID: "tenant-1", WorkerID: "worker-1", ServerID: "server-1", Generation: 2,
		Action: LocalRepairAction, Controls: []string{"firewall.default_inbound", "kernel.sysctl"},
		PolicyExpiresAt: issued.Add(24 * time.Hour), IssuedAt: issued,
		Bounds:  LocalRepairBounds{CooldownSeconds: 1800, MaxAttemptsPerDay: 3, BreakerFailures: 3, BreakerOpenSeconds: 21600},
		History: []LocalRepairRecord{},
	}
	if err := signer.SignLocalGrant(&grant); err != nil {
		t.Fatal(err)
	}
	return grant, signer
}

// A grant survives the wire unchanged, and any change to what it authorizes
// (scope, expiry, target) breaks its signature. Without a pinned key there is
// no local authority at all.
func TestLocalGrantSignatureCoversItsAuthority(t *testing.T) {
	issued := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	grant, signer := localGrantFixture(t, issued)
	raw, err := json.Marshal(grant)
	if err != nil {
		t.Fatal(err)
	}
	var wire LocalRepairGrant
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	if denial := wire.VerifySignature(signer.PublicKeys()); denial != nil {
		t.Fatalf("a grant read off the wire did not verify: %v", denial)
	}
	tampered := map[string]func(*LocalRepairGrant){
		"widened scope":    func(g *LocalRepairGrant) { g.Controls = append(g.Controls, "ssh.password_authentication") },
		"extended expiry":  func(g *LocalRepairGrant) { g.PolicyExpiresAt = g.PolicyExpiresAt.Add(time.Hour) },
		"renewed issue":    func(g *LocalRepairGrant) { g.IssuedAt = g.IssuedAt.Add(time.Hour) },
		"other worker":     func(g *LocalRepairGrant) { g.WorkerID = "worker-2" },
		"relaxed attempts": func(g *LocalRepairGrant) { g.Bounds.MaxAttemptsPerDay = 100 },
	}
	for name, mutate := range tampered {
		t.Run(name, func(t *testing.T) {
			changed := wire
			changed.Controls = append([]string(nil), wire.Controls...)
			mutate(&changed)
			if denial := changed.VerifySignature(signer.PublicKeys()); denial == nil {
				t.Fatal("a changed grant still verified")
			}
		})
	}
	if denial := wire.VerifySignature(nil); denial == nil || denial.Code != CodeLocalAuthorityMissing {
		t.Fatalf("a grant was admitted without a pinned key: %v", denial)
	}
}

// Missing authority, a clock that disagrees with the control plane at receipt,
// a restart, a stepped clock and the end of the offline lifetime all leave the
// Guard observe-only.
func TestLocalGrantAdmissionIsBoundedAndObserveOnlyWithoutTrust(t *testing.T) {
	issued := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	grant, signer := localGrantFixture(t, issued)
	expect := Expectation{TenantID: "tenant-1", WorkerID: "worker-1", ServerID: "server-1", HighestGeneration: 2}
	mono := time.Hour
	held, denial := ReceiveLocalGrant(grant, signer.PublicKeys(), expect, issued.Add(time.Second), mono)
	if denial != nil {
		t.Fatalf("a valid grant was refused at receipt: %v", denial)
	}
	state := func(after time.Duration) EffectState {
		return EffectState{Expect: expect, Now: issued.Add(time.Second + after), Mono: mono + after}
	}
	if denial := held.Admit(state(5*time.Minute), []string{"firewall.default_inbound"}); denial != nil {
		t.Fatalf("a covered repair within the offline lifetime was refused: %v", denial)
	}

	var none *HeldLocalGrant
	if denial := none.Admit(state(time.Minute), []string{"firewall.default_inbound"}); denial == nil || denial.Code != CodeLocalAuthorityMissing {
		t.Fatalf("a Guard without a grant was not observe-only: %v", denial)
	}
	if _, denial := ReceiveLocalGrant(grant, signer.PublicKeys(), expect, issued.Add(10*time.Minute), mono); denial == nil || denial.Code != CodeRevalidationRequired {
		t.Fatalf("a grant was received on a clock that disagrees with the control plane: %v", denial)
	}
	if _, denial := ReceiveLocalGrant(grant, signer.PublicKeys(), Expectation{TenantID: "tenant-1", WorkerID: "worker-1", HighestGeneration: 3}, issued, mono); denial == nil || denial.Code != CodeStaleGeneration {
		t.Fatalf("a grant of an older generation was received: %v", denial)
	}
	cases := map[string]struct {
		state    EffectState
		controls []string
		code     string
	}{
		"control outside scope":   {state(time.Minute), []string{"ssh.password_authentication"}, CodeLocalScope},
		"offline lifetime spent":  {state(LocalOfflineLifetime), []string{"firewall.default_inbound"}, CodeAuthorityExpired},
		"restarted process clock": {EffectState{Expect: expect, Now: issued.Add(2 * time.Minute), Mono: time.Minute}, []string{"firewall.default_inbound"}, CodeRevalidationRequired},
		"wall clock set back":     {EffectState{Expect: expect, Now: issued.Add(-10 * time.Minute), Mono: mono + 2*time.Minute}, []string{"firewall.default_inbound"}, CodeRevalidationRequired},
		"revocation observed":     {EffectState{Expect: expect, Revoked: true, Now: issued.Add(time.Minute), Mono: mono + time.Minute}, []string{"firewall.default_inbound"}, CodeAuthorityRevoked},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if denial := held.Admit(tc.state, tc.controls); denial == nil || denial.Code != tc.code {
				t.Fatalf("want %s, got %v", tc.code, denial)
			}
		})
	}
}

// The privileged executor admits only an operation made under the grant's
// policy version, inside its lifetime on its own clock.
func TestLocalGrantExecutorAdmission(t *testing.T) {
	issued := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	grant, signer := localGrantFixture(t, issued)
	operation := grant.NewLocalOperationID("0123456789abcdef")
	if denial := grant.AdmitAtExecutor(signer.PublicKeys(), operation, []string{"kernel.sysctl"}, issued.Add(time.Minute)); denial != nil {
		t.Fatalf("a covered local repair was refused: %v", denial)
	}
	other := grant
	other.PolicyVersion = 2
	cases := map[string]struct {
		operation string
		controls  []string
		now       time.Time
	}{
		"operation of another version": {other.NewLocalOperationID("0123456789abcdef"), []string{"kernel.sysctl"}, issued.Add(time.Minute)},
		"operation without nonce":      {grant.NewLocalOperationID(""), []string{"kernel.sysctl"}, issued.Add(time.Minute)},
		"uncovered control":            {operation, []string{"unattended_upgrades.enabled"}, issued.Add(time.Minute)},
		"past the offline lifetime":    {operation, []string{"kernel.sysctl"}, issued.Add(LocalOfflineLifetime + ResultClockMargin)},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if denial := grant.AdmitAtExecutor(signer.PublicKeys(), tc.operation, tc.controls, tc.now); denial == nil {
				t.Fatal("the executor admitted a local repair outside its grant")
			}
		})
	}
}
