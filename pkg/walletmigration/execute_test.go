package walletmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	"github.com/kombifyio/techstack/pkg/kombifywallet/walletfake"
)

const (
	alice = "auth0|alice"
	bob   = "auth0|bob"
)

// memStore is an in-memory Store. beforeRecord runs just before a row is
// recorded and stands for a writer that changes the row after the executor
// read it.
type memStore struct {
	rows         map[rowKey]SourceRow
	stacks       map[stackKey]*memStack
	clock        int
	beforeRecord func(*memStore)
}

type memStack struct {
	owner    string
	config   map[string]any
	revision time.Time
}

func newMemStore() *memStore {
	return &memStore{rows: map[rowKey]SourceRow{}, stacks: map[stackKey]*memStack{}}
}

func (s *memStore) tick() time.Time {
	s.clock++
	return time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC).Add(time.Duration(s.clock) * time.Second)
}

func (s *memStore) addRow(row SourceRow) {
	if row.CreatedAt.IsZero() {
		row.CreatedAt = s.tick()
	}
	row.UpdatedAt = s.tick()
	s.rows[rowKey{row.TenantID, row.ID}] = row
}

func (s *memStore) addStack(tenant, id, owner string, config map[string]any) {
	s.stacks[stackKey{tenant, id}] = &memStack{owner: owner, config: config, revision: s.tick()}
}

func (s *memStore) ListWalletRows(_ context.Context, tenant string) ([]SourceRow, error) {
	var rows []SourceRow
	for _, row := range s.rows {
		if row.TenantID == tenant {
			rows = append(rows, cloneRow(row))
		}
	}
	return rows, nil
}

