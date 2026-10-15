package walletmigration

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// RecordedItemKey is the wallet_items metadata key that records the kombify
// Wallet item a row moved to. The row keeps its ciphertext.
const RecordedItemKey = "kombify_wallet_item_id"

var (
	// ErrSourceChanged is returned by a compare-and-swap write whose row or
	// stack changed since it was read. Nothing was written.
	ErrSourceChanged = errors.New("walletmigration: the source changed since it was read")
	// ErrSourceGone is returned by a read of a row that no longer exists.
	ErrSourceGone = errors.New("walletmigration: the source no longer exists")
)

// Store is the control-plane surface execute needs: the planner's reads, a
// fresh read of one row, and two compare-and-swap writes. Every call runs
// inside the named tenant's RLS boundary.
type Store interface {
	Reader
	// GetWalletRow reads one row as it is now. ErrSourceGone when it is gone.
	GetWalletRow(ctx context.Context, tenantID, itemID string) (SourceRow, error)
	// RecordWalletItem sets RecordedItemKey on the row, only when the row is
	// still at revision (its updated_at). ErrSourceChanged otherwise.
	RecordWalletItem(ctx context.Context, tenantID, itemID string, revision time.Time, walletItemID string) error
	// BindStackCredential sets StackConfigKeyCredentialItemID in the stack's
	// configuration, only when the stack is still at revision. ErrSourceChanged
	// otherwise.
	BindStackCredential(ctx context.Context, tenantID, stackID string, revision time.Time, walletItemID string) error
}

// Wallet is the kombify Wallet surface execute needs; *kombifywallet.Client
// satisfies it.
type Wallet interface {
	CanCreate(ctx context.Context, owner string) error
	Items(ctx context.Context, owner string) ([]walletservice.Item, error)
	Create(ctx context.Context, owner string, spec kombifywallet.ItemSpec) (string, error)
	Reveal(ctx context.Context, owner, itemID string) (walletservice.Secret, error)
}

var _ Wallet = (*kombifywallet.Client)(nil)

// Options selects what one execute run moves.
type Options struct {
	// Tenants are the tenants to migrate; at least one.
	Tenants []string
	// Owners restricts the run to rows of these Auth0 subjects; empty means all.
	Owners []string
	// Limit is the most eligible rows one run attempts; zero means no limit.
	// The rest are deferred to a later run.
	Limit int
}

// Executor moves eligible wallet_items rows into the kombify Wallet. It is
// verified (a created item is delivered back and compared before the source is
// touched), idempotent (an item is found again by its migration token) and
// resumable (a rerun adopts what an interrupted run left). It never prints,
// logs or returns a value, name, label or owner id.
type Executor struct {
	Store  Store
	Wallet Wallet
	// Decrypt opens an enc:v1 value with TECHSTACK_ENCRYPTION_KEY.
	Decrypt func(string) (string, error)
	Now     func() time.Time
}

// consecutiveOutageLimit stops a run whose Wallet stopped answering instead of
// letting every remaining row wait out its timeout.
const consecutiveOutageLimit = 5

// Execute plans the tenants and moves every eligible row of the selection. A
// read error before the first write aborts the run; afterwards each row has
// its own outcome and the run goes on.
func (e Executor) Execute(ctx context.Context, opts Options) (ExecuteReport, error) {
	if e.Store == nil || e.Wallet == nil || e.Decrypt == nil {
		return ExecuteReport{}, errors.New("walletmigration: execute needs a store, a Wallet and a decryptor")
	}
	planned, err := Planner{Reader: e.Store, Now: e.Now}.plan(ctx, opts.Tenants)
	if err != nil {
		return ExecuteReport{}, err
	}
	now := time.Now
	if e.Now != nil {
		now = e.Now
	}
	r := &run{
		e: e, opts: opts, owners: map[string]bool{}, selected: toSet(opts.Owners),
		items: map[string]ownerItems{}, createRight: map[string]error{}, migrated: map[rowKey]migratedItem{},
		report: ExecuteReport{Schema: ExecuteReportSchema, Mode: ModeExecute, Tenants: planned.report.Tenants,
			Results: []RowResult{}, StackResults: []StackResult{}},
	}
	r.report.GeneratedAt = now().UTC().Format(time.RFC3339)
	targets := nodeTargets(planned.plans, planned.refs, planned.report.LabelResolutions)
	refs := map[stackKey]StackCredentialRef{}
	for _, ref := range planned.refs {
		refs[stackKey{ref.TenantID, ref.StackID}] = ref
	}
	for _, tenant := range planned.report.Tenants {
		if r.aborted() {
			break
		}
		r.tenant(ctx, tenant, planned, targets)
		r.switchStacks(ctx, tenant, planned.report.LabelResolutions, refs)
	}
	r.report.Owners = len(r.owners)
	return r.report, nil
}

