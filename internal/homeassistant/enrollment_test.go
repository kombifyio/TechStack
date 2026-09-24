package homeassistant

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"github.com/kombifyio/techstack/internal/managedstackkit"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/pkg/auth"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestDurableOwnerEnrollmentRequiresOwnerAndRetainsEncryptedAttachment(t *testing.T) {
	dsn := os.Getenv("TECHSTACK_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("dedicated Postgres not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("ha_owner_%d", time.Now().UnixNano())
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, _ := url.Parse(dsn)
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, `CREATE TABLE techstack_tenants(id text PRIMARY KEY); CREATE TABLE stacks(tenant_id text,id text,owner_subject_id text,PRIMARY KEY(tenant_id,id)); CREATE TABLE workers(tenant_id text,id text,approved bool,status text,PRIMARY KEY(tenant_id,id)); CREATE TABLE nodes(tenant_id text,stack_id text,worker_id text);
 INSERT INTO techstack_tenants VALUES('tenant'); INSERT INTO stacks VALUES('tenant','stack','owner'); INSERT INTO workers VALUES('tenant','agent',true,'connected'); INSERT INTO nodes VALUES('tenant','stack','agent')`)
	if err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../pkg/db/migrations/110_home_assistant_owner_bindings.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(ctx, string(migration)); err != nil {
		t.Fatal(err)
	}
	enc, err := auth.NewSecretEncryptor([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	store := &EnrollmentStore{DB: db, Encryptor: enc}
	operations := &OwnerOperations{Enrollments: store}
	input := managedstackkit.OperationsRequest{TenantID: "tenant", StackID: "stack", RuntimeAgentID: "agent", Request: sealedExistingOwnerRequest(t)}
	if _, err = operations.Execute(ctx, input); err == nil {
		t.Fatal("pending enrollment executed without attachment")
	}
	pending, err := store.List(ctx, "tenant", "owner", "stack")
	if err != nil || len(pending) == 0 {
		t.Fatalf("pending enrollment unavailable: %v", err)
	}
	reads := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Error("existing native instance mutated")
		}
		reads++
		_, _ = w.Write([]byte(`{"version":"2026.9.1"}`))
	}))
	defer server.Close()
	req := AttachOwner{StackID: "stack", BindingRef: pending[0].BindingRef, URL: server.URL, Token: "owner-secret"}
	if err = store.Attach(ctx, "tenant", "other", req); err == nil || reads != 0 {
		t.Fatal("foreign owner probed native instance")
	}
	if err = store.Attach(ctx, "tenant", "owner", req); err != nil {
		t.Fatal(err)
	}
	var encrypted string
	if err = db.QueryRowContext(ctx, "SELECT token_enc FROM home_assistant_owner_bindings").Scan(&encrypted); err != nil || strings.Contains(encrypted, req.Token) || !auth.IsEncrypted(encrypted) {
		t.Fatal("token not encrypted")
	}
	if _, err = (&OwnerOperations{Enrollments: &EnrollmentStore{DB: db, Encryptor: enc}}).Execute(ctx, input); err != nil {
		t.Fatalf("restart did not resolve attachment: %v", err)
	}
	before := reads
	if _, err = db.ExecContext(ctx, "UPDATE workers SET approved=false"); err != nil {
		t.Fatal(err)
	}
	if _, err = operations.Execute(ctx, input); err == nil || reads != before {
		t.Fatal("revoked worker resolved retained native token")
	}
	req.Token = "replacement"
	if err = store.Attach(ctx, "tenant", "owner", req); err == nil {
		t.Fatal("original attachment overwritten")
	}
	input.TenantID = "foreign"
	if _, err = operations.Execute(ctx, input); err == nil {
		t.Fatal("foreign tenant resolved owner")
	}
	// The native-address boundary uses retained admission plus authenticated
	// inventory; a same-name or same-IP replacement is not the created guest.
	_, err = db.ExecContext(ctx, `UPDATE workers SET approved=true;
 CREATE TABLE techstack_vm_leases(tenant_id text,id text,owner_subject_id text,lease_json jsonb);
 CREATE TABLE substrate_guest_leases(tenant_id text,lease_id text,substrate_server_id text,operation_id text);
 CREATE TABLE substrate_bindings(tenant_id text,server_id text,worker_id text,enabled bool,revision bigint);
 CREATE TABLE servers(tenant_id text,id text,owner_subject_id text,worker_id text,metadata_json jsonb,connection_state text,lifecycle_state text,last_heartbeat_at timestamptz);
 CREATE TABLE provider_operations(tenant_id text,operation_id text,lease_id text,desired_spec_revision bigint,status text,phase text);
 CREATE TABLE provider_desired_spec_revisions(tenant_id text,lease_id text,revision bigint,spec_json jsonb);
 INSERT INTO techstack_vm_leases VALUES('tenant','lease','owner','{"metadata":{"stack_id":"stack","runtime_offering_id":"haos","substrate_binding_revision":"1"}}');
 INSERT INTO substrate_guest_leases VALUES('tenant','lease','substrate','operation');
 INSERT INTO substrate_bindings VALUES('tenant','substrate','agent',true,1);
 INSERT INTO servers VALUES('tenant','substrate','owner','agent','{}','connected','active',now());
 INSERT INTO provider_operations VALUES('tenant','operation','lease',1,'succeeded','present')`)
	if err != nil {
		t.Fatal(err)
	}
	spec := substrate.ExecutionSpec{Action: "provision", Guest: substrate.GuestSpec{GuestIdentity: substrate.GuestIdentity{ID: 200, Name: "kombify-ha", OperationTag: "kombify-operation-test"}, ProfileID: "haos"}}
	specRaw, _ := json.Marshal(spec)
	if _, err = db.ExecContext(ctx, `INSERT INTO provider_desired_spec_revisions VALUES('tenant','lease',1,$1::jsonb)`, specRaw); err != nil {
		t.Fatal(err)
	}
	images := map[string]substrate.Image{}
	for _, profile := range substrate.Profiles() {
		pin, e := substrate.DefaultImage(profile.ID)
		if e != nil {
			t.Fatal(e)
		}
		images[profile.ID] = pin
	}
	guest := substrate.OwnedGuestObservation{Identity: spec.Guest.GuestIdentity, SpecDigest: spec.Guest.CreationDigest(), ObservedAt: time.Now(), Observation: substrate.GuestObservation{ID: 200, Name: "kombify-ha", Present: true, Status: "running", DiskAttached: true, Addresses: []string{"192.168.1.50"}}}
	writeInventory := func() {
		t.Helper()
		payload, _ := json.Marshal(map[string]any{"inventory_observed_at": time.Now(), "substrate_inventory": substrate.GuardObservation{Node: "pve", MinGuestID: 100, MaxGuestID: 999, Images: images, OwnedGuests: map[int]substrate.OwnedGuestObservation{200: guest}}})
		if _, err = db.ExecContext(ctx, `UPDATE servers SET metadata_json=$1::jsonb`, payload); err != nil {
			t.Fatal(err)
		}
	}
	writeInventory()
	for _, endpoint := range []string{"http://192.168.1.50:8123", "http://192.168.1.50", "http://192.168.1.50:80", "https://192.168.1.50", "https://192.168.1.50:443"} {
		if err = store.VerifyNativeGuestEndpoint(ctx, "tenant", "owner", "stack", "lease", endpoint); err != nil {
			t.Fatalf("admitted guest endpoint %s rejected: %v", endpoint, err)
		}
	}
	if err = store.VerifyNativeGuestEndpoint(ctx, "tenant", "owner", "stack", "lease", "http://192.168.1.50:22"); err == nil {
		t.Fatal("unrelated service port accepted")
	}
	if err = store.VerifyNativeGuestEndpoint(ctx, "tenant", "owner", "stack", "lease", "http://192.168.1.51:8123"); err == nil {
		t.Fatal("foreign instance address accepted")
	}
	guest.SpecDigest = "sha256:" + strings.Repeat("c", 64)
	writeInventory()
	if err = store.VerifyNativeGuestEndpoint(ctx, "tenant", "owner", "stack", "lease", "http://192.168.1.50:8123"); err == nil {
		t.Fatal("replacement guest digest accepted")
	}
	guest.SpecDigest = spec.Guest.CreationDigest()
	guest.ObservedAt = time.Now().Add(-3 * time.Minute)
	writeInventory()
	if err = store.VerifyNativeGuestEndpoint(ctx, "tenant", "owner", "stack", "lease", "http://192.168.1.50:8123"); err == nil {
		t.Fatal("stale guest inventory accepted")
	}
}
