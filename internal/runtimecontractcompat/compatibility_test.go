package runtimecontractcompat

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeinventory"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimelease"
	"github.com/kombifyio/techstack/internal/selfhostcontracts/stackaction"
)

const (
	lockSchemaV1 = "techstack.selfhost-runtime-contract-lock/v1"
	contractRoot = "github.com/kombifyio/techstack/internal/selfhostcontracts"
)

type consumerLock struct {
	SchemaVersion string        `json:"schemaVersion"`
	ContractRoot  string        `json:"contractRoot"`
	Packages      []string      `json:"packages"`
	Fixtures      []lockFixture `json:"fixtures"`
}

type lockFixture struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

func TestSelfhostRuntimeContractLockAndFixtures(t *testing.T) {
	root := filepath.Clean(filepath.Join("..", ".."))
	lock := readConsumerLock(t)
	wantPackages := []string{"runtimeinventory", "runtimelease", "stackaction"}
	if lock.SchemaVersion != lockSchemaV1 || lock.ContractRoot != contractRoot ||
		!slices.Equal(lock.Packages, wantPackages) || len(lock.Fixtures) != len(wantPackages) {
		t.Fatalf("unexpected self-host runtime contract lock: %+v", lock)
	}

	seen := map[string]bool{}
	for _, fixture := range lock.Fixtures {
		payload := readLockedFixture(t, root, fixture)
		name := strings.TrimSuffix(filepath.Base(fixture.Path), filepath.Ext(fixture.Path))
		if seen[name] {
			t.Fatalf("duplicate compatibility fixture %q", name)
		}
		seen[name] = true
		exerciseFixture(t, name, payload)
	}
}

func readConsumerLock(t *testing.T) consumerLock {
	t.Helper()
	payload, err := os.ReadFile("consumer-lock.json")
	if err != nil {
		t.Fatal(err)
	}
	var lock consumerLock
	if err := decodeClosed(payload, &lock); err != nil {
		t.Fatalf("decode consumer lock: %v", err)
	}
	return lock
}

func readLockedFixture(t *testing.T, root string, fixture lockFixture) []byte {
	t.Helper()
	cleanRoot, err := filepath.Abs(root)
	if err != nil {
		t.Fatal(err)
	}
	target, err := filepath.Abs(filepath.Join(cleanRoot, filepath.FromSlash(fixture.Path)))
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(cleanRoot, target)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("compatibility fixture escapes repository: %q", fixture.Path)
	}
	info, err := os.Lstat(target)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 1<<20 {
		t.Fatalf("compatibility fixture is not a bounded plain file: %q", fixture.Path)
	}
	payload, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(payload)
	if hex.EncodeToString(sum[:]) != fixture.SHA256 {
		t.Fatalf("compatibility fixture digest drift: %q", fixture.Path)
	}
	return payload
}

func exerciseFixture(t *testing.T, name string, payload []byte) {
	t.Helper()
	switch name {
	case "runtimeinventory":
		var inventory runtimeinventory.ServerList
		if err := decodeClosed(payload, &inventory); err != nil {
			t.Fatal(err)
		}
		if len(inventory.Servers) != 1 || inventory.Servers[0].Platform.OS != "windows" || inventory.Servers[0].Connection.State != "connected" {
			t.Fatalf("runtime inventory compatibility failed: %+v", inventory)
		}
	case "runtimelease":
		var lease runtimelease.Lease
		if err := decodeClosed(payload, &lease); err != nil {
			t.Fatal(err)
		}
		if err := lease.Validate(time.Date(2026, 8, 3, 10, 30, 0, 0, time.UTC)); err != nil {
			t.Fatalf("runtime lease compatibility failed: %v", err)
		}
	case "stackaction":
		var request stackaction.VerifyRolloutRequest
		if err := decodeClosed(payload, &request); err != nil {
			t.Fatal(err)
		}
		if err := request.Validate(); err != nil {
			t.Fatalf("StackKits verification compatibility failed: %+v err=%v", request, err)
		}
	default:
		t.Fatalf("unrecognized compatibility fixture %q", name)
	}
}

func decodeClosed(payload []byte, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}
