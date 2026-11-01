package jobs

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

// Sensitive crypto boundary (platform-cesjv.2): the managed-target host-key
// trust rule. A pin retires only through the database's provider-absence
// record; nothing else turns a changed key into a trusted one.

type fakeHostKeyPins struct {
	pins     map[string]string
	retired  map[string]string
	bindings map[string]RuntimeHostKeyBinding
	lookup   error
	pinned   int
}

func newFakeHostKeyPins() *fakeHostKeyPins {
	return &fakeHostKeyPins{pins: map[string]string{}, retired: map[string]string{}, bindings: map[string]RuntimeHostKeyBinding{}}
}

func (f *fakeHostKeyPins) LoadPins(context.Context) (map[string]string, error) {
	out := map[string]string{}
	for address, fingerprint := range f.pins {
		out[address] = fingerprint
	}
	return out, nil
}

func (f *fakeHostKeyPins) LookupPin(_ context.Context, address string) (string, bool, error) {
	if f.lookup != nil {
		return "", false, f.lookup
	}
	fingerprint, ok := f.pins[address]
	return fingerprint, ok, nil
}

func (f *fakeHostKeyPins) PinFirst(_ context.Context, address, fingerprint string, binding RuntimeHostKeyBinding) (string, error) {
	f.pinned++
	if held, ok := f.pins[address]; ok {
		return held, nil
	}
	f.pins[address], f.bindings[address] = fingerprint, binding
	return fingerprint, nil
}

func (f *fakeHostKeyPins) RetiredPins(_ context.Context, candidates map[string]string) (map[string]bool, error) {
	out := map[string]bool{}
	for address, fingerprint := range candidates {
		if f.retired[address] == fingerprint {
			out[address] = true
		}
	}
	return out, nil
}

// retire mirrors migration 176: the pin row goes and its retirement stays.
func (f *fakeHostKeyPins) retire(address string) {
	f.retired[address] = f.pins[address]
	delete(f.pins, address)
	delete(f.bindings, address)
}

func hostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatal(err)
	}
	return key
}

func TestRuntimeHostKeyChangedKeyFailsClosedWhilePinHolds(t *testing.T) {
	backend := newFakeHostKeyPins()
	store := NewRuntimeHostKeyStoreWithBackend("", backend)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	first, second := hostKey(t), hostKey(t)
	leaseA := RuntimeHostKeyBinding{TenantID: "tenant", LeaseID: "lease-a"}
	if err := store.VerifyAndPin("203.0.113.5:22", first, leaseA); err != nil {
		t.Fatal(err)
	}
	if backend.bindings["203.0.113.5:22"] != leaseA {
		t.Fatalf("the first-contact pin must carry its lease: %+v", backend.bindings)
	}
	leaseB := RuntimeHostKeyBinding{TenantID: "tenant", LeaseID: "lease-b"}
	for name, verify := range map[string]func() error{
		"bootstrap":   func() error { return store.VerifyAndPin("203.0.113.5:22", second, leaseB) },
		"diagnostics": func() error { return store.VerifyReadOnly("203.0.113.5:22", second) },
	} {
		if err := verify(); !errors.Is(err, ErrRuntimeHostKeyChanged) {
			t.Fatalf("%s: a changed key at a held address must be refused, got %v", name, err)
		}
	}
	if backend.pins["203.0.113.5:22"] != ssh.FingerprintSHA256(first) {
		t.Fatal("a refused key must never replace the pin")
	}
}

