package walletmigration

import (
	"context"
	"crypto/subtle"
	"errors"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	"github.com/kombifyio/techstack/pkg/kombifywallet"
)

// rowFlow carries one eligible row through the steps of its move. Each step
// returns true when the row is finished, with the outcome set.
type rowFlow struct {
	run    *run
	tenant string
	target nodeTarget

	result RowResult
	moved  *migratedItem

	fresh SourceRow
	plan  ItemPlan
	owner string
	items []walletservice.Item
	built capture
	// adopt is the item an interrupted run left; nil when the row needs a new one.
	adopt        *walletservice.Item
	outcome      Outcome
	walletItemID string
}

// row moves one eligible row. The row is read again first, so the window in
// which it can change before the compare-and-swap is one Wallet round trip.
func (r *run) row(ctx context.Context, tenant string, planned ItemPlan, target nodeTarget) (RowResult, *migratedItem) {
	f := &rowFlow{run: r, tenant: tenant, target: target, result: RowResult{TenantID: tenant, ItemID: planned.Source.ItemID}}
	defer func() { clear(f.built.spec.Value) }()
	if f.load(ctx, planned) || f.existing() || f.prepare(ctx) || f.deliver(ctx) {
		return f.result, f.moved
	}
	f.record(ctx)
	return f.result, f.moved
}

func (f *rowFlow) finish(outcome Outcome, reason string) bool {
	f.result.Outcome, f.result.Reason = outcome, reason
	return true
}

// walletFailure maps a Wallet error to a row outcome. A missing create right
// is its own outcome: the owner grants it, nobody else can.
func (f *rowFlow) walletFailure(err error) bool {
	denial := kombifywallet.Denied(err)
	if denial.Reason == kombifywallet.ReasonCreateRightMissing {
		return f.finish(OutcomeCreateRightMissing, denial.Reason)
	}
	return f.finish(OutcomeFailed, denial.Reason)
}

// load reads the row again, plans it again and lists the owner's Wallet items.
func (f *rowFlow) load(ctx context.Context, planned ItemPlan) bool {
	fresh, err := f.run.e.Store.GetWalletRow(ctx, f.tenant, planned.Source.ItemID)
	switch {
	case errors.Is(err, ErrSourceGone):
		return f.finish(OutcomeSkippedChanged, "row_gone")
	case err != nil:
		return f.finish(OutcomeFailed, "row_unreadable")
	}
	f.fresh, f.plan = fresh, PlanItem(fresh, f.tenant)
	if !f.plan.Eligible() || f.plan.Owner.OwnerID != planned.Owner.OwnerID {
		return f.finish(OutcomeSkippedChanged, "row_changed")
	}
	f.owner = f.plan.Owner.OwnerID
	listing := f.run.listing(ctx, f.owner)
	if listing.err != nil {
		return f.walletFailure(listing.err)
	}
	f.items = listing.items
	return false
}

// existing settles a row an earlier run already moved or started to move.
func (f *rowFlow) existing() bool {
	if recorded := f.plan.Source.RecordedWalletItemID; recorded != "" {
		item, ok := findByID(f.items, recorded)
		if !ok {
			return f.finish(OutcomeFailed, "recorded_item_missing")
		}
		f.result.WalletItemID = recorded
		f.moved = &migratedItem{walletItemID: recorded, nodeAccess: item.ItemType == walletservice.ItemTypeNodeAccessProfile}
		return f.finish(OutcomeAlreadyMigrated, "")
	}
	switch matches := itemsWithToken(f.items, f.plan.Target.MigrationKey); len(matches) {
	case 0:
	case 1:
		f.adopt = &matches[0]
	default:
		return f.finish(OutcomeAmbiguous, "duplicate_migration_key")
	}
	return false
}

// prepare checks the create right (only a new item needs it) and decrypts the
// row into the Wallet value.
func (f *rowFlow) prepare(ctx context.Context) bool {
	if f.adopt == nil {
		if err := f.run.createRightFor(ctx, f.owner); err != nil {
			return f.walletFailure(err)
		}
	}
	built, err := buildCapture(f.plan, f.target, f.run.e.Decrypt)
	if err != nil {
		return f.finish(OutcomeFailed, captureReason(err))
	}
	f.built = built
	return false
}

// deliver adopts the item an interrupted run left, or creates the item, and in
// both cases reads it back before the row is touched.
func (f *rowFlow) deliver(ctx context.Context) bool {
	if f.adopt != nil {
		f.outcome, f.walletItemID, f.result.WalletItemID = OutcomeAdopted, f.adopt.ItemID, f.adopt.ItemID
		if f.adopt.ItemType != f.built.spec.Type {
			return f.finish(OutcomeFailed, "wallet_item_differs")
		}
		return f.verify(ctx, "wallet_item_differs")
	}
	created, err := f.run.e.Wallet.Create(ctx, f.owner, f.built.spec)
	if err != nil {
		return f.walletFailure(err)
	}
	f.outcome, f.walletItemID, f.result.WalletItemID = OutcomeMigrated, created, created
	return f.verify(ctx, "readback_mismatch")
}

// verify delivers the item and compares it with the value in constant time.
func (f *rowFlow) verify(ctx context.Context, mismatch string) bool {
	delivered, err := f.run.e.Wallet.Reveal(ctx, f.owner, f.walletItemID)
	if err != nil {
		return f.walletFailure(err)
	}
	defer delivered.Wipe()
	if subtle.ConstantTimeCompare(delivered.Bytes(), f.built.spec.Value) != 1 {
		return f.finish(OutcomeFailed, mismatch)
	}
	return false
}

// record writes the Wallet item id on the row in one compare-and-swap on the
// revision the row was read at.
func (f *rowFlow) record(ctx context.Context) {
	f.result.WalletItemID = f.walletItemID
	err := f.run.e.Store.RecordWalletItem(ctx, f.tenant, f.plan.Source.ItemID, f.fresh.UpdatedAt, f.walletItemID)
	switch {
	case errors.Is(err, ErrSourceChanged), errors.Is(err, ErrSourceGone):
		f.finish(OutcomeSkippedChanged, "row_changed")
	case err != nil:
		f.finish(OutcomeFailed, "record_failed")
	default:
		f.result.Outcome = f.outcome
		f.moved = &migratedItem{walletItemID: f.walletItemID, nodeAccess: f.built.nodeAccess}
	}
}
