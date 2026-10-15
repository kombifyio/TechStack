package walletmigration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeReader is an in-memory Reader keyed by tenant.
type fakeReader struct {
	rows    map[string][]SourceRow
	stacks  map[string][]fakeStack
	failFor string
}

type fakeStack struct {
	id, owner string
	config    map[string]any
}

func (f fakeReader) ListWalletRows(_ context.Context, tenant string) ([]SourceRow, error) {
	if tenant == f.failFor {
		return nil, errors.New("connection reset")
	}
	return f.rows[tenant], nil
}

func (f fakeReader) ListStackCredentialRefs(_ context.Context, tenant string) ([]StackCredentialRef, error) {
	var refs []StackCredentialRef
	for _, stack := range f.stacks[tenant] {
		if ref, ok := StackCredentialRefFromConfig(tenant, stack.id, stack.owner, stack.config); ok {
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

// Values the report must never contain.
const (
	knownSecret      = "correct-horse-battery-staple"
	knownTOTP        = "JBSWY3DPEHPK3PXP"
	knownUsername    = "ops-admin@example.test"
	knownNotes       = "break-glass note 7731"
	knownOldKey      = "old-private-key-5521"
	knownNewKey      = "new-private-key-6632"
	knownSSHPassword = "ssh-password-7743"
)

func plannerFixture(t *testing.T) (fakeReader, string) {
	t.Helper()
	ciphertext := sealed(t, knownSecret)
	at := func(minute int) time.Time { return time.Date(2026, 9, 1, 12, minute, 0, 0, time.UTC) }
	owner := map[string]any{"owner_id": "auth0|alice"}
	with := func(extra map[string]any) map[string]any {
		out := map[string]any{}
		for k, v := range owner {
			out[k] = v
		}
		for k, v := range extra {
			out[k] = v
		}
		return out
	}
	reader := fakeReader{
		rows: map[string][]SourceRow{
			"org_a": {
				{ID: "login-1", TenantID: "org_a", ItemType: "password", CreatedAt: at(1),
					Metadata: with(map[string]any{"name": "Grafana", "username": knownUsername, "secret": ciphertext, "totp": sealed(t, knownTOTP), "notes": knownNotes})},
				{ID: "plain-1", TenantID: "org_a", ItemType: "api_key", CreatedAt: at(2),
					Metadata: with(map[string]any{"name": "Legacy", "secret": knownSecret})},
				{ID: "key-old", TenantID: "org_a", ItemType: "ssh_key", CreatedAt: at(3),
					Metadata: with(map[string]any{"name": "Prod", "secret": sealed(t, knownOldKey)})},
				{ID: "key-new", TenantID: "org_a", ItemType: "ssh_key", CreatedAt: at(4),
					Metadata: with(map[string]any{"name": " prod ", "secret": sealed(t, knownNewKey)})},
				{ID: "stack-p:remote-ssh", TenantID: "org_a", StackID: "stack-p", ItemType: "ssh_password", Provider: "remote_ssh", CreatedAt: at(5),
					Metadata: with(map[string]any{"name": "remote-ssh:stack-p", "stack_id": "stack-p", "secret": sealed(t, knownSSHPassword)})},
				{ID: "orphan-1", TenantID: "org_a", ItemType: "api_key", CreatedAt: at(6),
					Metadata: map[string]any{"name": "No owner", "secret": ciphertext}},
				{ID: "split-1", TenantID: "org_a", ItemType: "api_key", CreatedAt: at(7),
					Metadata: map[string]any{"owner_id": "auth0|alice", "user_id": "auth0|mallory", "secret": ciphertext}},
				// A reader that leaked a row from another tenant must not
				// make it eligible here.
				{ID: "foreign-1", TenantID: "org_b", ItemType: "api_key", CreatedAt: at(8),
					Metadata: with(map[string]any{"secret": ciphertext})},
			},
			"org_b": {
				{ID: "b-cert", TenantID: "org_b", ItemType: "certificate", CreatedAt: at(9),
					Metadata: map[string]any{"owner_id": "auth0|bob", "secret": ciphertext}},
			},
		},
		stacks: map[string][]fakeStack{
			"org_a": {
				{id: "stack-p", owner: "auth0|alice", config: map[string]any{"server_remote_host": "203.0.113.5", "server_remote_user": "root", "server_remote_auth_method": "password"}},
				{id: "stack-k", owner: "auth0|alice", config: map[string]any{"metadata": map[string]any{"server_remote_host": "203.0.113.6", "server_remote_user": "root", "server_remote_ssh_key_label": "PROD"}}},
				{id: "stack-x", owner: "auth0|alice", config: map[string]any{"server_remote_host": "203.0.113.7", "server_remote_user": "root", "server_remote_credential_ref": "missing"}},
				{id: "stack-local", owner: "auth0|alice", config: map[string]any{"name": "no remote target"}},
			},
		},
	}
	return reader, ciphertext
}

func TestPlannerReportsEligibilityQuarantineCallersAndOperations(t *testing.T) {
	reader, _ := plannerFixture(t)
	fixed := time.Date(2026, 10, 10, 9, 0, 0, 0, time.UTC)
	report, plans, err := Planner{Reader: reader, Now: func() time.Time { return fixed }}.Plan(context.Background(), []string{"org_a", "org_b", "org_a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(plans) != report.Totals.Rows {
		t.Fatalf("plans %d, report rows %d", len(plans), report.Totals.Rows)
	}
	wantTotals := Totals{Rows: 9, Eligible: 6, Quarantined: 3, LegacyPlaintext: 1, Derived: 1}
	if report.Totals != wantTotals {
		t.Fatalf("totals = %+v, want %+v", report.Totals, wantTotals)
	}
	if !reflect.DeepEqual(report.Tenants, []string{"org_a", "org_b"}) || report.Mode != ModeDryRun {
		t.Fatalf("tenants %v mode %s", report.Tenants, report.Mode)
	}
	wantQuarantine := []QuarantineGroup{
		{Reason: ReasonTenantMismatch, Items: []ItemRef{{"org_b", "foreign-1"}}},
		{Reason: ReasonOwnerConflict, Items: []ItemRef{{"org_a", "split-1"}}},
		{Reason: ReasonOwnerMissing, Items: []ItemRef{{"org_a", "orphan-1"}}},
	}
	if !reflect.DeepEqual(report.Quarantine, wantQuarantine) {
		t.Fatalf("quarantine = %+v", report.Quarantine)
	}
	if !reflect.DeepEqual(report.LegacyPlaintext, []ItemRef{{"org_a", "plain-1"}}) {
		t.Fatalf("legacy plaintext = %+v", report.LegacyPlaintext)
	}
	wantKinds := []KindCount{
		{Kind: "api_key", Target: ResourceGenericSecret, Count: 1},
		{Kind: "certificate", Target: ResourceGenericSecret, Count: 1},
		{Kind: "password", Target: ResourceLogin, Count: 1},
		{Kind: "ssh_key", Target: ResourceNodeAccessProfile, Count: 2},
		{Kind: "ssh_password", Target: ResourceNodeAccessProfile, Count: 1},
	}
	if !reflect.DeepEqual(report.Kinds, wantKinds) {
		t.Fatalf("kinds = %+v", report.Kinds)
	}
	wantLabels := []LabelResolution{
		{TenantID: "org_a", StackID: "stack-k", Kind: "ssh_key", Outcome: LabelAmbiguous, ItemIDs: []string{"key-new", "key-old"}},
		{TenantID: "org_a", StackID: "stack-p", Kind: "ssh_password", Outcome: LabelResolved, ItemIDs: []string{"stack-p:remote-ssh"}},
		{TenantID: "org_a", StackID: "stack-x", Kind: "ssh_key", Outcome: LabelUnresolved},
	}
	if !reflect.DeepEqual(report.LabelResolutions, wantLabels) {
		t.Fatalf("label resolutions = %+v", report.LabelResolutions)
	}
	if !reflect.DeepEqual(report.LabelCollisions, []LabelCollision{{TenantID: "org_a", Kind: "ssh_key", ItemIDs: []string{"key-new", "key-old"}}}) {
		t.Fatalf("label collisions = %+v", report.LabelCollisions)
	}
	wantOps := Operations{WalletCaptures: 6, WalletReadBacks: 6, LegacyDecryptions: 6, PlaintextValues: 1, SourceTombstones: 6, CallerSwitches: 2, WalletTotal: 12}
	if report.Operations != wantOps {
		t.Fatalf("operations = %+v, want %+v", report.Operations, wantOps)
	}
}

func TestPlannerOutputCarriesNoValue(t *testing.T) {
	reader, ciphertext := plannerFixture(t)
	report, plans, err := Planner{Reader: reader}.Plan(context.Background(), []string{"org_a", "org_b"})
	if err != nil {
		t.Fatal(err)
	}
	var text bytes.Buffer
	if writeErr := report.WriteText(&text); writeErr != nil {
		t.Fatal(writeErr)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	plansJSON, err := json.Marshal(plans)
	if err != nil {
		t.Fatal(err)
	}
	outputs := map[string]string{
		"text report": text.String(),
		"json report": string(reportJSON),
		"plans json":  string(plansJSON),
		"plans fmt":   fmt.Sprintf("%v %+v %#v %s", plans, plans, plans, plans),
	}
	for name, output := range outputs {
		for _, value := range []string{knownSecret, knownTOTP, ciphertext, knownOldKey, knownNewKey, knownSSHPassword} {
			if strings.Contains(output, value) {
				t.Errorf("%s contains a stored value", name)
			}
		}
	}
	// The report names rows by id only: no labels, usernames or notes.
	for _, output := range []string{text.String(), string(reportJSON)} {
		for _, label := range []string{knownUsername, knownNotes, "Grafana", "auth0|alice"} {
			if strings.Contains(output, label) {
				t.Errorf("report contains %q", label)
			}
		}
	}
	// The value stays readable for the execute path through Open alone.
	for _, plan := range plans {
		if plan.Source.ItemID != "login-1" {
			continue
		}
		opened, err := plan.Target.Values.Secret.Open(testEncryptor(t).Decrypt)
		if err != nil || opened != knownSecret {
			t.Fatalf("Open = %v", err)
		}
	}
}

func TestPlannerFailsClosedOnAReadError(t *testing.T) {
	reader, _ := plannerFixture(t)
	reader.failFor = "org_b"
	report, plans, err := Planner{Reader: reader}.Plan(context.Background(), []string{"org_a", "org_b"})
	if err == nil || report.Totals.Rows != 0 || plans != nil {
		t.Fatalf("Plan = %+v, %d plans, %v; want an error and no partial report", report.Totals, len(plans), err)
	}
}
