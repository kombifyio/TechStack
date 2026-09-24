package homeassistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

type memoryCustody struct {
	receipts map[string]map[string]any
	keys     map[string]string
}

func (m *memoryCustody) Claim(_ context.Context, k string) (bool, map[string]any, error) {
	r, ok := m.receipts[k]
	if !ok {
		r = map[string]any{}
		m.receipts[k] = r
	}
	return !ok, r, nil
}
func (m *memoryCustody) Save(_ context.Context, k string, v map[string]any) error {
	m.receipts[k] = v
	return nil
}
func (m *memoryCustody) RecoveryKey(_ context.Context, k string) (string, error) {
	if m.keys[k] == "" {
		m.keys[k] = "native-backup-secret"
	}
	return m.keys[k], nil
}
func (m *memoryCustody) ReadRecoveryKey(_ context.Context, k string) (string, error) {
	if m.keys[k] == "" {
		return "", errors.New("missing key")
	}
	return m.keys[k], nil
}

type memoryArchive struct{ data map[string][]byte }

func (m *memoryArchive) Put(_ context.Context, k string, r io.Reader) (backupstore.ArchiveReceipt, error) {
	k = "archive:" + k
	data, err := io.ReadAll(r)
	if err != nil {
		return backupstore.ArchiveReceipt{}, err
	}
	sum := sha256.Sum256(data)
	m.data[k] = data
	return backupstore.ArchiveReceipt{ObjectKey: k, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}, nil
}
func (m *memoryArchive) Open(_ context.Context, r backupstore.ArchiveReceipt) (io.ReadCloser, error) {
	data, ok := m.data[r.ObjectKey]
	if !ok {
		return nil, errors.New("missing archive")
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}
func (m *memoryArchive) Verify(_ context.Context, r backupstore.ArchiveReceipt) error {
	data, ok := m.data[r.ObjectKey]
	sum := sha256.Sum256(data)
	if !ok || hex.EncodeToString(sum[:]) != r.SHA256 {
		return errors.New("archive mismatch")
	}
	return nil
}

type migrationRuntimeProbe struct {
	sourceStopped, targetStopped bool
	refuseStop                   bool
	targetStarts                 int
}

func (m *migrationRuntimeProbe) PrepareIsolated(context.Context) (bool, error) { return true, nil }
func (m *migrationRuntimeProbe) VerifyIsolation(context.Context) (bool, error) { return true, nil }
func (m *migrationRuntimeProbe) StopSource(context.Context) (bool, error) {
	if m.refuseStop {
		return false, errors.New("source stop denied")
	}
	m.sourceStopped = true
	return true, nil
}
func (m *migrationRuntimeProbe) StartSource(context.Context) (bool, error) {
	if !m.targetStopped {
		return false, errors.New("dual control")
	}
	m.sourceStopped = false
	return true, nil
}
func (m *migrationRuntimeProbe) StopTarget(context.Context) (bool, error) {
	m.targetStopped = true
	return true, nil
}
func (m *migrationRuntimeProbe) StartTarget(context.Context) (bool, error) {
	if !m.sourceStopped {
		return false, errors.New("dual control")
	}
	m.targetStarts++
	m.targetStopped = false
	return true, nil
}
func (m *migrationRuntimeProbe) TransferToTarget(context.Context) (bool, error) {
	return m.sourceStopped, nil
}
func (m *migrationRuntimeProbe) TransferToSource(context.Context) (bool, error) {
	return m.targetStopped, nil
}
func (m *migrationRuntimeProbe) SourceStopped(context.Context) (bool, error) {
	return m.sourceStopped, nil
}
func (m *migrationRuntimeProbe) TargetStopped(context.Context) (bool, error) {
	return m.targetStopped, nil
}

// Sensitive migration boundary: exercise native HTTP effects, retained archive
// receipts and explicit cutover; a denied source stop must never activate target.
func TestNativeMigrationAndReturnRetainArchives(t *testing.T) {
	ctx := context.Background()
	backupPosts := 0
	coreVersion := "2026.9.1"
	updatePosts := 0
	osVersion := "18.1"
	osUpdatePosts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer native-token" {
			w.WriteHeader(401)
			return
		}
		var data any
		switch r.URL.Path {
		case "/api/states":
			_, _ = w.Write([]byte(`[{"entity_id":"automation.welcome"},{"entity_id":"sensor.template"}]`))
			return
		case "/api/config":
			_ = json.NewEncoder(w).Encode(map[string]any{"version": coreVersion, "components": []string{"automation", "template"}})
			return
		case "/core/update":
			updatePosts++
			coreVersion = "2026.9.2"
			data = map[string]any{}
		case "/core/info":
			data = map[string]any{"version": "2026.9.1", "arch": "amd64"}
		case "/os/info":
			data = map[string]any{"version": osVersion}
		case "/os/update":
			osUpdatePosts++
			data = map[string]any{}
		case "/backups/new/full":
			backupPosts++
			data = map[string]any{"job_id": "j1", "slug": "abcd1234"}
		case "/jobs/j1":
			data = map[string]any{"done": true, "errors": []any{}}
		case "/backups/abcd1234/info":
			data = map[string]any{"slug": "abcd1234", "type": "full", "protected": true, "homeassistant": "2026.9.1"}
		case "/backups/abcd1234/download":
			_, _ = w.Write([]byte("native-encrypted-archive"))
			return
		case "/backups/new/upload":
			_, _ = io.Copy(io.Discard, r.Body)
			data = map[string]any{"slug": "abcd1234"}
		case "/backups/abcd1234/restore/full":
			data = map[string]any{"job_id": "j1"}
		default:
			t.Errorf("unexpected native endpoint %s", r.URL.Path)
			w.WriteHeader(404)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"result": "ok", "data": data})
	}))
	defer server.Close()
	core, err := NewLocalClient(ctx, server.URL, "native-token")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()
	supervisor, err := NewLocalSupervisor(ctx, server.URL, "native-token", Grants{Backup: true, Restore: true})
	if err != nil {
		t.Fatal(err)
	}
	defer supervisor.Close()
	runtime := &migrationRuntimeProbe{}
	binding := &MigrationBinding{OwnerID: "owner", SourceServiceID: "source", TargetBindingRef: "target", AuthorizationRef: "grant", DependencyAssessmentRef: "dependency-review", SourceCore: core, TargetCore: core, SourceSupervisor: supervisor, TargetSupervisor: supervisor, Custody: &memoryCustody{receipts: map[string]map[string]any{}, keys: map[string]string{}}, Archives: &memoryArchive{data: map[string][]byte{}}, Runtime: runtime}
	activities := workflow.NewActivityRunner()
	for name, fn := range binding.Activities() {
		activities.Register(name, fn)
	}
	rc := &workflow.RunContext{Run: &workflow.Run{RunID: "migration", OwnerID: "owner", Input: map[string]any{"source_service_id": "source", "target_binding_ref": "target", "authorization_ref": "grant"}}, Activities: activities}
	drive := func(steps []workflow.StepDef) {
		t.Helper()
		for _, step := range steps {
			out, err := step.Run(ctx, rc)
			if err != nil {
				t.Fatalf("%s: %v", step.Name, err)
			}
			if out.Suspend != nil {
				if runtime.sourceStopped {
					t.Fatal("source stopped before confirmation")
				}
				rc.Signal = &workflow.Signal{Key: out.Suspend.SignalKey, Payload: map[string]any{"confirmed": true}}
				if _, err = step.Run(ctx, rc); err != nil {
					t.Fatal(err)
				}
				rc.Signal = nil
			}
		}
	}
	drive(workflows.NewHomeAssistantMigrationWorkflow().Steps())
	if !runtime.sourceStopped || runtime.targetStarts != 1 {
		t.Fatal("cutover did not exclusively activate target")
	}
	rc.Run.RunID = "return"
	rc.Run.Input["migration_run_id"] = "migration"
	// The rollback confirmation occurs while the source is intentionally stopped.
	rc.Signal = &workflow.Signal{Key: workflows.HomeAssistantConfirmationKey("return"), Payload: map[string]any{"confirmed": true}}
	for _, step := range workflows.NewHomeAssistantRollbackWorkflow().Steps() {
		if _, err := step.Run(ctx, rc); err != nil {
			t.Fatalf("%s: %v", step.Name, err)
		}
		rc.Signal = nil
	}
	if !runtime.targetStopped || runtime.sourceStopped || backupPosts != 2 {
		t.Fatal("return must retain both native backups and stop target")
	}
	runtime.refuseStop = true
	input := map[string]any{"owner_id": "owner", "request": rc.Run.Input}
	if _, err := binding.Activities()[workflows.ActHAStopSource](ctx, input); err == nil {
		t.Fatal("stop denial ignored")
	}
	if _, err := binding.Activities()[workflows.ActHAStartTarget](ctx, input); err == nil || runtime.targetStarts != 1 {
		t.Fatal("target activated without a stopped source")
	}
	// Standalone updates require a native backup and this exact archive's
	// independently isolated recovery; retries never submit a second update.
	isolated := httptest.NewServer(server.Config.Handler)
	defer isolated.Close()
	recoveryCore, err := NewLocalClient(ctx, isolated.URL, "native-token")
	if err != nil {
		t.Fatal(err)
	}
	defer recoveryCore.Close()
	recoverySupervisor, err := NewLocalSupervisor(ctx, isolated.URL, "native-token", Grants{Restore: true, Backup: true})
	if err != nil {
		t.Fatal(err)
	}
	defer recoverySupervisor.Close()
	managed, err := NewLocalSupervisor(ctx, server.URL, "native-token", Grants{Backup: true, Restore: true, Update: true})
	if err != nil {
		t.Fatal(err)
	}
	defer managed.Close()
	instance := &InstanceBinding{BindingRef: "source", Origin: "new", ManagementScope: "managed", Core: core, Supervisor: managed, Custody: &memoryCustody{receipts: map[string]map[string]any{}, keys: map[string]string{}}, Archives: &memoryArchive{data: map[string][]byte{}}, DependencyAssessmentRef: "dependency-review"}
	if _, err = instance.Backup(ctx, "before-update"); err != nil {
		t.Fatal(err)
	}
	if _, err = instance.Update(ctx, "update", "2026.9.2", "before-update"); err == nil || updatePosts != 0 {
		t.Fatal("update without recovery was accepted")
	}
	recovery := *binding
	recovery.TargetCore, recovery.TargetSupervisor = recoveryCore, recoverySupervisor
	recovery.Runtime = &migrationRuntimeProbe{}
	if out, err := instance.Restore(ctx, "recovery", "before-update", &recovery); err != nil || out["complete"] != true {
		t.Fatalf("restore: %v %v", out, err)
	}
	instance.ConfigureGranted = true
	instance.RecoveryBackupOperationID = "before-update"
	baseline, err := instance.ReconcileBaseline(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, gap := range baseline.Gaps {
		if gap == "off_instance_recovery_verification" {
			t.Fatal("verified retained recovery ignored by baseline")
		}
	}
	if out, err := instance.UpdateTarget(ctx, "os-update", "os", "18.2", "before-update"); err != nil || out["reboot_required"] != true {
		t.Fatalf("OS staging: %v %v", out, err)
	}
	if out, err := instance.UpdateTarget(ctx, "os-update", "os", "18.2", "before-update"); err != nil || out["pending"] != true || osUpdatePosts != 1 {
		t.Fatal("OS update retry resubmitted or completed before reboot")
	}
	osVersion = "18.2"
	if out, err := instance.UpdateTarget(ctx, "os-update", "os", "18.2", "before-update"); err != nil || out["complete"] != true || coreVersion != "2026.9.1" {
		t.Fatal("OS observation confused with Core version")
	}
	for i := 0; i < 2; i++ {
		if _, err = instance.Update(ctx, "update", "2026.9.2", "before-update"); err != nil {
			t.Fatal(err)
		}
	}
	if updatePosts != 1 {
		t.Fatal("update retry repeated native submission")
	}
}
