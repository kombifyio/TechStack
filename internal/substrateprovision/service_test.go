package substrateprovision_test

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"testing"

	"github.com/kombifyio/techstack/internal/providercontrol"
	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/internal/substrateprovision"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

type forbiddenAdmission struct{ t *testing.T }

func (a forbiddenAdmission) AdmitSubstrateGuest(context.Context, providercontrol.NativeProvisionAdmissionRequest) (providercontrol.NativeProvisionAdmissionResult, error) {
	a.t.Fatal("retained or occupied guest reached provider admission")
	return providercontrol.NativeProvisionAdmissionResult{}, errors.New("unexpected admission")
}

// A restarted caller must retrieve retained custody, while changed keys/nodes
// must not allocate a second guest or take over an existing Linux node.
func TestPreparePreservesRetainedGuestAndExistingNode(t *testing.T) {
	dsn := os.Getenv("TECHSTACK_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("isolated PostgreSQL not configured")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*config)
	defer admin.Close()
	schema := "substrate_service_" + uuid.NewString()
	if _, err = admin.ExecContext(t.Context(), "CREATE SCHEMA "+pgx.Identifier{schema}.Sanitize()); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(context.Background(), "DROP SCHEMA "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	config.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*config)
	defer db.Close()
	for _, ddl := range []string{
		`CREATE TABLE stacks(tenant_id text,id text,owner_subject_id text)`,
		`CREATE TABLE nodes(tenant_id text,stack_id text,id text)`,
		`CREATE TABLE techstack_vm_leases(tenant_id text,id text,owner_subject_id text,lease_json jsonb)`,
		`CREATE TABLE substrate_guest_leases(tenant_id text,lease_id text,server_id text,operation_id text,guest_id int)`,
		`CREATE TABLE servers(tenant_id text,stack_id text,node_id text,metadata_json jsonb,lifecycle_state text)`,
	} {
		if _, err = db.ExecContext(t.Context(), ddl); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.ExecContext(t.Context(), `INSERT INTO stacks VALUES('tenant','stack','owner'); INSERT INTO servers VALUES('tenant','stack','linux','{}','active')`); err != nil {
		t.Fatal(err)
	}
	request := substrateprovision.Request{TenantID: "tenant", OwnerID: "owner", StackID: "stack", NodeID: "haos", IdempotencyKey: "request", Placement: substrateprovision.Placement{ServerID: "pve", ProfileID: "haos"}}
	raw, _ := json.Marshal(request)
	digest := sha256.Sum256(raw)
	identity := sha256.Sum256([]byte("tenant\x00owner\x00stack\x00haos"))
	lease := "substrate-" + hex.EncodeToString(identity[:20])
	key := sha256.Sum256([]byte("tenant\x00owner\x00stack\x00request"))
	metadata, _ := json.Marshal(map[string]any{"metadata": map[string]string{"runtime_offering_id": "haos", "substrate_intent_hash": hex.EncodeToString(digest[:]), "substrate_request_key": hex.EncodeToString(key[:])}})
	if _, err = db.ExecContext(t.Context(), `INSERT INTO techstack_vm_leases VALUES('tenant',$1,'owner',$2)`, lease, metadata); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `INSERT INTO substrate_guest_leases VALUES('tenant',$1,'guest','operation',8000)`, lease); err != nil {
		t.Fatal(err)
	}
	service := substrateprovision.New(db, forbiddenAdmission{t})
	got, err := service.Prepare(t.Context(), request)
	if err != nil || got.LeaseID != lease || got.OperationID != "operation" {
		t.Fatalf("lost retained operation: %+v %v", got, err)
	}
	request.NodeID = "another"
	if _, err = service.Prepare(t.Context(), request); !errors.Is(err, providercontrol.ErrNativeAdmissionConflict) {
		t.Fatalf("same key reused on another node: %v", err)
	}
	request.NodeID = "linux"
	request.IdempotencyKey = "new-request"
	if _, err = service.Prepare(t.Context(), request); !errors.Is(err, providercontrol.ErrNativeAdmissionConflict) {
		t.Fatalf("existing Linux node replaced: %v", err)
	}
	if _, err = service.Get(t.Context(), "tenant", "other-owner", lease); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("guest custody crossed owner boundary: %v", err)
	}
	// A successful earlier admission can be missing from the latest Guard
	// inventory. Its retained RAM request still prevents over-allocation.
	for _, ddl := range []string{
		`ALTER TABLE servers ADD COLUMN id text, ADD COLUMN owner_subject_id text, ADD COLUMN worker_id text, ADD COLUMN connection_state text, ADD COLUMN last_heartbeat_at timestamptz`,
		`ALTER TABLE substrate_guest_leases ADD COLUMN substrate_server_id text`,
		`CREATE TABLE substrate_bindings(tenant_id text,server_id text,worker_id text,revision bigint,config_json jsonb,enabled bool)`,
		`CREATE TABLE provider_operations(tenant_id text,operation_id text,lease_id text,desired_spec_revision int)`,
		`CREATE TABLE provider_desired_spec_revisions(tenant_id text,lease_id text,revision int,spec_json jsonb)`,
	} {
		if _, err = db.ExecContext(t.Context(), ddl); err != nil {
			t.Fatal(err)
		}
	}
	images := map[string]substrate.Image{}
	for _, id := range []string{"haos", "ubuntu-24.04"} {
		images[id], _ = substrate.DefaultImage(id)
	}
	observation := substrate.GuardObservation{Node: "pve", MinGuestID: 8000, MaxGuestID: 8999, Images: images, Inventory: substrate.Inventory{Architecture: "amd64", HardwareVirtualization: true, CPU: 8, MemoryAvailable: 8 * 1024 * 1024 * 1024, Storage: []substrate.Storage{{ID: "local", Content: "images,import,vztmpl", Active: 1, Available: 100 * 1024 * 1024 * 1024}}, Networks: []substrate.Network{{Name: "vmbr0", Type: "bridge", Active: 1}}}}
	observed, _ := json.Marshal(observation)
	if _, err = db.ExecContext(t.Context(), `INSERT INTO servers(tenant_id,stack_id,id,owner_subject_id,worker_id,metadata_json,lifecycle_state,connection_state,last_heartbeat_at) VALUES('tenant','stack','pve','owner','guard',jsonb_build_object('server_node_role','substrate','substrate_inventory',$1::jsonb,'inventory_observed_at',clock_timestamp()),'active','connected',clock_timestamp());`, observed); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `INSERT INTO substrate_bindings VALUES('tenant','pve','guard',1,$1,true)`, observed); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `UPDATE substrate_guest_leases SET substrate_server_id='pve'; INSERT INTO servers(tenant_id,id,lifecycle_state) VALUES('tenant','guest','planned')`); err != nil {
		t.Fatal(err)
	}
	if _, err = db.ExecContext(t.Context(), `INSERT INTO provider_operations VALUES('tenant','operation',$1,1)`, lease); err != nil {
		t.Fatal(err)
	}
	reserved, _ := json.Marshal(substrate.ExecutionSpec{Action: "provision", Guest: substrate.GuestSpec{GuestIdentity: substrate.GuestIdentity{ID: 8000}, CPU: 2, MemoryMiB: 6144, DiskGiB: 32, Storage: "local"}})
	if _, err = db.ExecContext(t.Context(), `INSERT INTO provider_desired_spec_revisions VALUES('tenant',$1,1,$2)`, lease, reserved); err != nil {
		t.Fatal(err)
	}
	request.NodeID = "second-haos"
	request.IdempotencyKey = "second-request"
	request.Placement.CPU = 2
	request.Placement.MemoryMiB = 4096
	request.Placement.DiskGiB = 32
	request.Placement.Storage = "local"
	request.Placement.Bridge = "vmbr0"
	if _, err = service.Prepare(t.Context(), request); !errors.Is(err, substrateprovision.ErrInsufficientResources) {
		t.Fatalf("unobserved reservation did not deny RAM over-allocation: %v", err)
	}
}
