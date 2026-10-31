package main

import (
	"context"
	"database/sql"
	"os"
	"strings"

	"github.com/kombifyio/techstack/pkg/diskcustody"
	"github.com/kombifyio/techstack/pkg/logger"
)

// startDiskCustodyImport hands the durable data-directory items (host-key
// pins, stack spec files and an archive copy) to the runtime database in the
// background (kombify-Techstack-6vse8). Readiness does not wait: until the
// import finishes the host-key store still honors the file's pins and a job
// adopts its stack's local spec files itself.
func startDiskCustodyImport(ctx context.Context, pool *sql.DB, log *logger.Logger) {
	dataDir := strings.TrimSpace(os.Getenv("TECHSTACK_DATA_DIR"))
	if dataDir == "" {
		dataDir = "data"
	}
	if info, err := os.Stat(dataDir); err != nil || !info.IsDir() {
		return
	}
	go func() {
		if _, err := diskcustody.Import(ctx, pool, dataDir, log.Logger); err != nil {
			log.Error("disk_custody_import_failed", "error", err)
		}
	}()
}
