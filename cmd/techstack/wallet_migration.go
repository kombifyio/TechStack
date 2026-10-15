package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/auth"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/kombifywallet"
	"github.com/kombifyio/techstack/pkg/walletmigration"
)

const walletMigrationUsage = "usage: techstack wallet-migration plan [--dry-run] --tenant <id> [--tenant <id> ...] [--format text|json]\n" +
	"       techstack wallet-migration execute --tenant <id> [--tenant <id> ...] [--owner <sub> ...] [--limit N] [--format text|json]"

// walletMigrationDeps are the seams of the command: the environment and the
// database. They let the command be exercised without either.
type walletMigrationDeps struct {
	getenv func(string) string
	// openStore opens the installation database, verified, and returns the
	// tenant-scoped store with a release function.
	openStore func(ctx context.Context) (walletmigration.Store, func(), error)
}

// runWalletMigration plans or executes the consolidation of the Techstack
// wallet into the kombify Wallet (Wallet plan slice P3.3). plan reads wallet
// rows and stack credential references inside each named tenant's RLS boundary,
// decrypts nothing and prints ids and counts only. execute moves the eligible
// rows into the Wallet, verified, idempotent and resumable, and prints ids,
// counts and reason codes only.
func runWalletMigration(ctx context.Context, args []string) error {
	return runWalletMigrationWith(ctx, args, os.Stdout, os.Stderr, walletMigrationDeps{getenv: os.Getenv, openStore: openWalletMigrationStore})
}

func runWalletMigrationWith(ctx context.Context, args []string, stdout, stderr io.Writer, deps walletMigrationDeps) error {
	if len(args) == 0 {
		return errors.New(walletMigrationUsage)
	}
	switch args[0] {
	case "plan":
		return runWalletMigrationPlan(ctx, args[1:], stdout, stderr, deps)
	case "execute":
		return runWalletMigrationExecute(ctx, args[1:], stdout, stderr, deps)
	default:
		return errors.New(walletMigrationUsage)
	}
}

func runWalletMigrationPlan(ctx context.Context, args []string, stdout, stderr io.Writer, deps walletMigrationDeps) error {
	flags := flag.NewFlagSet("wallet-migration plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	dryRun := flags.Bool("dry-run", false, "plan without reading any value (default)")
	execute := flags.Bool("execute", false, "refused: use the execute subcommand")
	format := flags.String("format", "text", "report format: text or json")
	var tenants walletMigrationList
	flags.Var(&tenants, "tenant", "tenant id to plan; repeat or separate with commas")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, walletMigrationUsage)
	}
	if flags.NArg() != 0 || (*dryRun && *execute) {
		return errors.New(walletMigrationUsage)
	}
	if *execute {
		return refuseWalletMigration(stderr, walletmigration.ExecuteIsASubcommand())
	}
	if *format != "text" && *format != "json" {
		return errors.New(walletMigrationUsage)
	}
	if len(tenants) == 0 {
		return fmt.Errorf("at least one --tenant is required\n%s", walletMigrationUsage)
	}
	store, release, err := deps.openStore(ctx)
	if err != nil {
		return err
	}
	defer release()
	report, _, err := walletmigration.Planner{Reader: store}.Plan(ctx, tenants)
	if err != nil {
		return err
	}
	if *format == "json" {
		return writeWalletMigrationJSON(stdout, report)
	}
	return report.WriteText(stdout)
}

