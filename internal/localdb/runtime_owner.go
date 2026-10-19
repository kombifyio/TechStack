package localdb

import (
	"context"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// embeddedRuntimeOwner is the login role the local runtime migrates and runs
// as. Migrations 153 to 155 refuse a superuser or BYPASSRLS migration owner,
// so the bootstrap superuser only prepares this role and never serves the app.
const embeddedRuntimeOwner = "techstack_owner"

// ensureRuntimeOwner makes embeddedRuntimeOwner a NOSUPERUSER NOBYPASSRLS
// login that owns the database, every non-system schema and every object in
// them, then returns its DSN. A fresh random password is set on every start
// and held only in memory; the statement carries a SCRAM verifier, never the
// password. It is idempotent: a data dir migrated by the bootstrap superuser
// (or by an earlier start) is re-owned in place before the posture is proven.
func ensureRuntimeOwner(ctx context.Context, adminDSN string, port uint32) (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", fmt.Errorf("generate local runtime owner credential: %w", err)
	}
	password := hex.EncodeToString(secret)
	clear(secret)
	verifier, err := scramVerifier(password)
	if err != nil {
		return "", err
	}

	conn, err := pgx.Connect(ctx, adminDSN)
	if err != nil {
		return "", fmt.Errorf("connect embedded postgres bootstrap role: %w", err)
	}
	defer conn.Close(context.Background())

	tx, err := conn.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin local runtime owner setup: %w", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()

	role := pgx.Identifier{embeddedRuntimeOwner}.Sanitize()
	attributes := " LOGIN NOSUPERUSER NOBYPASSRLS NOCREATEROLE NOCREATEDB NOREPLICATION INHERIT PASSWORD " + quoteLiteral(verifier)
	var exists bool
	if err := tx.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_roles WHERE rolname=$1)", embeddedRuntimeOwner).Scan(&exists); err != nil {
		return "", fmt.Errorf("inspect local runtime owner: %w", err)
	}
	statement := "CREATE ROLE " + role + attributes
	if exists {
		statement = "ALTER ROLE " + role + attributes
	}
	if _, err := tx.Exec(ctx, statement); err != nil {
		return "", fmt.Errorf("prepare local runtime owner role failed (SQLSTATE %s)", sqlState(err))
	}
	if _, err := tx.Exec(ctx, "ALTER DATABASE "+pgx.Identifier{embeddedDatabase}.Sanitize()+" OWNER TO "+role); err != nil {
		return "", fmt.Errorf("transfer local database ownership: %w", err)
	}
	// REASSIGN OWNED cannot run for the bootstrap superuser (it owns system
	// objects), so each user object outside the system schemas moves alone.
	if _, err := tx.Exec(ctx, reownUserObjectsSQL(embeddedRuntimeOwner)); err != nil {
		return "", fmt.Errorf("transfer local schema object ownership: %w", err)
	}
	if err := verifyRuntimeOwnerPosture(ctx, tx); err != nil {
		return "", err
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit local runtime owner setup: %w", err)
	}

	dsn := (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(embeddedRuntimeOwner, password),
		Host:     "127.0.0.1:" + strconv.FormatUint(uint64(port), 10),
		Path:     "/" + embeddedDatabase,
		RawQuery: "sslmode=disable",
	}).String()
	if err := verifyRuntimeOwnerLogin(ctx, dsn); err != nil {
		return "", err
	}
	return dsn, nil
}

func reownUserObjectsSQL(owner string) string {
	return `DO $techstack_local_owner$
DECLARE
    target text := ` + quoteLiteral(owner) + `;
    target_oid oid := (SELECT oid FROM pg_catalog.pg_roles WHERE rolname = target);
    item record;
BEGIN
    FOR item IN SELECT n.nspname FROM pg_catalog.pg_namespace n
        WHERE n.nspowner <> target_oid AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
          AND n.nspname NOT LIKE 'pg\_temp\_%' AND n.nspname NOT LIKE 'pg\_toast\_temp\_%' LOOP
        EXECUTE format('ALTER SCHEMA %I OWNER TO %I', item.nspname, target);
    END LOOP;
    FOR item IN SELECT c.oid, c.relkind FROM pg_catalog.pg_class c
        JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
        WHERE c.relowner <> target_oid AND c.relkind IN ('r','p','v','m','f','S')
          AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
          AND n.nspname NOT LIKE 'pg\_temp\_%' AND n.nspname NOT LIKE 'pg\_toast\_temp\_%'
          AND NOT (c.relkind = 'S' AND EXISTS (SELECT 1 FROM pg_catalog.pg_depend d
              WHERE d.classid = 'pg_catalog.pg_class'::regclass AND d.objid = c.oid
                AND d.refclassid = 'pg_catalog.pg_class'::regclass AND d.deptype IN ('a','i'))) LOOP
        EXECUTE format('ALTER %s %s OWNER TO %I',
            CASE item.relkind WHEN 'v' THEN 'VIEW' WHEN 'm' THEN 'MATERIALIZED VIEW'
                WHEN 'f' THEN 'FOREIGN TABLE' WHEN 'S' THEN 'SEQUENCE' ELSE 'TABLE' END,
            item.oid::regclass, target);
    END LOOP;
    FOR item IN SELECT p.oid, p.prokind FROM pg_catalog.pg_proc p
        JOIN pg_catalog.pg_namespace n ON n.oid = p.pronamespace
        WHERE p.proowner <> target_oid
          AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
          AND n.nspname NOT LIKE 'pg\_temp\_%' AND n.nspname NOT LIKE 'pg\_toast\_temp\_%' LOOP
        EXECUTE format('ALTER %s %s OWNER TO %I',
            CASE item.prokind WHEN 'a' THEN 'AGGREGATE' ELSE 'ROUTINE' END,
            item.oid::regprocedure, target);
    END LOOP;
    FOR item IN SELECT t.oid, t.typtype FROM pg_catalog.pg_type t
        JOIN pg_catalog.pg_namespace n ON n.oid = t.typnamespace
        WHERE t.typowner <> target_oid AND t.typtype IN ('e','d','r','c')
          AND (t.typtype <> 'c' OR (SELECT c.relkind FROM pg_catalog.pg_class c WHERE c.oid = t.typrelid) = 'c')
          AND n.nspname NOT IN ('pg_catalog','information_schema','pg_toast')
          AND n.nspname NOT LIKE 'pg\_temp\_%' AND n.nspname NOT LIKE 'pg\_toast\_temp\_%' LOOP
        EXECUTE format('ALTER %s %s OWNER TO %I',
            CASE item.typtype WHEN 'd' THEN 'DOMAIN' ELSE 'TYPE' END, item.oid::regtype, target);
    END LOOP;
END $techstack_local_owner$`
}

