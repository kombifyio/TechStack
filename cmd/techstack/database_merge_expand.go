package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"regexp"

	"github.com/kombifyio/techstack/pkg/db"
)

// The pinned producer authenticates the original merge and Local receipt; this
// child independently verifies its body-bound GitHub OIDC identity. It receives
// no OIDC request or context-signing custody.
func runDatabaseMergeExpand(ctx context.Context) error {
	sha := regexp.MustCompile(`^[a-f0-9]{40}$`)
	digest := regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	if os.Getenv("GITHUB_ACTIONS") != "true" ||
		!sha.MatchString(buildRevision) || os.Getenv("SOURCE_SHA") != buildRevision ||
		os.Getenv("TECHSTACK_SCHEMA_MANIFEST_SHA256") != db.DeliveryForwardManifestDigest() ||
		!digest.MatchString(os.Getenv("TECHSTACK_LOCAL_RECEIPT_SHA256")) ||
		os.Getenv(db.EnvDatabaseMigrationMode) != "apply" || os.Getenv(db.EnvAllowDBGenesis) != "false" ||
		db.ReleaseChannel() != "stable" ||
		os.Getenv("ACTIONS_ID_TOKEN_REQUEST_TOKEN") != "" || os.Getenv("ACTIONS_ID_TOKEN_REQUEST_URL") != "" ||
		os.Getenv("TECHSTACK_DB_CONTEXT_KEY") != "" || os.Getenv("TECHSTACK_DB_CONTEXT_KEY_ID") != "" ||
		os.Getenv("TECHSTACK_ADVANCED_ISSUER_DATABASE_URL") != "" || "" != "" ||
		os.Getenv("DATABASE_URL") != "" {
		return errDatabaseDeliveryAdmission
	}
	authorityProof, err := verifySchemaExpandAuthority(ctx)
	if err != nil || authorityProof.SourceSHA != buildRevision || authorityProof.ManifestSHA256 != db.DeliveryForwardManifestDigest() ||
		authorityProof.LocalReceiptSHA256 != os.Getenv("TECHSTACK_LOCAL_RECEIPT_SHA256") {
		return errDatabaseDeliveryAdmission
	}
	identity, pinned, err := db.ExpectedDatabaseIdentityFromEnv()
	if err != nil || !pinned || identity.DatabaseName != "kombify_runtime" || identity.SystemIdentifier == "" {
		return errDatabaseDeliveryAdmission
	}
	host, err := db.DeliveryForwardDatabaseHost()
	if err != nil {
		return err
	}
	providers := map[string]string{
		"kombify-prd-pg-runtime-52af3c33.pgz4r1vc79kp5qv1ezdb68ahqx.c0.eu-central-1.aws.pg.clickhouse.cloud": "clickhouse-postgres:b6140b1d-3833-455a-b515-729055d3140d:f9301db0-e99d-8ad0-bec2-efb56642a37e:migrations",
		"kombify-prd-pg-core-c42ec78c.pgdszt7an6ee5h1w660thxd8hb.c0.eu-central-1.aws.pg.clickhouse.cloud":    "clickhouse-postgres:b6140b1d-3833-455a-b515-729055d3140d:6e7fa3aa-a673-8ad0-8878-630351eb5115:migrations",
	}
	if providers[host] == "" || providers[host] != authorityProof.ProviderResource {
		return errDatabaseDeliveryAdmission
	}
	dsn, err := url.Parse(os.Getenv(db.EnvMigrationDatabaseURL))
	if err != nil || dsn.User == nil || dsn.User.Username() != "kombify_runtime_user" ||
		dsn.Hostname() != host || dsn.Path != "/"+identity.DatabaseName ||
		(dsn.Scheme != "postgres" && dsn.Scheme != "postgresql") || dsn.Fragment != "" ||
		(dsn.Port() != "" && dsn.Port() != "5432") || dsn.Query().Get("sslmode") != "verify-full" || dsn.Query().Get("sslrootcert") == "" {
		return errDatabaseDeliveryAdmission
	}
	if password, ok := dsn.User.Password(); !ok || password == "" {
		return errDatabaseDeliveryAdmission
	}
	query, err := url.ParseQuery(dsn.RawQuery)
	if err != nil {
		return errDatabaseDeliveryAdmission
	}
	for name, values := range query {
		if len(values) != 1 || values[0] == "" {
			return errDatabaseDeliveryAdmission
		}
		switch name {
		case "sslmode", "sslrootcert", "application_name", "connect_timeout", "channel_binding":
		default:
			return errDatabaseDeliveryAdmission
		}
	}
	cfg, err := db.MigrationConfigFromEnv()
	if err != nil {
		return err
	}
	authority, err := db.OpenContext(ctx, cfg)
	if err != nil {
		return err
	}
	defer authority.Close()
	batch, err := db.DeliveryForwardBatch()
	if err != nil {
		return err
	}
	before, err := authority.InspectForwardExpand(ctx, batch)
	if err != nil {
		return err
	}
	applied, err := authority.MigrateForwardExpand(ctx, batch)
	if err != nil {
		return err
	}
	after, err := authority.InspectForwardExpand(ctx, batch)
	if err != nil {
		return err
	}
	if applied.AppliedFrom != before.State.AppliedPrefix || before.AdmissibleEnd != applied.AppliedEnd ||
		before.Held != applied.Held || after.State.AppliedPrefix != applied.AppliedEnd ||
		after.AdmissibleEnd != applied.AppliedEnd || after.Held != applied.Held {
		return errors.New("forward expand ledger changed; inspect without replay")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.merge-expand/v1", "status": "verified", "source_sha": buildRevision,
		"manifest_sha256": db.DeliveryForwardManifestDigest(), "local_receipt_sha256": os.Getenv("TECHSTACK_LOCAL_RECEIPT_SHA256"),
		"database_identity": identity.DatabaseName + "@" + identity.SystemIdentifier,
		"applied":           applied, "before": before, "after": after,
	})
}
