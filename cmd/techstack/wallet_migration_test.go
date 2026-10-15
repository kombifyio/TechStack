package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/kombifywallet/walletfake"
	"github.com/kombifyio/techstack/pkg/walletmigration"
)

// Execute reads secrets and writes them into the Wallet, so without a complete
// Wallet configuration and the legacy key it refuses before the database is
// opened, and writes nothing.
func TestWalletMigrationExecuteRefusesBeforeTouchingTheDatabase(t *testing.T) {
	const validKey = "walletmigration-test-key-32-byte"
	walletEnv := func(key string) map[string]string {
		env := walletfake.New(t).Env()
		if key != "" {
			env[auth.EncryptionKeyEnvVar] = key
		}
		return env
	}
	cases := map[string]struct {
		env  map[string]string
		code string
	}{
		"kombify Wallet not configured": {env: map[string]string{auth.EncryptionKeyEnvVar: validKey}, code: walletmigration.ErrorCodeWalletUnavailable},
		"legacy key missing":            {env: walletEnv(""), code: walletmigration.ErrorCodeEncryptionKeyRequired},
		"legacy key of the wrong size":  {env: walletEnv("short"), code: walletmigration.ErrorCodeEncryptionKeyRequired},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			opened := false
			deps := walletMigrationDeps{
				getenv: func(key string) string { return tc.env[key] },
				openStore: func(context.Context) (walletmigration.Store, func(), error) {
					opened = true
					return nil, func() {}, errors.New("must not open")
				},
			}
			var stdout, stderr bytes.Buffer
			err := runWalletMigrationWith(context.Background(), []string{"execute", "--tenant", "org_a"}, &stdout, &stderr, deps)
			var denial *walletmigration.Denial
			if !errors.As(err, &denial) || denial.ErrorCode != tc.code || denial.Retryable {
				t.Fatalf("execute error = %v, want a %s denial", err, tc.code)
			}
			if opened {
				t.Fatal("execute opened the database before refusing")
			}
			var printed walletmigration.Denial
			if decodeErr := json.Unmarshal(stderr.Bytes(), &printed); decodeErr != nil || printed.ErrorCode != tc.code {
				t.Fatalf("stderr denial = %q (%v)", stderr.String(), decodeErr)
			}
			if stdout.Len() != 0 {
				t.Fatalf("execute wrote a report: %q", stdout.String())
			}
		})
	}
}

type stackOnlyStore struct {
	walletMigrationStore
	stack    controlplane.Stack
	swapped  *controlplane.StackConfigCAS
	conflict bool
}

func (s *stackOnlyStore) GetStack(context.Context, string, string) (*controlplane.Stack, error) {
	stack := s.stack
	return &stack, nil
}

func (s *stackOnlyStore) CompareAndSwapStackConfig(_ context.Context, command controlplane.StackConfigCAS) (*controlplane.Stack, error) {
	s.swapped = &command
	if s.conflict {
		return nil, controlplane.ErrConflict
	}
	return &s.stack, nil
}

// The caller switch writes the Wallet item id beside the name label, and only
// into the stack revision the migration read.
func TestWalletMigrationBindsAStackOnlyAtTheRevisionItRead(t *testing.T) {
	read := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	newStore := func() *stackOnlyStore {
		return &stackOnlyStore{stack: controlplane.Stack{ID: "stack-k", TenantID: "org_a", UpdatedAt: read.Add(time.Second),
			Config: map[string]any{"server_remote_ssh_key_label": "prod"}}}
	}

	stale := newStore()
	err := walletMigrationStoreAdapter{store: stale}.BindStackCredential(context.Background(), "org_a", "stack-k", read, "itm_x")
	if !errors.Is(err, walletmigration.ErrSourceChanged) || stale.swapped != nil {
		t.Fatalf("a stack that changed since it was read was written: %v", err)
	}

	current := newStore()
	err = walletMigrationStoreAdapter{store: current}.BindStackCredential(context.Background(), "org_a", "stack-k", read.Add(time.Second), "itm_x")
	if err != nil || current.swapped == nil || !current.swapped.ExpectedUpdatedAt.Equal(read.Add(time.Second)) {
		t.Fatalf("bind = %v, swap %+v", err, current.swapped)
	}
	if current.swapped.Config[walletmigration.StackConfigKeyCredentialItemID] != "itm_x" || current.swapped.Config["server_remote_ssh_key_label"] != "prod" {
		t.Fatalf("config = %v", current.swapped.Config)
	}

	raced := newStore()
	raced.conflict = true
	err = walletMigrationStoreAdapter{store: raced}.BindStackCredential(context.Background(), "org_a", "stack-k", read.Add(time.Second), "itm_x")
	if !errors.Is(err, walletmigration.ErrSourceChanged) {
		t.Fatalf("a lost compare-and-swap = %v", err)
	}
}
