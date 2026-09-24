package homeassistant

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/kombifyio/techstack/pkg/auth"
	_ "github.com/jackc/pgx/v5/stdlib"
	"net/url"
	"os"
	"testing"
	"time"
)

// Sensitive native-operation boundary: restart must adopt the original claim
// and recovery key without generating another backup or crossing tenants.
func TestJournalRetainsClaimAndEncryptedKeyAcrossRestart(t *testing.T) {
	dsn := os.Getenv("TECHSTACK_TEST_POSTGRES_URL")
	if dsn == "" {
		t.Skip("dedicated test Postgres not configured")
	}
	ctx := context.Background()
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	schema := fmt.Sprintf("ha_journal_%d", time.Now().UnixNano())
	if _, err = admin.ExecContext(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer admin.ExecContext(ctx, "DROP SCHEMA "+schema+" CASCADE")
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := parsed.Query()
	q.Set("search_path", schema)
	parsed.RawQuery = q.Encode()
	db, err := sql.Open("pgx", parsed.String())
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.ExecContext(ctx, "CREATE TABLE ril_workflow_runs (type text)"); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("../../pkg/db/migrations/108_home_assistant_workflows.sql")
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
	first := &Journal{DB: db, Encryptor: enc, TenantID: "tenant-a"}
	fresh, _, err := first.Claim(ctx, "native-backup")
	if err != nil || !fresh {
		t.Fatal("initial native operation was not claimed")
	}
	password, err := first.RecoveryKey(ctx, "native-backup")
	if err != nil {
		t.Fatal(err)
	}
	if err = first.Save(ctx, "native-backup", map[string]any{"job_id": "native-job"}); err != nil {
		t.Fatal(err)
	}
	restarted := &Journal{DB: db, Encryptor: enc, TenantID: "tenant-a"}
	fresh, receipt, err := restarted.Claim(ctx, "native-backup")
	if err != nil || fresh || receipt["job_id"] != "native-job" {
		t.Fatal("restart did not recover original submission")
	}
	recovered, err := restarted.ReadRecoveryKey(ctx, "native-backup")
	if err != nil || recovered != password {
		t.Fatal("recovery key changed on restart")
	}
	var stored string
	if err = db.QueryRowContext(ctx, "SELECT password_enc FROM home_assistant_recovery_keys").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == password || !auth.IsEncrypted(stored) {
		t.Fatal("recovery password persisted without encryption")
	}
	foreign := &Journal{DB: db, Encryptor: enc, TenantID: "tenant-b"}
	if _, err = foreign.ReadRecoveryKey(ctx, "native-backup"); err == nil {
		t.Fatal("foreign tenant obtained source recovery key")
	}
	if _, err = restarted.ReadRecoveryKey(ctx, "missing"); err == nil {
		t.Fatal("missing source key was fabricated")
	}
}
