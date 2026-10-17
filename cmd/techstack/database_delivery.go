package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"

	"github.com/kombifyio/techstack/pkg/db"
)

var errDatabaseDeliveryAdmission = errors.New("database delivery requires the owning compiled production publish authority")

func runDatabaseDelivery(ctx context.Context, args []string) error {
	if len(args) != 1 || (args[0] != "inspect" && args[0] != "apply" && args[0] != "verify") {
		return errDatabaseDeliveryAdmission
	}
	apply := args[0] == "apply"
	if os.Getenv(db.EnvAllowDBGenesis) != "false" || db.ReleaseChannel() == "preview" {
		return errDatabaseDeliveryAdmission
	}
	if apply {
		if os.Getenv(db.EnvDatabaseMigrationMode) != "apply" || os.Getenv("DELIVERY_OPERATION") != "publish" ||
			os.Getenv("DELIVERY_ARTIFACT") != "database-schema" || os.Getenv("SOURCE_REPOSITORY") != "kombify-Techstack" ||
			os.Getenv("SOURCE_SHA") != buildRevision || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(buildRevision) ||
			!regexp.MustCompile(`^sha256:[a-f0-9]{64}$`).MatchString(os.Getenv("DELIVERY_PLAN_DIGEST")) ||
			os.Getenv("DELIVERY_DATABASE_ADMITTED") != "true" {
			return errDatabaseDeliveryAdmission
		}
	} else if os.Getenv(db.EnvDatabaseMigrationMode) != "verify" {
		return errDatabaseDeliveryAdmission
	}
	pin, pinned, err := db.ExpectedDatabaseIdentityFromEnv()
	if err != nil || !pinned || pin.SystemIdentifier == "" {
		return errDatabaseDeliveryAdmission
	}
	batch, err := db.DeliveryForwardBatch()
	if err != nil {
		return err
	}
	cfg, err := advancedIssuerBootstrapConfigFromEnv()
	if err != nil {
		return err
	}
	host, err := db.DeliveryForwardDatabaseHost()
	if err != nil {
		return err
	}
	for _, dsn := range []string{cfg.application.DSN, cfg.migration.DSN, cfg.issuerDSN} {
		uri, err := url.Parse(dsn)
		if err != nil || uri.Hostname() != host || uri.Query().Get("sslmode") != "verify-full" {
			return errors.New("database delivery target or transport differs")
		}
	}
	authority, err := db.OpenContext(ctx, cfg.migration)
	if err != nil {
		return err
	}
	defer authority.Close()
	if err := db.VerifyExpectedDatabaseIdentity(ctx, authority.DB); err != nil {
		return err
	}
	issuerID, keyID, err := verifyRetainedIssuerBeforePreparation(ctx, authority, cfg)
	if err != nil {
		return err
	}
	if apply {
		err = authority.MigrateForwardBatch(ctx, batch, verifyDeliveryMigrationPosture)
		if err == nil {
			err = authority.VerifyMigrations(ctx)
		}
	} else if args[0] == "verify" {
		err = authority.VerifyMigrations(ctx)
		if err == nil {
			tx, txErr := authority.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
			if txErr != nil {
				return txErr
			}
			defer tx.Rollback()
			err = verifyDeliveryMigrationPosture(ctx, tx)
		}
	} else {
		err = authority.VerifyForwardBatch(ctx, batch)
	}
	if err != nil {
		return err
	}
	observedIssuer, observedKey, err := verifyRetainedIssuerBeforePreparation(ctx, authority, cfg)
	if err != nil {
		return err
	}
	if observedIssuer != issuerID || observedKey != keyID {
		return errors.New("database delivery retained issuer changed")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.database-delivery/v1", "operation": args[0], "source_sha": buildRevision,
		"migrations": batch, "database_effects": apply, "status": "acknowledged", "plan_digest": os.Getenv("DELIVERY_PLAN_DIGEST"),
		"database_identity": os.Getenv(db.EnvExpectedDatabaseIdentity), "issuer_id": issuerID, "key_id": keyID,
		"issuer_preserved": true, "wrapping_verified": true, "role_or_provider_mutations": false,
	})
}

func verifyDeliveryMigrationPosture(ctx context.Context, tx *sql.Tx) error {
	var safe bool
	err := tx.QueryRowContext(ctx, `SELECT NOT rolsuper AND NOT rolbypassrls
		FROM pg_roles WHERE rolname=current_user`).Scan(&safe)
	if err != nil {
		return err
	}
	if !safe {
		return errors.New("database migration owner bypasses tenant authority")
	}
	// Migration157 closes the helper's implicit PUBLIC EXECUTE. Verify that
	// effective capability before the entire batch and ledger can commit.
	err = tx.QueryRowContext(ctx, `SELECT NOT EXISTS (
		SELECT 1 FROM pg_proc p, LATERAL aclexplode(COALESCE(p.proacl,acldefault('f',p.proowner))) a
		WHERE p.oid='public.techstack_subject_ref(text)'::regprocedure
		AND a.grantee=0 AND a.privilege_type='EXECUTE')`).Scan(&safe)
	if err != nil {
		return err
	}
	if !safe {
		return errors.New("subject helper retains public execution")
	}
	return nil
}
