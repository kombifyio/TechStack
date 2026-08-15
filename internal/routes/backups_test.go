package routes

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/logger"
)

func TestRequireAdminAcceptsSignedEdgeAdmin(t *testing.T) {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backups", nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "api-key:techstack-admin",
		Roles:  []string{"admin"},
	}))
	e := &httpx.Event{
		Request:  req,
		Response: httptest.NewRecorder(),
	}

	if err := requireAdmin(e); err != nil {
		t.Fatalf("requireAdmin returned error for signed edge admin: %v", err)
	}
}

func TestRequireAdminRejectsSignedEdgeNonAdmin(t *testing.T) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/backups", nil)
	req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
		UserID: "api-key:techstack-user",
		Roles:  []string{"operator"},
	}))
	e := &httpx.Event{
		Request:  req,
		Response: rec,
	}

	// Asserting nil here is what let a refused caller continue. A rejection is
	// an error now, so `if err != nil { return err }` stops.
	if err := requireAdmin(e); err == nil {
		t.Fatal("requireAdmin must return an error for a non-admin")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("requireAdmin status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestRequireAdminAccessStopsAfterForbiddenResponse(t *testing.T) {
	e, rec := backupRouteTestEvent(http.MethodGet, "/api/v1/backups", "", false)

	ok, err := requireAdminAccess(e)
	if err == nil {
		t.Fatal("requireAdminAccess must return an error once it has written the refusal")
	}
	if ok {
		t.Fatal("requireAdminAccess ok = true, want false")
	}
	if rec.Code != http.StatusForbidden {
		t.Fatalf("requireAdminAccess status = %d, want %d", rec.Code, http.StatusForbidden)
	}
}

func TestBackupNameFromPathValidatesManagedBackupName(t *testing.T) {
	tests := []struct {
		name       string
		pathValue  string
		wantOK     bool
		wantStatus int
	}{
		{name: "valid", pathValue: "techstack_backup_20260515_120000.tar.gz", wantOK: true, wantStatus: http.StatusOK},
		{name: "empty", pathValue: "", wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "wrong prefix", pathValue: "other_backup_20260515_120000.tar.gz", wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "wrong suffix", pathValue: "techstack_backup_20260515_120000.zip", wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "unix traversal", pathValue: "../techstack_backup_20260515_120000.tar.gz", wantOK: false, wantStatus: http.StatusBadRequest},
		{name: "windows traversal", pathValue: `..\techstack_backup_20260515_120000.tar.gz`, wantOK: false, wantStatus: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, rec := backupRouteTestEvent(http.MethodGet, "/api/v1/backups/"+tt.pathValue, tt.pathValue, true)

			got, ok, err := backupNameFromPath(e)
			if err != nil {
				t.Fatalf("backupNameFromPath returned unexpected response error: %v", err)
			}
			if ok != tt.wantOK {
				t.Fatalf("backupNameFromPath ok = %v, want %v", ok, tt.wantOK)
			}
			if rec.Code != tt.wantStatus {
				t.Fatalf("response status = %d, want %d", rec.Code, tt.wantStatus)
			}
			if tt.wantOK && got != tt.pathValue {
				t.Fatalf("backupNameFromPath name = %q, want %q", got, tt.pathValue)
			}
		})
	}
}

func TestNormalizeBackupConfigDefaults(t *testing.T) {
	config := normalizeBackupConfig(BackupConfig{BackupDir: t.TempDir(), Retention: 0}, logger.NewNop())
	if config.Retention != backupDefaultRetention {
		t.Fatalf("retention = %d, want %d", config.Retention, backupDefaultRetention)
	}
	if config.BackupDir == "" {
		t.Fatal("backup dir is empty")
	}
}

func TestBackupRouteLatestReturnsNotFoundForEmptyDir(t *testing.T) {
	handler := backupRouteHandlers{
		config: BackupConfig{BackupDir: t.TempDir(), Retention: backupDefaultRetention},
		log:    logger.NewNop(),
	}
	e, rec := backupRouteTestEvent(http.MethodGet, "/api/v1/backups/latest", "", true)

	if err := handler.latest(e); err != nil {
		t.Fatalf("latest returned unexpected response error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("latest status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestBackupRouteListResponseIncludesRetention(t *testing.T) {
	backupDir := t.TempDir()
	backupPath := filepath.Join(backupDir, "techstack_backup_20260515_120000.tar.gz")
	if err := os.WriteFile(backupPath, []byte("backup"), 0600); err != nil {
		t.Fatalf("write backup fixture: %v", err)
	}

	backups, err := listBackups(backupDir)
	if err != nil {
		t.Fatalf("listBackups: %v", err)
	}
	got := backupListResponse(backups, 3)
	if got["total"] != 1 {
		t.Fatalf("total = %v, want 1", got["total"])
	}
	if got["retention"] != 3 {
		t.Fatalf("retention = %v, want 3", got["retention"])
	}
}

func TestBackupRouteDeleteMissingBackupReturnsNotFound(t *testing.T) {
	handler := backupRouteHandlers{
		config: BackupConfig{BackupDir: t.TempDir(), Retention: backupDefaultRetention},
		log:    logger.NewNop(),
	}
	e, rec := backupRouteTestEvent(http.MethodDelete, "/api/v1/backups/techstack_backup_20260515_120000.tar.gz", "techstack_backup_20260515_120000.tar.gz", true)

	if err := handler.delete(e); err != nil {
		t.Fatalf("delete returned unexpected response error: %v", err)
	}
	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete status = %d, want %d", rec.Code, http.StatusNotFound)
	}
}

func TestBackupRouteS3StatusWithoutClient(t *testing.T) {
	got := backupS3StatusResponse(BackupConfig{}, nil)
	if got["enabled"] != false {
		t.Fatalf("enabled = %v, want false", got["enabled"])
	}
	if got["configured"] != false {
		t.Fatalf("configured = %v, want false", got["configured"])
	}
}

func TestBackupRouteS3UploadRequiresConfiguredClient(t *testing.T) {
	handler := backupRouteHandlers{
		config: BackupConfig{BackupDir: t.TempDir(), Retention: backupDefaultRetention},
		log:    logger.NewNop(),
	}
	e, rec := backupRouteTestEvent(http.MethodPost, "/api/v1/backups/techstack_backup_20260515_120000.tar.gz/upload-s3", "techstack_backup_20260515_120000.tar.gz", true)

	if err := handler.uploadS3(e); err != nil {
		t.Fatalf("uploadS3 returned unexpected response error: %v", err)
	}
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("uploadS3 status = %d, want %d", rec.Code, http.StatusServiceUnavailable)
	}
}

func backupRouteTestEvent(method, target, backupName string, admin bool) (*httpx.Event, *httptest.ResponseRecorder) {
	req := httptest.NewRequest(method, target, nil)
	if backupName != "" {
		req.SetPathValue(backupNamePathKey, backupName)
	}
	if admin {
		req = req.WithContext(identity.NewContext(context.Background(), &identity.Identity{
			UserID: "api-key:techstack-admin",
			Roles:  []string{"admin"},
		}))
	}
	rec := httptest.NewRecorder()
	return &httpx.Event{Request: req, Response: rec}, rec
}
