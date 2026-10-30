package guardbinding

import (
	"encoding/json"
	"testing"
	"time"
)

func predecessorFixture(t *testing.T, issued time.Time) (PredecessorRecovery, *Signer, Expectation) {
	t.Helper()
	signer, err := DeriveWorkerSigner([]byte("predecessor-test-base"), "tenant-1", "worker-new")
	if err != nil {
		t.Fatal(err)
	}
	recovery := NewPredecessorRecovery("tenant-1", "worker-new", "server-1",
		[]PredecessorCommand{{CommandID: "cmd-a", WorkerID: "worker-old"}}, issued)
	if err := signer.SignPredecessorRecovery(&recovery); err != nil {
		t.Fatal(err)
	}
	return recovery, signer, Expectation{TenantID: "tenant-1", WorkerID: "worker-new", ServerID: "server-1"}
}

// The statement admits only the exact prior command of the same tenant and
// server, only for the worker it was signed for, and only while it lives.
func TestPredecessorRecoveryAdmitsOnlyItsExactScope(t *testing.T) {
	issued := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	recovery, signer, expect := predecessorFixture(t, issued)
	pins := signer.PublicKeys()
	raw, err := json.Marshal(recovery)
	if err != nil {
		t.Fatal(err)
	}
	var wire PredecessorRecovery
	if err := json.Unmarshal(raw, &wire); err != nil {
		t.Fatal(err)
	}
	now := issued.Add(time.Minute)
	if denial := wire.AdmitDelivery(pins, expect, now, "cmd-a", "worker-old"); denial != nil {
		t.Fatalf("exact scope denied: %v", denial)
	}
	for name, deliver := range map[string]func() *Denial{
		"other command":      func() *Denial { return wire.AdmitDelivery(pins, expect, now, "cmd-b", "worker-old") },
		"other prior worker": func() *Denial { return wire.AdmitDelivery(pins, expect, now, "cmd-a", "worker-x") },
		"other tenant": func() *Denial {
			e := expect
			e.TenantID = "tenant-2"
			return wire.AdmitDelivery(pins, e, now, "cmd-a", "worker-old")
		},
		"other server": func() *Denial {
			e := expect
			e.ServerID = "server-2"
			return wire.AdmitDelivery(pins, e, now, "cmd-a", "worker-old")
		},
		"other current worker": func() *Denial {
			e := expect
			e.WorkerID = "worker-old"
			return wire.AdmitDelivery(pins, e, now, "cmd-a", "worker-old")
		},
		"expired": func() *Denial {
			return wire.AdmitDelivery(pins, expect, issued.Add(PredecessorRecoveryLifetime), "cmd-a", "worker-old")
		},
		"no pins": func() *Denial { return wire.AdmitDelivery(nil, expect, now, "cmd-a", "worker-old") },
	} {
		if deliver() == nil {
			t.Errorf("%s: delivery admitted", name)
		}
	}
}

// Any change to what the statement admits breaks its signature, and another
// worker's key cannot vouch for it.
func TestPredecessorRecoverySignatureCoversItsScope(t *testing.T) {
	issued := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	recovery, signer, expect := predecessorFixture(t, issued)
	now := issued.Add(time.Minute)
	widened := recovery
	widened.Commands = append([]PredecessorCommand{{CommandID: "cmd-z", WorkerID: "worker-old"}}, recovery.Commands...)
	if widened.Verify(signer.PublicKeys(), expect, now) == nil {
		t.Fatal("a widened command list still verifies")
	}
	extended := recovery
	extended.ExpiresAt = recovery.ExpiresAt.Add(time.Hour)
	if extended.Verify(signer.PublicKeys(), expect, now) == nil {
		t.Fatal("an extended expiry still verifies")
	}
	unsigned := recovery
	unsigned.Signature = ""
	if unsigned.Verify(signer.PublicKeys(), expect, now) == nil {
		t.Fatal("an unsigned statement verifies")
	}
	other, err := DeriveWorkerSigner([]byte("predecessor-test-base"), "tenant-1", "worker-other")
	if err != nil {
		t.Fatal(err)
	}
	if recovery.Verify(other.PublicKeys(), expect, now) == nil {
		t.Fatal("a statement verifies under another worker's pinned key")
	}
}

// A statement cannot name the current worker's own command, repeat a command,
// or carry a lifetime longer than the contract, even when correctly signed.
func TestPredecessorRecoveryRefusesMalformedScope(t *testing.T) {
	issued := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	signer, err := DeriveWorkerSigner([]byte("predecessor-test-base"), "tenant-1", "worker-new")
	if err != nil {
		t.Fatal(err)
	}
	expect := Expectation{TenantID: "tenant-1", WorkerID: "worker-new", ServerID: "server-1"}
	cases := map[string]func(*PredecessorRecovery){
		"own command": func(r *PredecessorRecovery) {
			r.Commands = []PredecessorCommand{{CommandID: "cmd-a", WorkerID: "worker-new"}}
		},
		"repeated":      func(r *PredecessorRecovery) { r.Commands = append(r.Commands, r.Commands[0]) },
		"empty":         func(r *PredecessorRecovery) { r.Commands = nil },
		"long lifetime": func(r *PredecessorRecovery) { r.ExpiresAt = r.IssuedAt.Add(PredecessorRecoveryLifetime + time.Second) },
		"no server":     func(r *PredecessorRecovery) { r.ServerID = "" },
	}
	for name, mutate := range cases {
		recovery := NewPredecessorRecovery("tenant-1", "worker-new", "server-1",
			[]PredecessorCommand{{CommandID: "cmd-a", WorkerID: "worker-old"}}, issued)
		mutate(&recovery)
		if err := signer.SignPredecessorRecovery(&recovery); err != nil {
			t.Fatal(err)
		}
		e := expect
		if name == "no server" {
			e.ServerID = ""
		}
		if recovery.Verify(signer.PublicKeys(), e, issued.Add(time.Minute)) == nil {
			t.Errorf("%s: statement verifies", name)
		}
	}
}
