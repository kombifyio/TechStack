package guardexec

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// Once this host pinned its binding key, the privileged executor itself runs
// only bindings that key signed, so a daemon that was taken over cannot mint
// its own authority: an unsigned, a foreign-signed and a tampered binding stop
// at the boundary, and the signed one runs once.
func TestPinnedExecutorRunsOnlyASignedBindingOnce(t *testing.T) {
	signer := func(seed byte) *guardbinding.Signer {
		s, err := guardbinding.NewSignerFromSeed(bytes.Repeat([]byte{seed}, ed25519.SeedSize))
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	controlPlane, stranger := signer(1), signer(2)
	pinPath := filepath.Join(t.TempDir(), guardbinding.PinFile)
	encoded, err := guardbinding.EncodePins(controlPlane.PublicKeys())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pinPath, encoded, 0o600); err != nil {
		t.Fatal(err)
	}
	backend := &fakeBackend{}
	self := uint32(os.Getuid())
	client := serveTest(t, &Server{
		Backend: backend, Replay: &memoryReplay{}, AllowPeer: func(uid uint32) bool { return uid == self },
		Pins: func() ([]guardbinding.PinnedKey, error) { return guardbinding.ReadPins(pinPath) },
	})
	signedBy := func(s *guardbinding.Signer) func(*guardbinding.Binding) {
		return func(b *guardbinding.Binding) {
			if err := s.Sign(b); err != nil {
				t.Fatal(err)
			}
		}
	}
	execute := func(params ExecuteParams) error {
		_, err := client.Call(context.Background(), OpStackKitExecute, params, nil)
		return err
	}
	tampered := admitted(t, "cmd-tampered", signedBy(controlPlane))
	tampered.Binding.CurrentGeneration++
	for name, params := range map[string]ExecuteParams{
		"unsigned": admitted(t, "cmd-unsigned", nil),
		"stranger": admitted(t, "cmd-stranger", signedBy(stranger)),
		"tampered": tampered,
	} {
		if err := execute(params); !IsCode(err, CodeBindingMismatch) {
			t.Fatalf("%s binding err = %v", name, err)
		}
	}
	if err := execute(admitted(t, "cmd-signed", signedBy(controlPlane))); err != nil {
		t.Fatalf("the signed binding was refused: %v", err)
	}
	if err := execute(admitted(t, "cmd-signed", signedBy(controlPlane))); !IsCode(err, CodeReplayed) {
		t.Fatalf("replay err = %v", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.executed) != 1 || backend.executed[0] != "cmd-signed" {
		t.Fatalf("the privileged backend ran %v; only the signed binding may reach it", backend.executed)
	}
}

// A host that pinned no binding key cannot tell a forged binding from a real
// one, so its privileged executor refuses to run a mutating command at all.
func TestExecutorWithoutPinnedKeyRefusesMutation(t *testing.T) {
	backend := &fakeBackend{}
	self := uint32(os.Getuid())
	client := serveTest(t, &Server{
		Backend: backend, Replay: &memoryReplay{}, AllowPeer: func(uid uint32) bool { return uid == self },
		Pins: func() ([]guardbinding.PinnedKey, error) { return nil, nil },
	})
	_, err := client.Call(context.Background(), OpStackKitExecute, admitted(t, "cmd-unpinned", nil), nil)
	if !IsCode(err, CodeBindingMismatch) {
		t.Fatalf("unpinned mutation err = %v", err)
	}
	backend.mu.Lock()
	defer backend.mu.Unlock()
	if len(backend.executed) != 0 {
		t.Fatalf("the privileged backend ran %v without a pinned key", backend.executed)
	}
}