func (s *memStore) ListStackCredentialRefs(_ context.Context, tenant string) ([]StackCredentialRef, error) {
	var refs []StackCredentialRef
	for key, stack := range s.stacks {
		if key.tenant != tenant {
			continue
		}
		if ref, ok := StackCredentialRefFromConfig(tenant, key.stack, stack.owner, stack.config); ok {
			ref.Revision = stack.revision
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (s *memStore) GetWalletRow(_ context.Context, tenant, id string) (SourceRow, error) {
	row, ok := s.rows[rowKey{tenant, id}]
	if !ok {
		return SourceRow{}, ErrSourceGone
	}
	return cloneRow(row), nil
}

func (s *memStore) RecordWalletItem(_ context.Context, tenant, id string, revision time.Time, walletItemID string) error {
	if s.beforeRecord != nil {
		s.beforeRecord(s)
	}
	key := rowKey{tenant, id}
	row, ok := s.rows[key]
	if !ok || !row.UpdatedAt.Equal(revision) {
		return ErrSourceChanged
	}
	row.Metadata[RecordedItemKey] = walletItemID
	row.UpdatedAt = s.tick()
	s.rows[key] = row
	return nil
}

func (s *memStore) BindStackCredential(_ context.Context, tenant, stackID string, revision time.Time, walletItemID string) error {
	stack, ok := s.stacks[stackKey{tenant, stackID}]
	if !ok || !stack.revision.Equal(revision) {
		return ErrSourceChanged
	}
	stack.config[StackConfigKeyCredentialItemID] = walletItemID
	stack.revision = s.tick()
	return nil
}

func cloneRow(row SourceRow) SourceRow {
	metadata := make(map[string]any, len(row.Metadata))
	for key, value := range row.Metadata {
		metadata[key] = value
	}
	row.Metadata = metadata
	return row
}

func (s *memStore) row(tenant, id string) SourceRow { return s.rows[rowKey{tenant, id}] }

// execRow is an eligible wallet_items row of an owner, secrets sealed under
// the test key.
func execRow(id, tenant, owner, kind string, metadata map[string]any) SourceRow {
	metadata["owner_id"] = owner
	return SourceRow{ID: id, TenantID: tenant, ItemType: kind, Metadata: metadata}
}

func executor(t *testing.T, fake *walletfake.Fake, store Store) Executor {
	t.Helper()
	return Executor{
		Store: store, Wallet: fake.Client(), Decrypt: testEncryptor(t).Decrypt,
		Now: func() time.Time { return time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC) },
	}
}

func resultOf(t *testing.T, report ExecuteReport, tenant, id string) RowResult {
	t.Helper()
	for _, result := range report.Results {
		if result.TenantID == tenant && result.ItemID == id {
			return result
		}
	}
	t.Fatalf("no result for %s/%s in %+v", tenant, id, report.Results)
	return RowResult{}
}

func stackResultOf(t *testing.T, report ExecuteReport, tenant, id string) StackResult {
	t.Helper()
	for _, result := range report.StackResults {
		if result.TenantID == tenant && result.StackID == id {
			return result
		}
	}
	t.Fatalf("no stack result for %s/%s in %+v", tenant, id, report.StackResults)
	return StackResult{}
}

func executeAll(t *testing.T, e Executor, tenants ...string) ExecuteReport {
	t.Helper()
	report, err := e.Execute(context.Background(), Options{Tenants: tenants})
	if err != nil {
		t.Fatal(err)
	}
	return report
}

// A row moves into its owner's Wallet, the Wallet holds exactly the legacy
// value, and the row records the item while keeping its ciphertext.
func TestExecuteMovesARowIntoTheOwnersWalletAndRecordsTheItem(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	ciphertext := sealed(t, knownSecret)
	store.addRow(execRow("key-1", "org_a", alice, "api_key", map[string]any{"name": "Grafana", "secret": ciphertext}))
	store.addRow(execRow("plain-1", "org_a", alice, "api_key", map[string]any{"secret": " legacy plaintext, kept as stored\n"}))
	store.addRow(SourceRow{ID: "cert-1", TenantID: "org_a", StackID: "stack-1", ItemType: "certificate",
		Metadata: map[string]any{"owner_id": alice, "stack_id": "stack-1", "secret": sealed(t, "pem-body")}})

	report := executeAll(t, executor(t, fake, store), "org_a")

	items := fake.Items(alice)
	if len(items) != 3 || report.Rows.Migrated != 3 || !report.Complete() {
		t.Fatalf("items %d, report %+v", len(items), report.Rows)
	}
	for _, item := range items {
		recorded := ""
		for _, id := range []string{"key-1", "plain-1", "cert-1"} {
			if store.row("org_a", id).Metadata[RecordedItemKey] == item.ID {
				recorded = id
			}
		}
		switch recorded {
		case "key-1":
			if item.Type != walletservice.ItemTypeGenericSecret || item.Scope != "account" || string(item.Value()) != knownSecret {
				t.Fatalf("key-1 item = %s %s", item.Type, item.Scope)
			}
		case "plain-1":
			if string(item.Value()) != " legacy plaintext, kept as stored\n" {
				t.Fatalf("plain-1 value was altered")
			}
		case "cert-1":
			if item.Scope != "project:stack-1" || string(item.Value()) != "pem-body" {
				t.Fatalf("cert-1 item scope %s", item.Scope)
			}
		default:
			t.Fatalf("item %s is recorded on no row", item.ID)
		}
	}
	if store.row("org_a", "key-1").Metadata["secret"] != ciphertext {
		t.Fatal("the legacy ciphertext was not kept")
	}
}

// A rerun converges on the same Wallet items instead of creating second ones.
func TestExecuteRerunConvergesOnTheSameItems(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	store.addRow(execRow("key-1", "org_a", alice, "api_key", map[string]any{"secret": sealed(t, knownSecret)}))
	e := executor(t, fake, store)

	first := executeAll(t, e, "org_a")
	second := executeAll(t, e, "org_a")

	creates := 0
	for _, request := range fake.Requests() {
		if request.Route == "/items" {
			creates++
		}
	}
	if creates != 1 || len(fake.Items(alice)) != 1 || first.Rows.Migrated != 1 || second.Rows.AlreadyMigrated != 1 || !second.Complete() {
		t.Fatalf("creates %d, items %d, first %+v, second %+v", creates, len(fake.Items(alice)), first.Rows, second.Rows)
	}
}

// An item an interrupted run left behind is adopted only when it reads back
// identically to the legacy value.
func TestExecuteAdoptsAnInterruptedRunsItemOnlyWhenItReadsBackIdentically(t *testing.T) {
	cases := map[string]struct {
		leftValue string
		outcome   Outcome
	}{
		"identical value is adopted": {leftValue: knownSecret, outcome: OutcomeAdopted},
		"different value is refused": {leftValue: "something-else", outcome: OutcomeFailed},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			fake := walletfake.New(t)
			fake.AllowCreate(alice)
			store := newMemStore()
			row := execRow("key-1", "org_a", alice, "api_key", map[string]any{"secret": sealed(t, knownSecret)})
			store.addRow(row)
			left := PlanItem(row, "org_a").Target
			leftID := fake.GrantDescribed(alice, walletservice.ItemTypeGenericSecret, left.Metadata.DisplayName, left.Metadata.Description, "account", []byte(tc.leftValue), "use", "read", "manage")

			report := executeAll(t, executor(t, fake, store), "org_a")

			recorded, _ := store.row("org_a", "key-1").Metadata[RecordedItemKey].(string)
			if result := resultOf(t, report, "org_a", "key-1"); result.Outcome != tc.outcome || len(fake.Items(alice)) != 1 {
				t.Fatalf("result %+v, items %d", result, len(fake.Items(alice)))
			}
			if adopted := tc.outcome == OutcomeAdopted; adopted != (recorded == leftID) || (!adopted && recorded != "") {
				t.Fatalf("recorded %q, left item %q", recorded, leftID)
			}
		})
	}
}