// verifyRuntimeOwnerPosture proves, before commit, the exact owner posture the
// migration guards check: no superuser or BYPASSRLS attribute, ownership of
// the database and the public schema, and no user object left to anyone else.
func verifyRuntimeOwnerPosture(ctx context.Context, tx pgx.Tx) error {
	var privileged, ownsDatabase, ownsPublic bool
	var foreignObjects int
	err := tx.QueryRow(ctx, `
WITH owner AS (SELECT oid, rolsuper, rolbypassrls FROM pg_catalog.pg_roles WHERE rolname = $1),
user_ns AS (SELECT oid FROM pg_catalog.pg_namespace
    WHERE nspname NOT IN ('pg_catalog','information_schema','pg_toast')
      AND nspname NOT LIKE 'pg\_temp\_%' AND nspname NOT LIKE 'pg\_toast\_temp\_%')
SELECT o.rolsuper OR o.rolbypassrls,
    EXISTS(SELECT 1 FROM pg_catalog.pg_database WHERE datname = current_database() AND datdba = o.oid),
    EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname = 'public' AND nspowner = o.oid),
    (SELECT count(*) FROM pg_catalog.pg_namespace WHERE oid IN (SELECT oid FROM user_ns) AND nspowner <> o.oid)
    + (SELECT count(*) FROM pg_catalog.pg_class WHERE relnamespace IN (SELECT oid FROM user_ns) AND relowner <> o.oid)
    + (SELECT count(*) FROM pg_catalog.pg_proc WHERE pronamespace IN (SELECT oid FROM user_ns) AND proowner <> o.oid)
    + (SELECT count(*) FROM pg_catalog.pg_type WHERE typnamespace IN (SELECT oid FROM user_ns) AND typowner <> o.oid)
FROM owner o`, embeddedRuntimeOwner).Scan(&privileged, &ownsDatabase, &ownsPublic, &foreignObjects)
	if err != nil {
		return fmt.Errorf("verify local runtime owner posture: %w", err)
	}
	if privileged || !ownsDatabase || !ownsPublic || foreignObjects != 0 {
		return fmt.Errorf("local runtime owner posture is not the exact non-bypass migration owner (privileged=%t database=%t public=%t foreign_objects=%d)",
			privileged, ownsDatabase, ownsPublic, foreignObjects)
	}
	return nil
}

func verifyRuntimeOwnerLogin(ctx context.Context, dsn string) error {
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("local runtime owner cannot log in: %w", redactConnectError(err))
	}
	defer conn.Close(context.Background())
	var login string
	if err := conn.QueryRow(ctx, "SELECT current_user").Scan(&login); err != nil || login != embeddedRuntimeOwner {
		return fmt.Errorf("local runtime owner login resolved to an unexpected role")
	}
	return nil
}

// scramVerifier builds the PostgreSQL SCRAM-SHA-256 verifier for password so
// the plaintext never appears in a statement (or a server log line).
func scramVerifier(password string) (string, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate local runtime owner salt: %w", err)
	}
	const iterations = 4096
	salted, err := pbkdf2.Key(sha256.New, password, salt, iterations, sha256.Size)
	if err != nil {
		return "", fmt.Errorf("derive local runtime owner verifier: %w", err)
	}
	defer clear(salted)
	clientMAC := hmac.New(sha256.New, salted)
	clientMAC.Write([]byte("Client Key"))
	clientKey := clientMAC.Sum(nil)
	defer clear(clientKey)
	storedKey := sha256.Sum256(clientKey)
	serverMAC := hmac.New(sha256.New, salted)
	serverMAC.Write([]byte("Server Key"))
	serverKey := serverMAC.Sum(nil)
	enc := base64.StdEncoding
	return fmt.Sprintf("SCRAM-SHA-256$%d:%s$%s:%s", iterations, enc.EncodeToString(salt),
		enc.EncodeToString(storedKey[:]), enc.EncodeToString(serverKey)), nil
}

// quoteLiteral quotes a value made only of base64/hex/identifier characters.
func quoteLiteral(value string) string {
	out := make([]byte, 0, len(value)+2)
	out = append(out, '\'')
	for i := 0; i < len(value); i++ {
		if value[i] == '\'' {
			out = append(out, '\'')
		}
		out = append(out, value[i])
	}
	return string(append(out, '\''))
}

func sqlState(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return "unknown"
}

// redactConnectError keeps only the failure class: a connect error can echo
// connection parameters.
func redactConnectError(err error) error {
	return fmt.Errorf("connect failed (SQLSTATE %s)", sqlState(err))
}

const runtimeOwnerSetupTimeout = 60 * time.Second
