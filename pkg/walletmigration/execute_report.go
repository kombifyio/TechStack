package walletmigration

import (
	"fmt"
	"io"
	"sort"
	"strings"
)

// ExecuteReportSchema identifies the execute report format.
const ExecuteReportSchema = "techstack.wallet-migration-execute.v1"

// ModeExecute is the mode of an execute report.
const ModeExecute = "execute"

// Outcome is what happened to one row.
type Outcome string

const (
	// OutcomeMigrated: a Wallet item was created, read back identically and
	// recorded on the row.
	OutcomeMigrated Outcome = "migrated"
	// OutcomeAdopted: an item an interrupted run left was read back
	// identically and recorded on the row.
	OutcomeAdopted Outcome = "adopted"
	// OutcomeAlreadyMigrated: an earlier run recorded the item, which the
	// Wallet still lists. Its value is not compared again: the owner may have
	// changed it in the Wallet since.
	OutcomeAlreadyMigrated Outcome = "already_migrated"
	// OutcomeSkippedChanged: the row changed since it was read; nothing on it
	// was written.
	OutcomeSkippedChanged Outcome = "skipped_changed"
	// OutcomeAmbiguous: two Wallet items carry the row's migration token.
	OutcomeAmbiguous Outcome = "ambiguous"
	// OutcomeQuarantined: the planner does not admit the row; it is untouched.
	OutcomeQuarantined Outcome = "quarantined"
	// OutcomeCreateRightMissing: the owner has not allowed Techstack to save
	// credentials in the Wallet; the row is untouched.
	OutcomeCreateRightMissing Outcome = "create_right_missing"
	// OutcomeFailed: see the reason code.
	OutcomeFailed Outcome = "failed"
	// OutcomeDeferred: beyond --limit, left for a later run.
	OutcomeDeferred Outcome = "deferred"
)

// StackOutcome is what happened to one stack's credential reference.
type StackOutcome string

const (
	StackSwitched          StackOutcome = "switched"
	StackAmbiguous         StackOutcome = "ambiguous"
	StackUnresolved        StackOutcome = "unresolved"
	StackTargetQuarantined StackOutcome = "target_quarantined"
	// StackItemNotMigrated: the row the reference names did not move in this run.
	StackItemNotMigrated StackOutcome = "item_not_migrated"
	// StackItemNotNodeAccess: the row moved as a plain secret, which a stack
	// cannot log in with, so the stack keeps its label.
	StackItemNotNodeAccess StackOutcome = "item_not_node_access"
	StackSkippedChanged    StackOutcome = "skipped_changed"
	StackFailed            StackOutcome = "failed"
)

// RowResult names one row and what became of it: ids and a reason code only.
type RowResult struct {
	TenantID     string  `json:"tenant_id"`
	ItemID       string  `json:"item_id"`
	Outcome      Outcome `json:"outcome"`
	Reason       string  `json:"reason_code,omitempty"`
	WalletItemID string  `json:"wallet_item_id,omitempty"`
}

// StackResult names one stack reference and what became of it.
type StackResult struct {
	TenantID     string       `json:"tenant_id"`
	StackID      string       `json:"stack_id"`
	Outcome      StackOutcome `json:"outcome"`
	WalletItemID string       `json:"wallet_item_id,omitempty"`
}

// RowCounts counts rows per outcome.
type RowCounts struct {
	Migrated           int `json:"migrated"`
	Adopted            int `json:"adopted"`
	AlreadyMigrated    int `json:"already_migrated"`
	SkippedChanged     int `json:"skipped_changed"`
	Ambiguous          int `json:"ambiguous"`
	Quarantined        int `json:"quarantined"`
	CreateRightMissing int `json:"create_right_missing"`
	Failed             int `json:"failed"`
	Deferred           int `json:"deferred"`
}

// StackCounts counts stack references per outcome.
type StackCounts struct {
	Switched          int `json:"switched"`
	Ambiguous         int `json:"ambiguous"`
	Unresolved        int `json:"unresolved"`
	TargetQuarantined int `json:"target_quarantined"`
	ItemNotMigrated   int `json:"item_not_migrated"`
	ItemNotNodeAccess int `json:"item_not_node_access"`
	SkippedChanged    int `json:"skipped_changed"`
	Failed            int `json:"failed"`
}

// ExecuteReport is the execute result. It carries ids, counts, outcomes and
// reason codes only: no value, display name, label or owner id (owners are
// counted).
type ExecuteReport struct {
	Schema       string        `json:"schema"`
	Mode         string        `json:"mode"`
	GeneratedAt  string        `json:"generated_at"`
	Tenants      []string      `json:"tenants"`
	Owners       int           `json:"owners"`
	Rows         RowCounts     `json:"rows"`
	Stacks       StackCounts   `json:"stacks"`
	Results      []RowResult   `json:"results"`
	StackResults []StackResult `json:"stack_results"`
	// Aborted is the reason code that stopped the run early, if any.
	Aborted string `json:"aborted,omitempty"`
}