type corruptingReadBack struct{ Wallet }

func (c corruptingReadBack) Reveal(ctx context.Context, owner, itemID string) (walletservice.Secret, error) {
	delivered, err := c.Wallet.Reveal(ctx, owner, itemID)
	if err == nil && len(delivered.Bytes()) > 0 {
		delivered.Bytes()[0] ^= 0xff
	}
	return delivered, err
}

// The source row is touched only after the new item has been delivered back
// and compared; a mismatch leaves the row as it was.
func TestExecuteDoesNotRecordAnItemThatDoesNotReadBackIdentically(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	store.addRow(execRow("key-1", "org_a", alice, "api_key", map[string]any{"secret": sealed(t, knownSecret)}))
	e := executor(t, fake, store)
	e.Wallet = corruptingReadBack{e.Wallet}

	report := executeAll(t, e, "org_a")

	if result := resultOf(t, report, "org_a", "key-1"); result.Outcome != OutcomeFailed || report.Complete() {
		t.Fatalf("result %+v", result)
	}
	if _, recorded := store.row("org_a", "key-1").Metadata[RecordedItemKey]; recorded {
		t.Fatal("the row records an item that did not read back identically")
	}
}

// A row that changed after it was read is skipped: the executor never
// overwrites the newer row.
func TestExecuteSkipsARowThatChangedSinceItWasRead(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	store.addRow(execRow("key-1", "org_a", alice, "api_key", map[string]any{"secret": sealed(t, knownSecret), "notes": "before"}))
	store.beforeRecord = func(s *memStore) {
		row := s.rows[rowKey{"org_a", "key-1"}]
		row.Metadata["notes"] = "edited by the owner"
		row.UpdatedAt = s.tick()
		s.rows[rowKey{"org_a", "key-1"}] = row
	}

	report := executeAll(t, executor(t, fake, store), "org_a")

	row := store.row("org_a", "key-1")
	if _, recorded := row.Metadata[RecordedItemKey]; recorded || row.Metadata["notes"] != "edited by the owner" {
		t.Fatalf("the newer row was overwritten: %v", row.Metadata)
	}
	if result := resultOf(t, report, "org_a", "key-1"); result.Outcome != OutcomeSkippedChanged || report.NotMoved() != 1 {
		t.Fatalf("result %+v, not moved %d", result, report.NotMoved())
	}
}