func runWalletMigrationExecute(ctx context.Context, args []string, stdout, stderr io.Writer, deps walletMigrationDeps) error {
	flags := flag.NewFlagSet("wallet-migration execute", flag.ContinueOnError)
	flags.SetOutput(stderr)
	format := flags.String("format", "text", "report format: text or json")
	limit := flags.Int("limit", 0, "most eligible rows to attempt in this run (0 = all)")
	var tenants, owners walletMigrationList
	flags.Var(&tenants, "tenant", "tenant id to migrate; repeat or separate with commas")
	flags.Var(&owners, "owner", "restrict to rows of this Auth0 subject; repeat or separate with commas")
	if err := flags.Parse(args); err != nil {
		return fmt.Errorf("%w\n%s", err, walletMigrationUsage)
	}
	if flags.NArg() != 0 || *limit < 0 || (*format != "text" && *format != "json") {
		return errors.New(walletMigrationUsage)
	}
	if len(tenants) == 0 {
		return fmt.Errorf("at least one --tenant is required\n%s", walletMigrationUsage)
	}

	// The preconditions are checked before the database is opened: execute
	// writes into the Wallet only, and never falls back to another store.
	wallet, _, err := kombifywallet.Boot(config.ModeSaaS, deps.getenv)
	if err != nil {
		return refuseWalletMigration(stderr, walletmigration.WalletUnavailable(err.Error()))
	}
	if wallet == nil {
		return refuseWalletMigration(stderr, walletmigration.WalletUnavailable(kombifywallet.EnvSource+" is not "+string(kombifywallet.SourceKombify)))
	}
	encryptor, err := auth.NewSecretEncryptor([]byte(deps.getenv(auth.EncryptionKeyEnvVar)))
	if err != nil {
		return refuseWalletMigration(stderr, walletmigration.EncryptionKeyRequired())
	}

	store, release, err := deps.openStore(ctx)
	if err != nil {
		return err
	}
	defer release()
	report, err := walletmigration.Executor{Store: store, Wallet: wallet, Decrypt: encryptor.Decrypt}.Execute(ctx, walletmigration.Options{
		Tenants: tenants, Owners: owners, Limit: *limit,
	})
	if err != nil {
		return err
	}
	if *format == "json" {
		err = writeWalletMigrationJSON(stdout, report)
	} else {
		err = report.WriteText(stdout)
	}
	if err != nil {
		return err
	}
	switch {
	case report.Aborted != "":
		return fmt.Errorf("wallet migration aborted (%s); rerun to resume", report.Aborted)
	case report.NotMoved() > 0:
		return fmt.Errorf("wallet migration incomplete: %d rows or stack references did not move; see the report and rerun", report.NotMoved())
	}
	return nil
}

func refuseWalletMigration(stderr io.Writer, denial *walletmigration.Denial) error {
	_ = json.NewEncoder(stderr).Encode(denial)
	return denial
}

func writeWalletMigrationJSON(w io.Writer, report any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(report)
}

type walletMigrationList []string

func (l *walletMigrationList) String() string { return strings.Join(*l, ",") }

func (l *walletMigrationList) Set(value string) error {
	for _, entry := range strings.Split(value, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			return errors.New("value must not be empty")
		}
		*l = append(*l, entry)
	}
	return nil
}

// openWalletMigrationStore opens the installation database, verifies its
// identity and reads and writes through the existing control-plane store,
// whose queries run inside each tenant's RLS boundary.
func openWalletMigrationStore(ctx context.Context) (walletmigration.Store, func(), error) {
	cfg, err := db.ConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("wallet migration requires the installation database configuration: %w", err)
	}
	if cfg.Backend != db.StoreBackendPostgres {
		return nil, nil, fmt.Errorf("wallet migration reads the control-plane Postgres database: set %s", db.EnvDatabaseURL)
	}
	database, err := db.Open(cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("wallet migration could not open the installation database: %w", err)
	}
	release := func() { _ = database.Close() }
	if err := db.VerifyExpectedDatabaseIdentity(ctx, database.DB); err != nil {
		release()
		return nil, nil, fmt.Errorf("wallet migration database identity was not verified: %w", err)
	}
	return walletMigrationStoreAdapter{store: controlplane.NewPostgresStore(database.DB)}, release, nil
}

// walletMigrationStore is the part of the control-plane store the migration
// uses; *controlplane.PostgresStore satisfies it.
type walletMigrationStore interface {
	ListWalletItems(ctx context.Context, tenantID, stackID string) ([]controlplane.WalletItem, error)
	GetWalletItem(ctx context.Context, tenantID, itemID string) (*controlplane.WalletItem, error)
	RecordWalletItemMigration(ctx context.Context, tenantID, itemID string, expectedUpdatedAt time.Time, metadataKey, kombifyWalletItemID string) error
	ListStacksByTenant(ctx context.Context, tenantID string) ([]controlplane.Stack, error)
	GetStack(ctx context.Context, tenantID, stackID string) (*controlplane.Stack, error)
	CompareAndSwapStackConfig(ctx context.Context, command controlplane.StackConfigCAS) (*controlplane.Stack, error)
}

