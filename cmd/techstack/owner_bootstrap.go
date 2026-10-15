package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/gocommon/authsession"
	"github.com/kombifyio/techstack/internal/breakglass"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"golang.org/x/term"
)

// This command runs under the operator's host/container account. It never
// starts the server, creates a database/tenant, migrates, or emits telemetry.
func runOwnerBootstrap(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "bootstrap" {
		return errors.New("usage: techstack owner bootstrap")
	}
	cfg, err := config.Load("")
	if err != nil {
		return err
	}
	if cfg.Edition != config.EditionSelfHostOSS || !cfg.DeploymentMode.IsSelfHosted() {
		return errors.New("owner bootstrap requires the selfhost-oss installation")
	}
	if !term.IsTerminal(int(os.Stdout.Fd())) {
		return errors.New("owner bootstrap requires an operator terminal; refusing redirected secret output")
	}
	service, release, err := openOperatorBootstrapService(ctx)
	if err != nil {
		return err
	}
	defer release()
	proof, replaced, err := localOperatorBootstrapProof(ctx, service)
	if err != nil {
		return err
	}
	if replaced {
		fmt.Fprintln(os.Stderr, "A new bootstrap password replaced the previous one. The previous bootstrap password no longer works.")
	}
	fmt.Fprintln(os.Stderr, "Use this bootstrap password once in local owner setup. It is not available over HTTP. Keep it private.")
	if _, err = fmt.Fprintln(os.Stdout, proof); err != nil {
		return errors.New("owner bootstrap could not write the password to the operator terminal; run the command again to replace it")
	}
	return nil
}

func openOperatorBootstrapService(ctx context.Context) (service *authlocal.Service, release func(), err error) {
	const activeStatus = "active"
	locked := strings.EqualFold(strings.TrimSpace(os.Getenv("TECHSTACK_BREAKGLASS_LOCKED")), "true")
	if locked {
		return nil, nil, authlocal.ErrLockedByOperator
	}
	dbConfig, err := db.ConfigFromEnv()
	if err != nil {
		return nil, nil, errors.New("owner bootstrap requires the existing installation database configuration")
	}
	database, err := db.Open(dbConfig)
	if err != nil {
		return nil, nil, errors.New("owner bootstrap could not open the existing installation database")
	}
	closeDatabase := func() { _ = database.Close() }
	defer func() {
		if err != nil {
			closeDatabase()
		}
	}()
	if err = db.VerifyExpectedDatabaseIdentity(ctx, database.DB); err != nil {
		return nil, nil, errors.New("owner bootstrap installation database identity was not verified")
	}
	tenant := v2DefaultTenantIDFromEnv()
	authority := controlplane.NewPostgresStore(database.DB)
	existingTenant, err := authority.GetTenant(ctx, tenant)
	if err != nil || existingTenant.Kind != "self_hosted" || existingTenant.Status != activeStatus {
		return nil, nil, errors.New("owner bootstrap requires an existing active self-hosted tenant")
	}
	store := breakglass.NewPostgres(authority, tenant)
	sessions, err := authsession.NewManager(authsession.Config{
		Audience: v2SessionAudienceFromEnv(), Secret: []byte(os.Getenv("TECHSTACK_V2_SESSION_SECRET")),
	})
	if err != nil {
		return nil, nil, errors.New("owner bootstrap requires the installation session configuration")
	}
	service, err = authlocal.New(authlocal.Config{Store: store, Sessions: sessions, DefaultTenantID: tenant, ClaimLocked: locked})
	return service, closeDatabase, err
}

func localOperatorBootstrapProof(ctx context.Context, service *authlocal.Service) (string, bool, error) {
	status, err := service.CurrentStatus(ctx)
	if err != nil {
		return "", false, err
	}
	if !status.Initialized {
		return "", false, authlocal.ErrNotFound
	}
	if status.Locked {
		return "", false, authlocal.ErrLockedByOperator
	}
	if status.Claimed {
		return "", false, authlocal.ErrAlreadyClaimed
	}
	result, err := service.Reveal(ctx)
	if err == nil {
		return result.Password, false, nil
	}
	if !errors.Is(err, authlocal.ErrPasswordExpired) {
		return "", false, err
	}
	proof, err := service.ReissueBootstrap(ctx)
	return proof, err == nil, err
}