// Without the owner's create right a row stays untouched and is never even
// decrypted; other owners in the same run move on.
func TestExecuteLeavesRowsOfAnOwnerWithoutTheCreateRightUntouched(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(bob)
	store := newMemStore()
	store.addRow(execRow("alice-1", "org_a", alice, "api_key", map[string]any{"secret": sealed(t, knownSecret)}))
	store.addRow(execRow("bob-1", "org_a", bob, "api_key", map[string]any{"secret": sealed(t, knownSecret)}))
	e := executor(t, fake, store)
	decrypts := 0
	opener := e.Decrypt
	e.Decrypt = func(value string) (string, error) { decrypts++; return opener(value) }

	report := executeAll(t, e, "org_a")

	if result := resultOf(t, report, "org_a", "alice-1"); result.Outcome != OutcomeCreateRightMissing {
		t.Fatalf("alice-1 = %+v", result)
	}
	if _, recorded := store.row("org_a", "alice-1").Metadata[RecordedItemKey]; recorded || len(fake.Items(alice)) != 0 {
		t.Fatal("a row of an owner without the create right was touched")
	}
	if decrypts != 1 || report.Rows.Migrated != 1 || report.Complete() {
		t.Fatalf("decryptions %d (want only bob's row), report %+v", decrypts, report.Rows)
	}
}

// Quarantined rows are not touched, and the report names rows by id only.
func TestExecuteReportCarriesNoValueNameOrOwner(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	store.addRow(execRow("login-1", "org_a", alice, "password", map[string]any{
		"name": "Grafana Prod", "username": knownUsername, "secret": sealed(t, knownSecret), "totp": sealed(t, knownTOTP), "notes": knownNotes,
	}))
	store.addRow(execRow("local-1", "org_a", "local-account-7", "api_key", map[string]any{"name": "Local", "secret": sealed(t, knownOldKey)}))
	store.addRow(execRow("broken-1", "org_a", alice, "api_key", map[string]any{"name": "Broken", "secret": "enc:v1:AAAA"}))

	report := executeAll(t, executor(t, fake, store), "org_a")

	var text bytes.Buffer
	if err := report.WriteText(&text); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if report.Rows.Migrated != 1 || report.Rows.Quarantined != 2 || report.Owners != 1 {
		t.Fatalf("rows %+v owners %d", report.Rows, report.Owners)
	}
	if _, recorded := store.row("org_a", "local-1").Metadata[RecordedItemKey]; recorded {
		t.Fatal("a quarantined row was migrated")
	}
	for _, output := range []string{text.String(), string(encoded)} {
		for _, leak := range []string{knownSecret, knownTOTP, knownUsername, knownNotes, knownOldKey, "Grafana Prod", "Local", "Broken", alice, "local-account-7", "alice"} {
			if strings.Contains(output, leak) {
				t.Errorf("report contains %q", leak)
			}
		}
	}
}