type rowKey struct{ tenant, item string }
type stackKey struct{ tenant, stack string }

type migratedItem struct {
	walletItemID string
	nodeAccess   bool
}

// ownerItems is one owner's Wallet listing, fetched once per run.
type ownerItems struct {
	items []walletservice.Item
	err   error
}

type run struct {
	e        Executor
	opts     Options
	selected map[string]bool
	report   ExecuteReport

	owners      map[string]bool
	items       map[string]ownerItems
	createRight map[string]error
	migrated    map[rowKey]migratedItem
	attempted   int
	outageRun   int
}

func (r *run) aborted() bool { return r.report.Aborted != "" }

func (r *run) tenant(ctx context.Context, tenant string, planned planResult, targets map[rowKey]nodeTarget) {
	for _, plan := range planned.plans {
		if plan.Source.TenantID != tenant || r.aborted() {
			continue
		}
		if len(r.selected) > 0 && (plan.Owner.Outcome != OwnerResolved || !r.selected[plan.Owner.OwnerID]) {
			continue
		}
		ref := ItemRef{TenantID: tenant, ItemID: plan.Source.ItemID}
		if !plan.Eligible() {
			r.add(RowResult{TenantID: ref.TenantID, ItemID: ref.ItemID, Outcome: OutcomeQuarantined, Reason: string(plan.Quarantine)})
			continue
		}
		if r.opts.Limit > 0 && r.attempted >= r.opts.Limit {
			r.add(RowResult{TenantID: ref.TenantID, ItemID: ref.ItemID, Outcome: OutcomeDeferred})
			continue
		}
		r.attempted++
		r.owners[plan.Owner.OwnerID] = true
		result, moved := r.row(ctx, tenant, plan, targets[rowKey{tenant, plan.Source.ItemID}])
		r.add(result)
		if moved != nil {
			r.migrated[rowKey{tenant, plan.Source.ItemID}] = *moved
		}
		if result.Outcome == OutcomeFailed && result.Reason == kombifywallet.ReasonUnavailable {
			r.outageRun++
			if r.outageRun >= consecutiveOutageLimit {
				r.report.Aborted = kombifywallet.ReasonUnavailable
			}
		} else {
			r.outageRun = 0
		}
	}
}

// createRightFor checks once per owner that Techstack may save to the owner's
// Wallet. Only a definite answer is kept; an outage is asked again.
func (r *run) createRightFor(ctx context.Context, owner string) error {
	if err, known := r.createRight[owner]; known {
		return err
	}
	err := r.e.Wallet.CanCreate(ctx, owner)
	if err == nil || kombifywallet.Denied(err).Reason == kombifywallet.ReasonCreateRightMissing {
		r.createRight[owner] = err
	}
	return err
}

func (r *run) listing(ctx context.Context, owner string) ownerItems {
	if cached, ok := r.items[owner]; ok {
		return cached
	}
	items, err := r.e.Wallet.Items(ctx, owner)
	listing := ownerItems{items: activeItems(items), err: err}
	if err == nil {
		r.items[owner] = listing
	}
	return listing
}

func captureReason(err error) string {
	switch {
	case errors.Is(err, errDecrypt):
		return "decrypt_failed"
	case errors.Is(err, errScope):
		return "scope_invalid"
	default:
		return "empty_value"
	}
}

func activeItems(items []walletservice.Item) []walletservice.Item {
	return slices.DeleteFunc(slices.Clone(items), func(item walletservice.Item) bool {
		return item.State != "" && item.State != "active"
	})
}

func findByID(items []walletservice.Item, id string) (walletservice.Item, bool) {
	index := slices.IndexFunc(items, func(item walletservice.Item) bool { return item.ItemID == id })
	if index < 0 {
		return walletservice.Item{}, false
	}
	return items[index], true
}

// itemsWithToken returns the items whose description carries the migration
// token of key.
func itemsWithToken(items []walletservice.Item, key string) []walletservice.Item {
	var found []walletservice.Item
	for _, item := range items {
		if tokenIn(item.Description) == key {
			found = append(found, item)
		}
	}
	return found
}

