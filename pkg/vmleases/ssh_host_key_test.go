package vmleases

import (
	"sync"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
)

func TestMemoryStorePinsFirstSSHHostKeyAcrossConcurrentAndStaleWrites(t *testing.T) {
	store := NewMemoryStore()
	lease := testLease(time.Now().UTC())
	lease.Metadata = map[string]string{MetadataKeyResourceGenerationID: "550e8400-e29b-41d4-a716-446655440000"}
	if _, err := store.Upsert(t.Context(), lease, "host-key-pin"); err != nil {
		t.Fatal(err)
	}
	stale, err := store.Get(t.Context(), "org-1", lease.ID)
	if err != nil {
		t.Fatal(err)
	}
	unauthorized := cloneLease(*stale)
	unauthorized.Metadata[MetadataKeySSHHostKey] = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIES1Xflf/Yf/edLYoabUDw1v88bOXzegNvZyNiiH+bik"
	if _, err := store.Update(t.Context(), "org-1", unauthorized); err != ErrSSHHostKeyImmutable {
		t.Fatalf("ordinary first-pin update error = %v, want %v", err, ErrSSHHostKeyImmutable)
	}
	digest, err := ResourceGenerationDigest("org-1", lease)
	if err != nil {
		t.Fatal(err)
	}
	keys := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIES1Xflf/Yf/edLYoabUDw1v88bOXzegNvZyNiiH+bik",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAILqyqJcqqaM5qWF7+Qgk8UmVXBNhVCGLyHpkvh043crA",
	}
	service := NewService(store, ServiceConfig{})
	results := make(chan string, len(keys))
	var group sync.WaitGroup
	for _, key := range keys {
		group.Add(1)
		go func() {
			defer group.Done()
			pinned, pinErr := service.PinSSHHostKey(t.Context(), SSHHostKeyPinRequest{
				TenantID: "org-1", LeaseID: lease.ID, OwnerSubjectID: "owner-1",
				ServerID: "server-1", StackID: "stack-1", ServerGeneration: 1,
				ExpectedResourceGenerationDigest: digest, HostKey: key,
			})
			if pinErr != nil {
				results <- "error"
				return
			}
			results <- pinned.Metadata[MetadataKeySSHHostKey]
		}()
	}
	group.Wait()
	close(results)
	var pinned string
	for result := range results {
		if result == "error" {
			t.Fatal("concurrent host-key pin failed")
		}
		if pinned == "" {
			pinned = result
		}
		if result != pinned {
			t.Fatalf("concurrent pins returned %q and %q", pinned, result)
		}
	}
	if pinned != keys[0] && pinned != keys[1] {
		t.Fatalf("pinned host key = %q", pinned)
	}
	stale.Metadata["unrelated"] = "update"
	if _, err := store.Update(t.Context(), "org-1", *stale); err != ErrSSHHostKeyImmutable {
		t.Fatalf("stale whole-lease update error = %v, want %v", err, ErrSSHHostKeyImmutable)
	}
	stored, err := store.Get(t.Context(), "org-1", vmlease.LeaseID("lease-1"))
	if err != nil {
		t.Fatal(err)
	}
	if stored.Metadata[MetadataKeySSHHostKey] != pinned {
		t.Fatalf("stored host key = %q, want %q", stored.Metadata[MetadataKeySSHHostKey], pinned)
	}
}
