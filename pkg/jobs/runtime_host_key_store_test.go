package jobs

import (
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func testRuntimeHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	public, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate host key: %v", err)
	}
	key, err := ssh.NewPublicKey(public)
	if err != nil {
		t.Fatalf("wrap host key: %v", err)
	}
	return key
}

func testRuntimeHostAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("203.0.113.10"), Port: 22}
}

func TestRuntimeHostKeyStorePinsFirstContactAndRejectsChanges(t *testing.T) {
	store := NewRuntimeHostKeyStore("")
	address := testRuntimeHostAddr().String()
	pinned := testRuntimeHostKey(t)
	replacement := testRuntimeHostKey(t)

	if err := store.VerifyAndPin(address, pinned); err != nil {
		t.Fatalf("first contact pin failed: %v", err)
	}
	if err := store.VerifyAndPin(address, pinned); err != nil {
		t.Fatalf("repeat contact with the pinned key failed: %v", err)
	}
	if err := store.VerifyAndPin(address, replacement); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("changed key error = %v, want ErrRuntimeHostKeyChanged", err)
	}
}

func TestRuntimeHostKeyStoreReadOnlyNeverPins(t *testing.T) {
	store := NewRuntimeHostKeyStore("")
	address := testRuntimeHostAddr().String()
	observed := testRuntimeHostKey(t)
	replacement := testRuntimeHostKey(t)

	if err := store.VerifyReadOnly(address, observed); err != nil {
		t.Fatalf("read-only verify of an unpinned address failed: %v", err)
	}
	// Nothing was pinned, so a different key still pins cleanly afterwards.
	if err := store.VerifyAndPin(address, replacement); err != nil {
		t.Fatalf("pin after read-only verify failed: %v", err)
	}
	if err := store.VerifyReadOnly(address, observed); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("read-only verify of a changed key = %v, want ErrRuntimeHostKeyChanged", err)
	}
}

func TestRuntimeHostKeyStorePersistsAcrossRestarts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-host-keys.json")
	address := testRuntimeHostAddr().String()
	pinned := testRuntimeHostKey(t)

	first := NewRuntimeHostKeyStore(path)
	if err := first.Load(); err != nil {
		t.Fatalf("load empty store: %v", err)
	}
	if err := first.VerifyAndPin(address, pinned); err != nil {
		t.Fatalf("pin: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("pinned store not written: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("pinned store permissions = %o, want 600", info.Mode().Perm())
	}

	second := NewRuntimeHostKeyStore(path)
	if err := second.Load(); err != nil {
		t.Fatalf("reload store: %v", err)
	}
	if err := second.VerifyAndPin(address, pinned); err != nil {
		t.Fatalf("reloaded store rejected the pinned key: %v", err)
	}
	if err := second.VerifyAndPin(address, testRuntimeHostKey(t)); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("reloaded changed key error = %v, want ErrRuntimeHostKeyChanged", err)
	}
}

func TestRuntimeHostKeyStoreFailsClosedOnCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime-host-keys.json")
	if err := os.WriteFile(path, []byte("{not-json"), 0o600); err != nil {
		t.Fatalf("seed corrupt store: %v", err)
	}
	store := NewRuntimeHostKeyStore(path)
	if err := store.Load(); err == nil {
		t.Fatal("corrupt store loaded without error")
	}
	if err := store.VerifyAndPin(testRuntimeHostAddr().String(), testRuntimeHostKey(t)); err == nil {
		t.Fatal("corrupt store pinned a key instead of failing closed")
	}
}

func TestRuntimeTargetBootstrapUsesPinnedHostKeys(t *testing.T) {
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		HostKeys: NewRuntimeHostKeyStore(""),
	})
	callback := bootstrapper.bootstrapHostKeyCallback()
	address := testRuntimeHostAddr()
	pinned := testRuntimeHostKey(t)

	if err := callback(address.String(), address, pinned); err != nil {
		t.Fatalf("first-contact callback failed: %v", err)
	}
	if err := callback(address.String(), address, testRuntimeHostKey(t)); !errors.Is(err, ErrRuntimeHostKeyChanged) {
		t.Fatalf("changed key callback error = %v, want ErrRuntimeHostKeyChanged", err)
	}
}

func TestRuntimeDiagnosticsVerifiesWithoutPinning(t *testing.T) {
	collector := NewSSHRuntimeDiagnosticsCollector(SSHRuntimeDiagnosticsCollectorConfig{
		HostKeys: NewRuntimeHostKeyStore(""),
	})
	callback := collector.diagnosticsHostKeyCallback()
	address := testRuntimeHostAddr()

	if err := callback(address.String(), address, testRuntimeHostKey(t)); err != nil {
		t.Fatalf("read-only diagnostics callback failed: %v", err)
	}
	// The diagnostics key was not pinned: a bootstrap with a different key
	// still becomes the first pin.
	bootstrapper := NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{
		HostKeys: collector.hostKeys,
	})
	if err := bootstrapper.bootstrapHostKeyCallback()(address.String(), address, testRuntimeHostKey(t)); err != nil {
		t.Fatalf("bootstrap after diagnostics failed to pin: %v", err)
	}
}

// Security boundary: without a pin store the runtime SSH paths must reject the
// handshake instead of sending target credentials to an unverified host.
func TestRuntimeHostKeyCallbacksRejectWithoutPinStore(t *testing.T) {
	address := testRuntimeHostAddr()
	callbacks := map[string]ssh.HostKeyCallback{
		"bootstrap":   NewSSHRuntimeTargetBootstrapper(SSHRuntimeTargetBootstrapperConfig{}).bootstrapHostKeyCallback(),
		"diagnostics": NewSSHRuntimeDiagnosticsCollector(SSHRuntimeDiagnosticsCollectorConfig{}).diagnosticsHostKeyCallback(),
	}
	for name, callback := range callbacks {
		if err := callback(address.String(), address, testRuntimeHostKey(t)); !errors.Is(err, ErrRuntimeHostKeyUnverifiable) {
			t.Fatalf("%s callback without a pin store = %v, want ErrRuntimeHostKeyUnverifiable", name, err)
		}
	}
}