// tokenIn extracts the migration key from a description, or "" when it has
// none.
func tokenIn(description string) string {
	_, after, found := strings.Cut(description, MigrationToken)
	if !found {
		return ""
	}
	key, _, _ := strings.Cut(after, " ")
	return strings.TrimSpace(key)
}

// switchStacks writes the Wallet item id into every stack whose name-label
// reference resolved to a row that moved. Ambiguous and unresolved references
// stay as they are and are reported.
func (r *run) switchStacks(ctx context.Context, tenant string, resolutions []LabelResolution, refs map[stackKey]StackCredentialRef) {
	for _, resolution := range resolutions {
		if resolution.TenantID != tenant || r.aborted() {
			continue
		}
		ref := refs[stackKey{tenant, resolution.StackID}]
		if len(r.selected) > 0 && !r.selected[ref.OwnerID] {
			continue
		}
		r.report.addStack(r.stackResult(ctx, resolution, ref))
	}
}

func (r *run) stackResult(ctx context.Context, resolution LabelResolution, ref StackCredentialRef) StackResult {
	result := StackResult{TenantID: resolution.TenantID, StackID: resolution.StackID}
	switch resolution.Outcome {
	case LabelAmbiguous:
		result.Outcome = StackAmbiguous
	case LabelUnresolved:
		result.Outcome = StackUnresolved
	case LabelTargetQuarantined:
		result.Outcome = StackTargetQuarantined
	case LabelResolved:
		r.bindStack(ctx, &result, resolution, ref)
	}
	return result
}

// bindStack writes the item id of the migrated row into the stack, at the
// revision the stack was read at.
func (r *run) bindStack(ctx context.Context, result *StackResult, resolution LabelResolution, ref StackCredentialRef) {
	moved, ok := r.migrated[rowKey{resolution.TenantID, resolution.ItemIDs[0]}]
	switch {
	case !ok:
		result.Outcome = StackItemNotMigrated
		return
	case !moved.nodeAccess:
		result.Outcome = StackItemNotNodeAccess
		return
	}
	result.WalletItemID = moved.walletItemID
	err := r.e.Store.BindStackCredential(ctx, resolution.TenantID, resolution.StackID, ref.Revision, moved.walletItemID)
	switch {
	case err == nil:
		result.Outcome = StackSwitched
	case errors.Is(err, ErrSourceChanged), errors.Is(err, ErrSourceGone):
		result.Outcome = StackSkippedChanged
	default:
		result.Outcome = StackFailed
	}
}

func (r *run) add(result RowResult) { r.report.addRow(result) }

func toSet(values []string) map[string]bool {
	set := map[string]bool{}
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			set[value] = true
		}
	}
	return set
}

// nodeTargets finds the server each SSH row is bound to: the stack the row
// belongs to and the stacks whose name-label reference resolved to it. A row
// whose stacks disagree on the server, or that no stack names, has no target
// and is kept as a plain secret rather than bound to a guess.
func nodeTargets(plans []ItemPlan, refs []StackCredentialRef, resolutions []LabelResolution) map[rowKey]nodeTarget {
	byStack := map[stackKey]nodeTarget{}
	for _, ref := range refs {
		byStack[stackKey{ref.TenantID, ref.StackID}] = nodeTarget{host: ref.Host, user: ref.User, port: ref.Port}
	}
	candidates := map[rowKey][]nodeTarget{}
	add := func(key rowKey, stack stackKey) {
		if target, ok := byStack[stack]; ok && !slices.Contains(candidates[key], target) {
			candidates[key] = append(candidates[key], target)
		}
	}
	for _, plan := range plans {
		if labelResolvedKinds[plan.Source.Kind] && plan.Source.StackID != "" {
			add(rowKey{plan.Source.TenantID, plan.Source.ItemID}, stackKey{plan.Source.TenantID, plan.Source.StackID})
		}
	}
	for _, resolution := range resolutions {
		if resolution.Outcome == LabelResolved {
			add(rowKey{resolution.TenantID, resolution.ItemIDs[0]}, stackKey{resolution.TenantID, resolution.StackID})
		}
	}
	targets := map[rowKey]nodeTarget{}
	for key, list := range candidates {
		if len(list) == 1 {
			targets[key] = list[0]
		}
	}
	return targets
}