// A stack whose name-label reference resolves to a migrated SSH key now names
// the Wallet item; an ambiguous reference stays as it is.
func TestExecuteSwitchesAStackReferenceToTheWalletItem(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	store.addRow(execRow("key-1", "org_a", alice, "ssh_key", map[string]any{"name": "Prod", "secret": sealed(t, knownNewKey)}))
	store.addRow(execRow("twin-1", "org_a", alice, "ssh_key", map[string]any{"name": "Twin", "secret": sealed(t, knownOldKey)}))
	store.addRow(execRow("twin-2", "org_a", alice, "ssh_key", map[string]any{"name": " twin ", "secret": sealed(t, knownSSHPassword)}))
	store.addStack("org_a", "stack-k", alice, map[string]any{
		"server_remote_host": "203.0.113.6", "server_remote_user": "root", "server_remote_port": float64(2222),
		"server_remote_auth_method": "ssh-key", "server_remote_ssh_key_label": "PROD",
	})
	store.addStack("org_a", "stack-amb", alice, map[string]any{
		"server_remote_host": "203.0.113.7", "server_remote_user": "root", "server_remote_ssh_key_label": "twin",
	})

	report := executeAll(t, executor(t, fake, store), "org_a")

	bound, _ := store.stacks[stackKey{"org_a", "stack-k"}].config[StackConfigKeyCredentialItemID].(string)
	item, ok := fake.Item(bound)
	if !ok || item.Type != walletservice.ItemTypeNodeAccessProfile || store.row("org_a", "key-1").Metadata[RecordedItemKey] != bound {
		t.Fatalf("stack-k is bound to %q (found %v)", bound, ok)
	}
	var profile map[string]any
	if err := json.Unmarshal(item.Value(), &profile); err != nil {
		t.Fatal(err)
	}
	if profile["host"] != "203.0.113.6" || profile["user"] != "root" || profile["port"] != float64(2222) || profile["privateKey"] != knownNewKey {
		t.Fatalf("node access profile = %v", profile)
	}
	if store.stacks[stackKey{"org_a", "stack-k"}].config["server_remote_ssh_key_label"] != "PROD" {
		t.Fatal("the name label was removed; clearing the item id must stay the rollback")
	}
	if _, switched := store.stacks[stackKey{"org_a", "stack-amb"}].config[StackConfigKeyCredentialItemID]; switched {
		t.Fatal("an ambiguous reference was switched")
	}
	if stackResultOf(t, report, "org_a", "stack-k").Outcome != StackSwitched || stackResultOf(t, report, "org_a", "stack-amb").Outcome != StackAmbiguous {
		t.Fatalf("stack results %+v", report.StackResults)
	}
}

// A stack that already reads a Wallet item keeps it: the legacy row its old
// label still matches must not replace the owner's choice.
func TestExecuteLeavesAStackThatAlreadyReadsAWalletItem(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	store.addRow(execRow("key-1", "org_a", alice, "ssh_key", map[string]any{"name": "Prod", "secret": sealed(t, knownNewKey)}))
	chosen := fake.Grant(alice, walletservice.ItemTypeNodeAccessProfile, "Chosen in the Wallet", "account", []byte(`{"privateKey":"k"}`), "use")
	store.addStack("org_a", "stack-k", alice, map[string]any{
		"server_remote_host": "203.0.113.6", "server_remote_user": "root",
		"server_remote_ssh_key_label": "prod", StackConfigKeyCredentialItemID: chosen,
	})

	report := executeAll(t, executor(t, fake, store), "org_a")

	if got := store.stacks[stackKey{"org_a", "stack-k"}].config[StackConfigKeyCredentialItemID]; got != chosen || len(report.StackResults) != 0 {
		t.Fatalf("stack item = %v, results %+v", got, report.StackResults)
	}
}

// Two Wallet items carrying one migration token cannot both be the row's item;
// the executor picks neither, writes nothing and reports the row.
func TestExecuteWritesNothingWhenTwoItemsCarryTheSameMigrationToken(t *testing.T) {
	fake := walletfake.New(t)
	fake.AllowCreate(alice)
	store := newMemStore()
	row := execRow("key-1", "org_a", alice, "api_key", map[string]any{"secret": sealed(t, knownSecret)})
	store.addRow(row)
	left := PlanItem(row, "org_a").Target
	for range 2 {
		fake.GrantDescribed(alice, walletservice.ItemTypeGenericSecret, left.Metadata.DisplayName, left.Metadata.Description, "account", []byte(knownSecret), "use", "read", "manage")
	}

	report := executeAll(t, executor(t, fake, store), "org_a")

	if _, recorded := store.row("org_a", "key-1").Metadata[RecordedItemKey]; recorded || len(fake.Items(alice)) != 2 {
		t.Fatalf("an ambiguous row was written: items %d", len(fake.Items(alice)))
	}
	if result := resultOf(t, report, "org_a", "key-1"); result.Outcome != OutcomeAmbiguous || report.Complete() {
		t.Fatalf("result %+v", result)
	}
}
