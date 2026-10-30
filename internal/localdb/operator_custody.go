package localdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/jackc/pgx/v5"
)

const operatorRecordName = "operator.json"

// This custody belongs only to the native supervisor. The serving child gets
// neither this handle nor the embedded-postgres configuration containing it.
type operatorCustody struct {
	root          *os.Root
	lock          *os.File
	record        operatorRecord
	startupNonce  string
	dataDirectory string
}

type operatorRecord struct {
	Version          int    `json:"version"`
	State            string `json:"state"`
	Password         string `json:"password"`
	PhysicalIdentity string `json:"physical_identity"`
}

func randomCredential() (string, error) {
	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return "", errors.New("generate native database credential")
	}
	defer clear(secret)
	return hex.EncodeToString(secret), nil
}

func openOperatorCustody(base string, adoptIdentity string) (*operatorCustody, error) {
	root, err := privatefs.Open(filepath.Join(base, "operator"))
	if err != nil {
		return nil, err
	}
	c := &operatorCustody{root: root}
	success := false
	defer func() {
		if !success {
			c.close()
		}
	}()
	c.lock, err = privatefs.OpenFile(root, "profile.lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := lockProfile(c.lock); err != nil {
		return nil, errors.New("native database profile is already in use")
	}
	raw, err := privatefs.ReadFile(root, operatorRecordName)
	if err == nil {
		if json.Unmarshal(raw, &c.record) != nil || c.record.Version != 1 || !validNativeCredential(c.record.Password) ||
			(c.record.State != "initializing" && c.record.State != "legacy-pending" && c.record.State != "active") {
			return nil, errors.New("native database operator custody is invalid; restore its private record")
		}
		if c.record.State != "initializing" && !validNativeIdentity(c.record.PhysicalIdentity) {
			return nil, errors.New("native database physical identity is missing")
		}
	} else if errors.Is(err, os.ErrNotExist) {
		entries, inspectErr := os.ReadDir(filepath.Join(base, "data"))
		if inspectErr != nil && !errors.Is(inspectErr, os.ErrNotExist) {
			return nil, inspectErr
		}
		existing := len(entries) > 0
		if existing && !validNativeIdentity(adoptIdentity) {
			return nil, errors.New("existing native database has no private operator record; use techstack local-database adopt --identity <database@system_identifier> after verifying the installation")
		}
		if !existing && adoptIdentity != "" {
			return nil, errors.New("legacy adoption requires an existing native database")
		}
		password, err := randomCredential()
		if err != nil {
			return nil, err
		}
		c.record = operatorRecord{Version: 1, State: "initializing", Password: password}
		if existing {
			c.record.State, c.record.PhysicalIdentity = "legacy-pending", adoptIdentity
		}
		// Persist intent before init/rotation. On ambiguity the next start tries
		// this exact credential first; it never generates another candidate.
		if err := c.save(); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if adoptIdentity != "" && c.record.PhysicalIdentity != adoptIdentity {
		return nil, errors.New("legacy adoption identity differs from the private record")
	}
	success = true
	return c, nil
}

func validNativeCredential(value string) bool {
	if len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	clear(decoded)
	return err == nil
}

func validNativeIdentity(identity string) bool {
	name, system, found := strings.Cut(identity, "@")
	if !found || name != embeddedDatabase || len(system) < 10 || len(system) > 24 {
		return false
	}
	for _, digit := range system {
		if digit < '0' || digit > '9' {
			return false
		}
	}
	return true
}

func (c *operatorCustody) save() error {
	raw, err := json.Marshal(c.record)
	if err != nil {
		return err
	}
	return privatefs.Replace(c.root, operatorRecordName, append(raw, '\n'), 0o600)
}

func (c *operatorCustody) close() {
	if c.lock != nil {
		_ = c.lock.Close()
		c.lock = nil
	}
	if c.root != nil {
		_ = c.root.Close()
		c.root = nil
	}
}

func (c *operatorCustody) qualify(ctx context.Context, dsn string, legacyDSN string) error {
	conn, err := connectNativeOperator(ctx, dsn)
	if err != nil {
		if c.record.State != "legacy-pending" || sqlState(err) != "28P01" {
			return errors.New("native database operator authentication failed; custody was not changed")
		}
		conn, err = pgx.Connect(ctx, legacyDSN)
		if err != nil {
			return errors.New("explicit legacy database authentication failed")
		}
		defer conn.Close(context.Background())
		if err := c.verifyIdentity(ctx, conn); err != nil {
			return err
		}
		var safe bool
		if err := conn.QueryRow(ctx, `SELECT current_user=$1 AND session_user=$1 AND r.rolsuper
		 AND (SELECT pg_get_userbyid(datdba)=$2 FROM pg_database WHERE datname=current_database())
		 AND (SELECT pg_get_userbyid(nspowner)=$2 FROM pg_namespace WHERE nspname='public')
		 FROM pg_roles r WHERE rolname=current_user`, embeddedUsername, embeddedRuntimeOwner).Scan(&safe); err != nil || !safe {
			return errors.New("legacy database is not the recognized native owner installation; nothing was rotated")
		}
		verifier, err := scramVerifier(c.record.Password)
		if err != nil {
			return err
		}
		if _, err := conn.Exec(ctx, "ALTER ROLE "+pgx.Identifier{embeddedUsername}.Sanitize()+" PASSWORD "+quoteLiteral(verifier)); err != nil {
			return errors.New("legacy credential rotation outcome unknown; preserve operator intent and inspect authentication before retry")
		}
		// A fresh authenticated read resolves the rotation; a dropped response
		// leaves the private intent intact and never triggers a blind replay.
		verified, err := pgx.Connect(ctx, dsn)
		if err != nil {
			return errors.New("rotated native operator credential readback failed")
		}
		defer verified.Close(context.Background())
		if err := c.verifyIdentity(ctx, verified); err != nil {
			return err
		}
	} else {
		defer conn.Close(context.Background())
		if err := c.verifyIdentity(ctx, conn); err != nil {
			return err
		}
	}
	c.record.State = "active"
	return c.save()
}

// Only connection refusal or PostgreSQL's explicit starting-up status while
// this supervisor starts its loopback server is a readiness condition.
// Authentication, TLS and other network failures return
// unchanged; only qualify's explicit28P01 branch may try the legacy credential.
func connectNativeOperator(ctx context.Context, dsn string) (*pgx.Conn, error) {
	for {
		conn, err := pgx.Connect(ctx, dsn)
		if err == nil || (!errors.Is(err, syscall.ECONNREFUSED) && sqlState(err) != "57P03") {
			return conn, err
		}
		timer := time.NewTimer(100 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *operatorCustody) verifyIdentity(ctx context.Context, conn *pgx.Conn) error {
	var database, system, cluster, directory string
	if err := conn.QueryRow(ctx, "SELECT current_database(),system_identifier::text,current_setting('cluster_name'),current_setting('data_directory') FROM pg_catalog.pg_control_system()").Scan(&database, &system, &cluster, &directory); err != nil {
		return errors.New("native database physical identity read failed")
	}
	actualDirectory, actualErr := os.Stat(directory)
	expectedDirectory, expectedErr := os.Stat(c.dataDirectory)
	sameDirectory := actualErr == nil && expectedErr == nil && actualDirectory.IsDir() && os.SameFile(actualDirectory, expectedDirectory)
	if c.startupNonce == "" || cluster != c.startupNonce || actualErr != nil || expectedErr != nil || !sameDirectory {
		return errors.New("native operator connection is not this supervisor's running database; no credential was changed")
	}
	identity := database + "@" + system
	if !validNativeIdentity(identity) || (c.record.PhysicalIdentity != "" && c.record.PhysicalIdentity != identity) {
		return fmt.Errorf("native database differs from the private physical identity pin")
	}
	if expected := strings.TrimSpace(os.Getenv("TECHSTACK_EXPECTED_DATABASE_IDENTITY")); expected != "" && expected != identity {
		return errors.New("native database differs from the configured physical identity; no role was changed")
	}
	c.record.PhysicalIdentity = identity
	return nil
}