func (r *ExecuteReport) addRow(result RowResult) {
	counts := &r.Rows
	switch result.Outcome {
	case OutcomeMigrated:
		counts.Migrated++
	case OutcomeAdopted:
		counts.Adopted++
	case OutcomeAlreadyMigrated:
		counts.AlreadyMigrated++
	case OutcomeSkippedChanged:
		counts.SkippedChanged++
	case OutcomeAmbiguous:
		counts.Ambiguous++
	case OutcomeQuarantined:
		counts.Quarantined++
	case OutcomeCreateRightMissing:
		counts.CreateRightMissing++
	case OutcomeFailed:
		counts.Failed++
	case OutcomeDeferred:
		counts.Deferred++
	}
	r.Results = append(r.Results, result)
}

func (r *ExecuteReport) addStack(result StackResult) {
	counts := &r.Stacks
	switch result.Outcome {
	case StackSwitched:
		counts.Switched++
	case StackAmbiguous:
		counts.Ambiguous++
	case StackUnresolved:
		counts.Unresolved++
	case StackTargetQuarantined:
		counts.TargetQuarantined++
	case StackItemNotMigrated:
		counts.ItemNotMigrated++
	case StackItemNotNodeAccess:
		counts.ItemNotNodeAccess++
	case StackSkippedChanged:
		counts.SkippedChanged++
	case StackFailed:
		counts.Failed++
	}
	r.StackResults = append(r.StackResults, result)
}

// NotMoved counts the eligible rows that did not move plus the stacks whose
// switch did not complete: the work a rerun still has. Quarantined rows are not
// eligible and deferred rows were not attempted, so neither counts.
func (r ExecuteReport) NotMoved() int {
	rows := r.Rows
	stacks := r.Stacks
	return rows.SkippedChanged + rows.Ambiguous + rows.CreateRightMissing + rows.Failed + stacks.SkippedChanged + stacks.Failed
}

// Complete reports whether the run moved everything it attempted.
func (r ExecuteReport) Complete() bool { return r.NotMoved() == 0 && r.Aborted == "" }

// WriteText renders the report for an operator terminal.
func (r ExecuteReport) WriteText(w io.Writer) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Techstack wallet migration (%s, %s)\n", r.Mode, r.GeneratedAt)
	fmt.Fprintf(&b, "Tenants: %s  owners: %d\n\n", strings.Join(r.Tenants, ", "), r.Owners)
	c := r.Rows
	fmt.Fprintf(&b, "Rows: migrated %d  adopted %d  already migrated %d  skipped (changed) %d  ambiguous %d\n", c.Migrated, c.Adopted, c.AlreadyMigrated, c.SkippedChanged, c.Ambiguous)
	fmt.Fprintf(&b, "      create right missing %d  failed %d  quarantined %d  deferred %d\n", c.CreateRightMissing, c.Failed, c.Quarantined, c.Deferred)
	s := r.Stacks
	fmt.Fprintf(&b, "Stacks: switched %d  ambiguous %d  unresolved %d  target quarantined %d  item not migrated %d\n", s.Switched, s.Ambiguous, s.Unresolved, s.TargetQuarantined, s.ItemNotMigrated)
	fmt.Fprintf(&b, "        item not node access %d  skipped (changed) %d  failed %d\n", s.ItemNotNodeAccess, s.SkippedChanged, s.Failed)
	if r.Aborted != "" {
		fmt.Fprintf(&b, "\nAborted: %s\n", r.Aborted)
	}
	var lines []string
	for _, result := range r.Results {
		if result.Outcome == OutcomeMigrated || result.Outcome == OutcomeAdopted || result.Outcome == OutcomeAlreadyMigrated || result.Outcome == OutcomeDeferred {
			continue
		}
		line := fmt.Sprintf("%s/%s %s", result.TenantID, result.ItemID, result.Outcome)
		if result.Reason != "" {
			line += " " + result.Reason
		}
		lines = append(lines, line)
	}
	sort.Strings(lines)
	writeSection(&b, "Rows needing attention (ids and reason codes)", lines)
	lines = nil
	for _, result := range r.StackResults {
		if result.Outcome != StackSwitched {
			lines = append(lines, fmt.Sprintf("%s/%s %s", result.TenantID, result.StackID, result.Outcome))
		}
	}
	sort.Strings(lines)
	writeSection(&b, "Stack references left as they are", lines)
	_, err := w.Write([]byte(b.String()))
	return err
}
