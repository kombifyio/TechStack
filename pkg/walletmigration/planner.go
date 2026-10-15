package walletmigration

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// ReportSchema identifies the dry-run report format.
const ReportSchema = "techstack.wallet-migration-plan.v1"

// ModeDryRun is the only mode that runs before the Wallet service API exists.
const ModeDryRun = "dry_run"

// Reader is the narrow read surface the planner needs. Implementations read
// inside each tenant's RLS boundary and return rows unmodified; the planner
// never asks for a key.
type Reader interface {
	ListWalletRows(ctx context.Context, tenantID string) ([]SourceRow, error)
	ListStackCredentialRefs(ctx context.Context, tenantID string) ([]StackCredentialRef, error)
}

// Planner builds the dry-run plan.
type Planner struct {
	Reader Reader
	Now    func() time.Time
}

// ItemRef names one row in a report: ids only.
type ItemRef struct {
	TenantID string `json:"tenant_id"`
	ItemID   string `json:"item_id"`
}

// Totals are row counts across the planned tenants.
type Totals struct {
	Rows            int `json:"rows"`
	Eligible        int `json:"eligible"`
	Quarantined     int `json:"quarantined"`
	LegacyPlaintext int `json:"legacy_plaintext"`
	Derived         int `json:"derived"`
}

// KindCount counts eligible rows per Techstack kind and Wallet type.
type KindCount struct {
	Kind   string       `json:"kind"`
	Target ResourceType `json:"target"`
	Count  int          `json:"count"`
}

// QuarantineGroup lists quarantined rows by their primary reason.
type QuarantineGroup struct {
	Reason QuarantineReason `json:"reason"`
	Items  []ItemRef        `json:"items"`
}

// WriterCount counts eligible rows per writer that must switch with them.
type WriterCount struct {
	Writer Writer `json:"writer"`
	Count  int    `json:"count"`
}

// Operations estimates the work of the execute phase.
type Operations struct {
	// WalletCaptures: one item with its first version per eligible row.
	WalletCaptures int `json:"wallet_captures"`
	// WalletReadBacks: one readback per capture before the source changes.
	WalletReadBacks int `json:"wallet_readbacks"`
	// LegacyDecryptions: enc:v1 values that need TECHSTACK_ENCRYPTION_KEY.
	LegacyDecryptions int `json:"legacy_decryptions"`
	// PlaintextValues: legacy plaintext secret or TOTP values captured as is.
	PlaintextValues int `json:"plaintext_values"`
	// SourceTombstones: wallet_items rows replaced by a tombstone after readback.
	SourceTombstones int `json:"source_tombstones"`
	// CallerSwitches: stack references rewritten from a name label to an item id.
	CallerSwitches int `json:"caller_switches"`
	// WalletTotal: Wallet API calls (captures plus readbacks).
	WalletTotal int `json:"wallet_total"`
}

// Report is the dry-run result. It carries ids and counts only: no value,
// display name, label, owner id or metadata.
type Report struct {
	Schema           string            `json:"schema"`
	Mode             string            `json:"mode"`
	GeneratedAt      string            `json:"generated_at"`
	Tenants          []string          `json:"tenants"`
	Totals           Totals            `json:"totals"`
	Kinds            []KindCount       `json:"kinds"`
	Quarantine       []QuarantineGroup `json:"quarantine"`
	LegacyPlaintext  []ItemRef         `json:"legacy_plaintext"`
	DerivedWriters   []WriterCount     `json:"derived_writers"`
	Callers          []Caller          `json:"callers"`
	LabelResolutions []LabelResolution `json:"label_resolutions"`
	LabelCollisions  []LabelCollision  `json:"label_collisions"`
	Operations       Operations        `json:"estimated_operations"`
}

// Plan reads every tenant and returns the report and the per-row plans. Any
// read error aborts the whole plan: a partial report would understate the
// rows that still depend on the legacy key.
func (p Planner) Plan(ctx context.Context, tenants []string) (Report, []ItemPlan, error) {
	result, err := p.plan(ctx, tenants)
	return result.report, result.plans, err
}

// planResult is a plan with the stack references it was resolved against,
// which the executor needs to find each node access item's target.
type planResult struct {
	report Report
	plans  []ItemPlan
	// refs are the stack references still resolved by name label: a stack that
	// already reads a Wallet item needs no switch and is left out.
	refs []StackCredentialRef
}

