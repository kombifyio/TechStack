package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/url"
	"os"

	"github.com/kombifyio/techstack/pkg/db"
)

var errExistingRolePreparationPolicy = errors.New("existing role preparation requires verify-only schema and no genesis")

// The governed db-role workflow reuses the existing capability installers. It
// cannot migrate schema, reconcile provider grants or issue a signing identity.
func prepareExistingPurposeRoles(ctx context.Context) error {
	if os.Getenv(db.EnvDatabaseMigrationMode) != "verify" || os.Getenv(db.EnvAllowDBGenesis) != "false" {
		return errExistingRolePreparationPolicy
	}
	cfg, err := advancedIssuerBootstrapConfigFromEnv()
	if err != nil {
		return err
	}
	issuerURL, err := url.Parse(cfg.issuerDSN)
	if err != nil || issuerURL.User == nil || cfg.appRole != "techstack_application" || issuerURL.User.Username() != "techstack_advanced_issuer" {
		return errors.New("hosted role preparation requires the declared application and signing principals")
	}
	authority, err := db.OpenContext(ctx, cfg.migration)
	if err != nil {
		return err
	}
	defer authority.Close()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, authority.DB); err != nil {
		return err
	}
	// Both checks precede role creation. A schema gap stays with migrations;
	// absent or unreadable retained custody stays with its existing authority.
	if err = authority.VerifyMigrations(ctx); err != nil {
		return err
	}
	issuerID, keyID, err := verifyRetainedIssuerBeforePreparation(ctx, authority, cfg)
	if err != nil {
		return err
	}
	if err = prepareApplicationPrincipal(ctx, authority, cfg); err != nil {
		return err
	}
	// This mode deliberately does not select the identity-initialization branch
	// of the existing preparation function. It prepares only login and grants.
	if err = prepareAdvancedIssuerFromConfig(ctx, authority, cfg); err != nil {
		return err
	}
	observedIssuer, observedKey, err := verifyRetainedIssuerBeforePreparation(ctx, authority, cfg)
	if err != nil {
		return err
	}
	if observedIssuer != issuerID || observedKey != keyID {
		return errors.New("existing role preparation changed retained issuer custody")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"schema": "techstack.credential-preparation/v1", "operation": "prepare-existing-roles", "status": "acknowledged",
		"source_sha": buildRevision, "application_role": cfg.appRole, "signing_role": issuerURL.User.Username(),
		"database_identity": os.Getenv(db.EnvExpectedDatabaseIdentity), "issuer_id": issuerID, "key_id": keyID,
		"wrapping_verified": true, "schema_applied": false, "signing_identity_issued": false,
	})
}