type walletMigrationStoreAdapter struct {
	store walletMigrationStore
}

func (a walletMigrationStoreAdapter) ListWalletRows(ctx context.Context, tenantID string) ([]walletmigration.SourceRow, error) {
	items, err := a.store.ListWalletItems(ctx, tenantID, "")
	if err != nil {
		return nil, err
	}
	rows := make([]walletmigration.SourceRow, 0, len(items))
	for _, item := range items {
		rows = append(rows, walletRowOf(item))
	}
	return rows, nil
}

func walletRowOf(item controlplane.WalletItem) walletmigration.SourceRow {
	return walletmigration.SourceRow{
		ID: item.ID, TenantID: item.TenantID, InstanceID: item.InstanceID, StackID: item.StackID,
		ItemType: item.ItemType, Provider: item.Provider, ExternalRef: item.ExternalRef,
		Metadata: item.Metadata, CreatedAt: item.CreatedAt, UpdatedAt: item.UpdatedAt,
	}
}

func (a walletMigrationStoreAdapter) ListStackCredentialRefs(ctx context.Context, tenantID string) ([]walletmigration.StackCredentialRef, error) {
	stacks, err := a.store.ListStacksByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	var refs []walletmigration.StackCredentialRef
	for _, stack := range stacks {
		if stack.DeletedAt != nil {
			continue
		}
		if ref, ok := walletmigration.StackCredentialRefFromConfig(stack.TenantID, stack.ID, stack.OwnerSubjectID, stack.Config); ok {
			ref.Revision = stack.UpdatedAt
			refs = append(refs, ref)
		}
	}
	return refs, nil
}

func (a walletMigrationStoreAdapter) GetWalletRow(ctx context.Context, tenantID, itemID string) (walletmigration.SourceRow, error) {
	item, err := a.store.GetWalletItem(ctx, tenantID, itemID)
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		return walletmigration.SourceRow{}, walletmigration.ErrSourceGone
	case err != nil:
		return walletmigration.SourceRow{}, err
	}
	return walletRowOf(*item), nil
}

func (a walletMigrationStoreAdapter) RecordWalletItem(ctx context.Context, tenantID, itemID string, revision time.Time, walletItemID string) error {
	err := a.store.RecordWalletItemMigration(ctx, tenantID, itemID, revision, walletmigration.RecordedItemKey, walletItemID)
	if errors.Is(err, controlplane.ErrConflict) {
		return walletmigration.ErrSourceChanged
	}
	return err
}

// BindStackCredential writes the Wallet item id beside the stack's name label
// (the label stays, so clearing the item id is the rollback).
func (a walletMigrationStoreAdapter) BindStackCredential(ctx context.Context, tenantID, stackID string, revision time.Time, walletItemID string) error {
	stack, err := a.store.GetStack(ctx, tenantID, stackID)
	switch {
	case errors.Is(err, controlplane.ErrNotFound):
		return walletmigration.ErrSourceGone
	case err != nil:
		return err
	case !stack.UpdatedAt.Equal(revision):
		return walletmigration.ErrSourceChanged
	}
	config := maps.Clone(stack.Config)
	if config == nil {
		config = map[string]any{}
	}
	config[walletmigration.StackConfigKeyCredentialItemID] = walletItemID
	_, err = a.store.CompareAndSwapStackConfig(ctx, controlplane.StackConfigCAS{
		TenantID: tenantID, StackID: stackID, ExpectedUpdatedAt: stack.UpdatedAt, Config: config,
	})
	if errors.Is(err, controlplane.ErrConflict) {
		return walletmigration.ErrSourceChanged
	}
	return err
}

var (
	_ walletMigrationStore  = (*controlplane.PostgresStore)(nil)
	_ walletmigration.Store = walletMigrationStoreAdapter{}
)