func (p Planner) plan(ctx context.Context, tenants []string) (planResult, error) {
	if p.Reader == nil {
		return planResult{}, errors.New("walletmigration: no reader configured")
	}
	scopes, err := normalizeTenants(tenants)
	if err != nil {
		return planResult{}, err
	}
	var plans []ItemPlan
	var refs []StackCredentialRef
	for _, tenant := range scopes {
		rows, err := p.Reader.ListWalletRows(ctx, tenant)
		if err != nil {
			return planResult{}, fmt.Errorf("walletmigration: list wallet rows for tenant %q: %w", tenant, err)
		}
		for _, row := range rows {
			plans = append(plans, PlanItem(row, tenant))
		}
		tenantRefs, err := p.Reader.ListStackCredentialRefs(ctx, tenant)
		if err != nil {
			return planResult{}, fmt.Errorf("walletmigration: list stack credential references for tenant %q: %w", tenant, err)
		}
		for _, ref := range tenantRefs {
			if strings.TrimSpace(ref.TenantID) == tenant && ref.ItemID == "" {
				refs = append(refs, ref)
			}
		}
	}
	sort.SliceStable(plans, func(i, j int) bool {
		if plans[i].Source.TenantID != plans[j].Source.TenantID {
			return plans[i].Source.TenantID < plans[j].Source.TenantID
		}
		return plans[i].Source.ItemID < plans[j].Source.ItemID
	})
	now := time.Now
	if p.Now != nil {
		now = p.Now
	}
	report := summarize(plans, ResolveStackLabels(refs, plans))
	report.GeneratedAt = now().UTC().Format(time.RFC3339)
	report.Tenants = scopes
	report.LabelCollisions = LabelCollisions(plans)
	return planResult{report: report, plans: plans, refs: refs}, nil
}

func normalizeTenants(tenants []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, tenant := range tenants {
		tenant = strings.TrimSpace(tenant)
		if tenant == "" {
			return nil, errors.New("walletmigration: tenant ids must not be empty")
		}
		if !seen[tenant] {
			seen[tenant] = true
			out = append(out, tenant)
		}
	}
	if len(out) == 0 {
		return nil, errors.New("walletmigration: at least one tenant id is required")
	}
	return out, nil
}

func summarize(plans []ItemPlan, labels []LabelResolution) Report {
	report := Report{
		Schema:           ReportSchema,
		Mode:             ModeDryRun,
		Quarantine:       []QuarantineGroup{},
		LegacyPlaintext:  []ItemRef{},
		Callers:          KnownCallers(),
		LabelResolutions: labels,
	}
	kinds := map[KindCount]int{}
	quarantine := map[QuarantineReason][]ItemRef{}
	writers := map[Writer]int{}
	for _, plan := range plans {
		ref := ItemRef{TenantID: plan.Source.TenantID, ItemID: plan.Source.ItemID}
		report.Totals.Rows++
		if !plan.Eligible() {
			report.Totals.Quarantined++
			quarantine[plan.Quarantine] = append(quarantine[plan.Quarantine], ref)
			continue
		}
		kinds[KindCount{Kind: plan.Source.Kind, Target: plan.Target.ResourceType}]++
		report.countEligible(plan, ref, writers)
	}
	for _, label := range labels {
		if label.Outcome == LabelResolved || label.Outcome == LabelAmbiguous {
			report.Operations.CallerSwitches++
		}
	}
	report.Operations.WalletTotal = report.Operations.WalletCaptures + report.Operations.WalletReadBacks
	for _, reason := range quarantinePrecedence {
		if items := quarantine[reason]; len(items) > 0 {
			report.Quarantine = append(report.Quarantine, QuarantineGroup{Reason: reason, Items: items})
		}
	}
	report.Kinds = sortedKinds(kinds)
	report.DerivedWriters = sortedWriters(writers)
	return report
}

// countEligible adds one eligible row to the totals and the operations.
func (r *Report) countEligible(plan ItemPlan, ref ItemRef, writers map[Writer]int) {
	r.Totals.Eligible++
	if plan.HasFlag(FlagLegacyPlaintext) {
		r.Totals.LegacyPlaintext++
		r.LegacyPlaintext = append(r.LegacyPlaintext, ref)
	}
	if plan.HasFlag(FlagDerivedProjection) {
		r.Totals.Derived++
		writers[plan.Source.Writer]++
	}
	r.Operations.WalletCaptures++
	r.Operations.WalletReadBacks++
	r.Operations.SourceTombstones++
	for _, value := range []SourceValue{plan.Target.Values.Secret, plan.Target.Values.TOTP} {
		switch value.Encoding() {
		case ValueEncryptedV1:
			r.Operations.LegacyDecryptions++
		case ValuePlaintext:
			r.Operations.PlaintextValues++
		}
	}
}