func TestRuntimeHostKeyRetiredPinStartsFreshPinForNextServer(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-host-keys.json")
	backend := newFakeHostKeyPins()
	store := NewRuntimeHostKeyStoreWithBackend(path, backend)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	old, next := hostKey(t), hostKey(t)
	if err := store.VerifyAndPin("203.0.113.6:22", old, RuntimeHostKeyBinding{TenantID: "t", LeaseID: "old"}); err != nil {
		t.Fatal(err)
	}
	// Another instance's provider-control transaction proves the old server
	// absent; this process and its file still hold the old pin.
	backend.retire("203.0.113.6:22")
	nextLease := RuntimeHostKeyBinding{TenantID: "t2", LeaseID: "next"}
	if err := store.VerifyAndPin("203.0.113.6:22", next, nextLease); err != nil {
		t.Fatalf("the next server at a retired address must pin fresh: %v", err)
	}
	if backend.pins["203.0.113.6:22"] != ssh.FingerprintSHA256(next) || backend.bindings["203.0.113.6:22"] != nextLease {
		t.Fatalf("the fresh pin must belong to the next lease: %+v %+v", backend.pins, backend.bindings)
	}
	data, err := os.ReadFile(path)
	if err != nil || strings.Contains(string(data), ssh.FingerprintSHA256(old)) {
		t.Fatalf("the file copy must drop the retired pin: %s %v", data, err)
	}

	// A restarted instance whose file still holds the retired pin follows the
	// database instead of refusing the address as a conflict.
	stale := filepath.Join(t.TempDir(), "runtime-host-keys.json")
	if err := os.WriteFile(stale, []byte(`{"203.0.113.6:22":"`+ssh.FingerprintSHA256(old)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	restarted := NewRuntimeHostKeyStoreWithBackend(stale, backend)
	if err := restarted.Load(); err != nil {
		t.Fatalf("a retired file pin is no conflict: %v", err)
	}
	if err := restarted.VerifyAndPin("203.0.113.6:22", next, nextLease); err != nil {
		t.Fatal(err)
	}
	if err := restarted.VerifyAndPin("203.0.113.6:22", old, nextLease); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("the retired key must not come back, got %v", err)
	}
}

func TestRuntimeHostKeyFilePinWithoutRetirementNeverDegradesToFirstUse(t *testing.T) {
	filePinned, presented := hostKey(t), hostKey(t)
	path := filepath.Join(t.TempDir(), "runtime-host-keys.json")
	if err := os.WriteFile(path, []byte(`{"203.0.113.7:22":"`+ssh.FingerprintSHA256(filePinned)+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	// The database lacks the pin (never imported) and never retired it.
	backend := newFakeHostKeyPins()
	store := NewRuntimeHostKeyStoreWithBackend(path, backend)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	if err := store.VerifyAndPin("203.0.113.7:22", presented, RuntimeHostKeyBinding{TenantID: "t", LeaseID: "l"}); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("an unretired file pin must keep holding, got %v", err)
	}
	if backend.pinned != 0 {
		t.Fatal("a refused key must never be pinned")
	}

	// A file pin that contradicts the database without a retirement refuses.
	conflicting := newFakeHostKeyPins()
	conflicting.pins["203.0.113.7:22"] = ssh.FingerprintSHA256(presented)
	refused := NewRuntimeHostKeyStoreWithBackend(path, conflicting)
	if err := refused.Load(); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("an unretired conflict must be reported, got %v", err)
	}
	if err := refused.VerifyAndPin("203.0.113.7:22", presented, RuntimeHostKeyBinding{}); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("an unretired conflict must refuse the address, got %v", err)
	}
}

func TestRuntimeHostKeyUnavailableStoreRefusesAndNeverPins(t *testing.T) {
	backend := newFakeHostKeyPins()
	store := NewRuntimeHostKeyStoreWithBackend("", backend)
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	backend.lookup = errors.New("database unreachable")
	key := hostKey(t)
	if err := store.VerifyAndPin("203.0.113.8:22", key, RuntimeHostKeyBinding{TenantID: "t", LeaseID: "l"}); !errors.Is(err, ErrRuntimeHostKeyUnverifiable) {
		t.Fatalf("an unreadable pin store must refuse, got %v", err)
	}
	if err := store.VerifyReadOnly("203.0.113.8:22", key); !errors.Is(err, ErrRuntimeHostKeyUnverifiable) {
		t.Fatalf("an unreadable pin store must refuse diagnostics, got %v", err)
	}
	backend.lookup = nil
	if err := store.VerifyReadOnly("203.0.113.8:22", key); err != nil || backend.pinned != 0 {
		t.Fatalf("diagnostics pass an unpinned address without pinning it: %v pinned=%d", err, backend.pinned)
	}
}
