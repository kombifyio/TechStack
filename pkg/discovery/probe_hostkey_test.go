package discovery

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func TestHostKeyStoreFirstUseSaveDoesNotDeadlock(t *testing.T) {
	store := NewHostKeyStore(filepath.Join(t.TempDir(), "known_hosts"))
	callback := store.GetCallback("example.test:22")

	signer, err := ssh.NewSignerFromKey(mustEd25519PrivateKey(t))
	if err != nil {
		t.Fatalf("NewSignerFromKey returned error: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- callback(
			"example.test:22",
			&net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 22},
			signer.PublicKey(),
		)
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("host key callback returned error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("host key callback deadlocked on first use")
	}
}

func mustEd25519PrivateKey(t *testing.T) ed25519.PrivateKey {
	t.Helper()
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey returned error: %v", err)
	}
	return key
}

func testSSHPublicKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	signer, err := ssh.NewSignerFromKey(mustEd25519PrivateKey(t))
	if err != nil {
		t.Fatalf("NewSignerFromKey returned error: %v", err)
	}
	return signer.PublicKey()
}

func testRemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4(203, 0, 113, 10), Port: 22}
}

func TestHostKeyStorePersistsPinsAcrossStores(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	address := "node.example.test:22"
	pinned := testSSHPublicKey(t)

	first := NewHostKeyStore(path)
	if err := first.GetScopedCallback("tenant-a", address)(address, testRemoteAddr(), pinned); err != nil {
		t.Fatalf("first contact pin failed: %v", err)
	}

	second := NewHostKeyStore(path)
	if err := second.GetScopedCallback("tenant-a", address)(address, testRemoteAddr(), pinned); err != nil {
		t.Fatalf("reloaded store rejected the pinned key: %v", err)
	}
	if err := second.GetScopedCallback("tenant-a", address)(address, testRemoteAddr(), testSSHPublicKey(t)); err == nil {
		t.Fatal("reloaded store accepted a changed host key")
	}
}

func TestHostKeyStoreScopesPinsPerTrustBoundary(t *testing.T) {
	store := NewHostKeyStore(filepath.Join(t.TempDir(), "known_hosts"))
	address := "node.example.test:22"
	tenantAKey := testSSHPublicKey(t)
	tenantBKey := testSSHPublicKey(t)

	if err := store.GetScopedCallback("tenant-a", address)(address, testRemoteAddr(), tenantAKey); err != nil {
		t.Fatalf("tenant-a pin failed: %v", err)
	}
	if err := store.GetScopedCallback("tenant-b", address)(address, testRemoteAddr(), tenantBKey); err != nil {
		t.Fatalf("tenant-b must not be blocked by tenant-a's pin: %v", err)
	}
	if err := store.GetScopedCallback("tenant-a", address)(address, testRemoteAddr(), tenantBKey); err == nil {
		t.Fatal("tenant-a accepted a changed host key after tenant-b pinned a different key")
	}
}

func TestHostKeyStoreFailsClosedOnCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	if err := os.WriteFile(path, []byte("<not-a-known-hosts-file>"), 0o600); err != nil {
		t.Fatalf("seed corrupt store: %v", err)
	}
	store := NewHostKeyStore(path)
	err := store.GetScopedCallback("tenant-a", "node.example.test:22")("node.example.test:22", testRemoteAddr(), testSSHPublicKey(t))
	if err == nil {
		t.Fatal("corrupt store accepted a first-use key instead of failing closed")
	}
	if !strings.Contains(err.Error(), path) {
		t.Fatalf("error should name the store path, got %v", err)
	}
}

func TestHostKeyStoreFailsClosedWhenPinCannotPersist(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	store := NewHostKeyStore(filepath.Join(blocker, "known_hosts"))
	err := store.GetCallback("node.example.test:22")("node.example.test:22", testRemoteAddr(), testSSHPublicKey(t))
	if err == nil {
		t.Fatal("unpersistable pin was trusted instead of failing closed")
	}
}

func TestHostKeyStoreReadsLegacyUnscopedLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key := testSSHPublicKey(t)
	encoded := base64.StdEncoding.EncodeToString(key.Marshal())
	line := "node.example.test:22 " + encoded + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatalf("seed legacy store: %v", err)
	}
	store := NewHostKeyStore(path)
	if err := store.GetCallback("node.example.test:22")("node.example.test:22", testRemoteAddr(), key); err != nil {
		t.Fatalf("legacy unscoped pin was not honored: %v", err)
	}
	if err := store.GetCallback("node.example.test:22")("node.example.test:22", testRemoteAddr(), testSSHPublicKey(t)); err == nil {
		t.Fatal("legacy unscoped pin accepted a changed key")
	}
}