func sortedKinds(kinds map[KindCount]int) []KindCount {
	out := make([]KindCount, 0, len(kinds))
	for key, count := range kinds {
		key.Count = count
		out = append(out, key)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].Target < out[j].Target
	})
	return out
}

func sortedWriters(writers map[Writer]int) []WriterCount {
	out := make([]WriterCount, 0, len(writers))
	for writer, count := range writers {
		out = append(out, WriterCount{Writer: writer, Count: count})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Writer < out[j].Writer })
	return out
}

// WriteText renders the report for an operator terminal.
func (r Report) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Techstack wallet migration plan (%s, %s)\n", r.Mode, r.GeneratedAt)
	fmt.Fprintf(&b, "Tenants: %s\n\n", strings.Join(r.Tenants, ", "))
	fmt.Fprintf(&b, "Rows %d  eligible %d  quarantined %d  legacy plaintext %d  derived %d\n",
		r.Totals.Rows, r.Totals.Eligible, r.Totals.Quarantined, r.Totals.LegacyPlaintext, r.Totals.Derived)

	var lines []string
	for _, kind := range r.Kinds {
		lines = append(lines, fmt.Sprintf("%-14s -> %-20s %d", kind.Kind, kind.Target, kind.Count))
	}
	writeSection(&b, "Eligible rows by kind and Wallet type", lines)

	lines = nil
	for _, group := range r.Quarantine {
		lines = append(lines, fmt.Sprintf("%s (%d)", group.Reason, len(group.Items)))
		for _, item := range group.Items {
			lines = append(lines, "  "+item.TenantID+"/"+item.ItemID)
		}
	}
	writeSection(&b, "Quarantine (item ids)", lines)
	writeSection(&b, "Legacy plaintext values (eligible, item ids)", itemRefLines(r.LegacyPlaintext))

	lines = nil
	for _, writer := range r.DerivedWriters {
		lines = append(lines, fmt.Sprintf("%-18s %d", writer.Writer, writer.Count))
	}
	writeSection(&b, "Writers that must switch with their rows", lines)

	lines = nil
	for _, caller := range r.Callers {
		if caller.Resolution == ResolutionNameLabel {
			lines = append(lines, caller.Location+" ("+strings.Join(caller.Kinds, ", ")+")")
		}
	}
	writeSection(&b, "Callers that still resolve by name label", lines)

	lines = nil
	for _, label := range r.LabelResolutions {
		line := fmt.Sprintf("%s/%s %s %s", label.TenantID, label.StackID, label.Kind, label.Outcome)
		if len(label.ItemIDs) > 0 {
			line += " -> " + strings.Join(label.ItemIDs, ", ")
		}
		lines = append(lines, line)
	}
	writeSection(&b, "Stack references resolved by name label", lines)

	lines = nil
	for _, collision := range r.LabelCollisions {
		lines = append(lines, fmt.Sprintf("%s %s: %s", collision.TenantID, collision.Kind, strings.Join(collision.ItemIDs, ", ")))
	}
	writeSection(&b, "Label collisions (newest first)", lines)

	ops := r.Operations
	writeSection(&b, "Estimated operations", []string{
		fmt.Sprintf("Wallet captures      %d", ops.WalletCaptures),
		fmt.Sprintf("Wallet readbacks     %d", ops.WalletReadBacks),
		fmt.Sprintf("Wallet total         %d", ops.WalletTotal),
		fmt.Sprintf("legacy decryptions   %d", ops.LegacyDecryptions),
		fmt.Sprintf("plaintext values     %d", ops.PlaintextValues),
		fmt.Sprintf("source tombstones    %d", ops.SourceTombstones),
		fmt.Sprintf("caller switches      %d", ops.CallerSwitches),
	})
	_, err := io.WriteString(w, b.String())
	return err
}

func writeSection(b *strings.Builder, title string, lines []string) {
	b.WriteString("\n" + title + "\n")
	if len(lines) == 0 {
		b.WriteString("  none\n")
	}
	for _, line := range lines {
		b.WriteString("  " + line + "\n")
	}
}

func itemRefLines(refs []ItemRef) []string {
	lines := make([]string, 0, len(refs))
	for _, ref := range refs {
		lines = append(lines, ref.TenantID+"/"+ref.ItemID)
	}
	return lines
}
