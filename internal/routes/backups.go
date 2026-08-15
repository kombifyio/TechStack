// Package routes provides custom HTTP routes for kombifyTechstack API.
package routes

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/backup"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/pocketbase/pocketbase/core"
)

const (
	backupDefaultDir             = "./backups"
	backupDefaultRetention       = 7
	backupNamePathKey            = "name"
	backupNamePrefix             = "techstack_backup_"
	backupNameSuffix             = ".tar.gz"
	backupErrCodeS3NotConfigured = api.ErrorCode("S3_NOT_CONFIGURED")
)

// BackupConfig holds backup configuration.
type BackupConfig struct {
	DataDir   string // Path to PocketBase data directory
	BackupDir string // Path to store backups
	Retention int    // Number of backups to keep

	// S3 configuration (optional)
	S3Enabled   bool
	S3Bucket    string
	S3Endpoint  string
	S3AccessKey string
	S3SecretKey string
	S3Region    string
	S3Prefix    string
}

// BackupInfo represents metadata about a backup.
type BackupInfo struct {
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	SizeHuman string    `json:"size_human"`
	CreatedAt time.Time `json:"created_at"`
	Path      string    `json:"path"`
	S3Path    string    `json:"s3_path,omitempty"`
}

func requireAdmin(e *httpx.Event) error {
	// The boolean is the admission signal; the error alone is not. Both are
	// checked so this wrapper cannot report success for a refused caller - the
	// bug that let GET /metrics serve its payload after writing 403.
	ok, err := requireAdminAccess(e)
	if err != nil {
		return err
	}
	if !ok {
		return httpx.ErrResponseWritten
	}
	return nil
}

func requireAdminAccess(e *httpx.Event) (bool, error) {
	if e != nil && e.Auth != nil && e.Auth.IsSuperuser() {
		return true, nil
	}
	if signedEdgeAdmin(e) {
		return true, nil
	}
	return false, httpx.Reject(e, http.StatusForbidden, api.ErrCodeForbidden, "Admin access required", nil)
}

// RegisterBackupRoutes adds backup management endpoints.
// These endpoints require admin authentication.
func RegisterBackupRoutes(r *httpx.Router, app core.App, config BackupConfig) {
	log := logger.Get()
	config = normalizeBackupConfig(config, log)
	handlers := backupRouteHandlers{
		app:      app,
		config:   config,
		log:      log,
		s3Client: newBackupS3Client(config, log),
	}

	r.POST("/api/v1/backups", handlers.create)
	r.GET("/api/v1/backups", handlers.list)
	r.GET("/api/v1/backups/{name}/download", handlers.download)
	r.DELETE("/api/v1/backups/{name}", handlers.delete)
	r.POST("/api/v1/backups/cleanup", handlers.cleanup)
	r.GET("/api/v1/backups/latest", handlers.latest)
	r.POST("/api/v1/backups/{name}/upload-s3", handlers.uploadS3)
	r.GET("/api/v1/backups/s3", handlers.listS3)
	r.POST("/api/v1/backups/s3/{name}/download", handlers.downloadS3)
	r.DELETE("/api/v1/backups/s3/{name}", handlers.deleteS3)
	r.GET("/api/v1/backups/s3/status", handlers.s3Status)
}

type backupRouteHandlers struct {
	app      core.App
	config   BackupConfig
	log      *logger.Logger
	s3Client *backup.S3Client
}

func (h backupRouteHandlers) create(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}

	info, err := createBackup(h.app, h.config)
	if err != nil {
		h.log.Error("Failed to create backup", "error", err)
		return backupInternalError(e, "Failed to create backup", err)
	}

	h.log.Info("Backup created", "name", info.Name, "size", info.SizeHuman)
	return httpx.Success(e, http.StatusCreated, info)
}

func (h backupRouteHandlers) list(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}

	backups, err := listBackups(h.config.BackupDir)
	if err != nil {
		return backupInternalError(e, "Failed to list backups", err)
	}
	return httpx.Success(e, http.StatusOK, backupListResponse(backups, h.config.Retention))
}

func (h backupRouteHandlers) download(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}

	name, backupPath, ok, err := h.localBackupFromPath(e)
	if !ok || err != nil {
		return err
	}
	if _, err := os.Stat(backupPath); err != nil {
		return httpx.Error(e, http.StatusNotFound, api.ErrCodeNotFound, "Backup not found", nil)
	}

	e.Response.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	e.Response.Header().Set("Content-Type", "application/gzip")
	http.ServeFile(e.Response, e.Request, backupPath)
	return nil
}

func (h backupRouteHandlers) delete(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}

	name, backupPath, ok, err := h.localBackupFromPath(e)
	if !ok || err != nil {
		return err
	}
	if err := os.Remove(backupPath); err != nil {
		if os.IsNotExist(err) {
			return httpx.Error(e, http.StatusNotFound, api.ErrCodeNotFound, "Backup not found", nil)
		}
		return httpx.Error(e, http.StatusInternalServerError, api.ErrCodeInternal, "Failed to delete backup", nil)
	}

	h.log.Info("Backup deleted", "name", name)
	return httpx.Success(e, http.StatusOK, map[string]string{routeMessageField: "Backup deleted successfully", backupNamePathKey: name})
}

func (h backupRouteHandlers) cleanup(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}

	deleted, err := applyRetention(h.config.BackupDir, h.config.Retention)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, api.ErrCodeInternal, "Failed to apply retention", nil)
	}
	return httpx.Success(e, http.StatusOK, backupRetentionResponse(deleted, h.config.Retention))
}

func (h backupRouteHandlers) latest(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}

	backups, err := listBackups(h.config.BackupDir)
	if err != nil {
		return httpx.Error(e, http.StatusInternalServerError, api.ErrCodeInternal, "Failed to list backups", nil)
	}
	if len(backups) == 0 {
		return httpx.Error(e, http.StatusNotFound, api.ErrCodeNotFound, "No backups found", nil)
	}
	return httpx.Success(e, http.StatusOK, backups[0])
}

func (h backupRouteHandlers) uploadS3(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}
	if err := h.requireS3(e); err != nil {
		return err
	}

	name, backupPath, ok, err := h.localBackupFromPath(e)
	if !ok || err != nil {
		return err
	}
	if _, err := os.Stat(backupPath); err != nil {
		return httpx.Error(e, http.StatusNotFound, api.ErrCodeNotFound, "Backup not found", nil)
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 10*time.Minute)
	defer cancel()
	if err := h.s3Client.Upload(ctx, backupPath, name); err != nil {
		h.log.Error("Failed to upload backup to S3", "name", name, "error", err)
		return backupInternalError(e, "Failed to upload to S3", err)
	}

	s3Path := h.s3Client.Prefix() + name
	h.log.Info("Backup uploaded to S3", "name", name, "s3_path", s3Path)
	return httpx.Success(e, http.StatusOK, map[string]any{routeMessageField: "Backup uploaded to S3 successfully", backupNamePathKey: name, "s3_path": s3Path})
}

func (h backupRouteHandlers) listS3(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}
	if err := h.requireS3(e); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()
	objects, err := h.s3Client.List(ctx, backupNamePrefix)
	if err != nil {
		return backupInternalError(e, "Failed to list S3 backups", err)
	}
	return httpx.Success(e, http.StatusOK, map[string]any{"backups": objects, "total": len(objects), "bucket": h.s3Client.Bucket()})
}

func (h backupRouteHandlers) downloadS3(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}
	if err := h.requireS3(e); err != nil {
		return err
	}

	name, ok, err := backupNameFromPath(e)
	if !ok || err != nil {
		return err
	}
	localPath := filepath.Join(h.config.BackupDir, name)

	ctx, cancel := context.WithTimeout(e.Request.Context(), 10*time.Minute)
	defer cancel()
	if err := h.s3Client.Download(ctx, name, localPath); err != nil {
		h.log.Error("Failed to download backup from S3", "name", name, "error", err)
		return backupInternalError(e, "Failed to download from S3", err)
	}

	h.log.Info("Backup downloaded from S3", "name", name, "local_path", localPath)
	return httpx.Success(e, http.StatusOK, map[string]any{routeMessageField: "Backup downloaded from S3 successfully", backupNamePathKey: name, "local_path": localPath})
}

func (h backupRouteHandlers) deleteS3(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}
	if err := h.requireS3(e); err != nil {
		return err
	}

	name, ok, err := backupNameFromPath(e)
	if !ok || err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(e.Request.Context(), 30*time.Second)
	defer cancel()
	if err := h.s3Client.Delete(ctx, name); err != nil {
		h.log.Error("Failed to delete backup from S3", "name", name, "error", err)
		return backupInternalError(e, "Failed to delete from S3", err)
	}

	h.log.Info("Backup deleted from S3", "name", name)
	return httpx.Success(e, http.StatusOK, map[string]string{routeMessageField: "Backup deleted from S3 successfully", backupNamePathKey: name})
}

func (h backupRouteHandlers) s3Status(e *httpx.Event) error {
	if ok, err := requireAdminAccess(e); !ok || err != nil {
		return err
	}
	return httpx.Success(e, http.StatusOK, backupS3StatusResponse(h.config, h.s3Client))
}

func (h backupRouteHandlers) requireS3(e *httpx.Event) error {
	if h.s3Client == nil {
		return httpx.Error(e, http.StatusServiceUnavailable, backupErrCodeS3NotConfigured, "S3 is not configured", nil)
	}
	return nil
}

func (h backupRouteHandlers) localBackupFromPath(e *httpx.Event) (string, string, bool, error) {
	name, ok, err := backupNameFromPath(e)
	if !ok || err != nil {
		return "", "", ok, err
	}
	return name, filepath.Join(h.config.BackupDir, name), true, nil
}

func normalizeBackupConfig(config BackupConfig, log *logger.Logger) BackupConfig {
	if config.BackupDir == "" {
		config.BackupDir = backupDefaultDir
	}
	if err := os.MkdirAll(config.BackupDir, 0750); err != nil {
		log.Error("Failed to create backup directory", "error", err)
	}
	if config.Retention <= 0 {
		config.Retention = backupDefaultRetention
	}
	return config
}

func newBackupS3Client(config BackupConfig, log *logger.Logger) *backup.S3Client {
	if !config.S3Enabled || config.S3Bucket == "" {
		return nil
	}
	client, err := backup.NewS3Client(backup.S3Config{
		Bucket:    config.S3Bucket,
		Endpoint:  config.S3Endpoint,
		AccessKey: config.S3AccessKey,
		SecretKey: config.S3SecretKey,
		Region:    config.S3Region,
		Prefix:    config.S3Prefix,
	})
	if err != nil {
		log.Warn("Failed to initialize S3 client for backup routes", "error", err)
		return nil
	}
	log.Info("S3 client initialized for backup routes", "bucket", config.S3Bucket)
	return client
}

func backupNameFromPath(e *httpx.Event) (string, bool, error) {
	rawName := e.Request.PathValue(backupNamePathKey)
	if rawName == "" {
		return "", false, httpx.Error(e, http.StatusBadRequest, api.ErrCodeBadRequest, "Backup name required", nil)
	}
	name := filepath.Base(rawName)
	if rawName != name || strings.ContainsAny(rawName, `/\`) ||
		!strings.HasPrefix(name, backupNamePrefix) || !strings.HasSuffix(name, backupNameSuffix) {
		return "", false, httpx.Error(e, http.StatusBadRequest, api.ErrCodeBadRequest, "Invalid backup name", nil)
	}
	return name, true, nil
}

func backupInternalError(e *httpx.Event, message string, err error) error {
	return httpx.Error(e, http.StatusInternalServerError, api.ErrCodeInternal, message, map[string]any{
		routeErrorField: err.Error(),
	})
}

func backupListResponse(backups []BackupInfo, retention int) map[string]any {
	return map[string]any{
		"backups":   backups,
		"total":     len(backups),
		"retention": retention,
	}
}

func backupRetentionResponse(deleted []string, retention int) map[string]any {
	return map[string]any{
		routeMessageField: "Retention policy applied",
		"deleted":         deleted,
		"retention":       retention,
	}
}

func backupS3StatusResponse(config BackupConfig, s3Client *backup.S3Client) map[string]any {
	status := map[string]any{
		"enabled":    s3Client != nil,
		"configured": config.S3Enabled,
	}
	if s3Client != nil {
		status["bucket"] = s3Client.Bucket()
		status["prefix"] = s3Client.Prefix()
	}
	return status
}
