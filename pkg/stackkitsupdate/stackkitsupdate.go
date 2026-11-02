// Package stackkitsupdate is Techstack's StackKits update management
// (D-2026-11-02-03, GUARD-RELIABILITY-SECURITY-TARGET.md §4.5): it proposes a
// node's pending StackKits release with highlights from the release notes and
// decides when Techstack starts the governed upgrade without a click. The
// Guard never decides; it runs the upgrade, verifies it and rolls it back.
package stackkitsupdate

import (
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/controlplane"
)

// UseCase is a use case activated on a node and every name a release note
// scope may use for it: the slug, its workload, add-ons and selected apps.
type UseCase struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	refs  []string
}

// ActivatedUseCases reads the use cases a node's StackSpec selects from the
// released catalog: a use case whose workload or add-on the spec names.
func ActivatedUseCases(catalog stackkitrelease.UseCaseCatalog, spec map[string]any) []UseCase {
	workloads, _ := spec["workloads"].(map[string]any)
	var out []UseCase
	for _, candidate := range catalog.UseCases {
		activated := UseCase{ID: candidate.ID, Title: candidate.Title}
		add := func(workload string, alternatives []stackkitrelease.UseCaseAlternative) {
			entry, ok := workloads[workload].(map[string]any)
			if workload == "" || !ok {
				return
			}
			activated.refs = append(activated.refs, workload)
			selected, _ := entry["alternative"].(string)
			for _, alternative := range alternatives {
				if alternative.ID == selected {
					activated.refs = append(activated.refs, alternative.ID)
				}
			}
		}
		add(candidate.WorkloadRef, candidate.Alternatives)
		for _, addOn := range candidate.AddOns {
			add(addOn.ID, addOn.Alternatives)
		}
		if len(activated.refs) > 0 {
			activated.refs = append(activated.refs, candidate.ID)
			out = append(out, activated)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// SecurityScopeAll is the scope of a security note that patches every node
// of the kit rather than one app. A security note without a scope names no
// app and starts nothing on its own.
const SecurityScopeAll = "all"

// Change is one release note line a proposal shows.
type Change struct {
	Version string `json:"version"`
	Kind    string `json:"kind"`
	Scope   string `json:"scope,omitempty"`
	Text    string `json:"text"`
}

// Highlight groups the changes of one activated use case.
type Highlight struct {
	UseCaseID    string   `json:"use_case_id"`
	UseCaseTitle string   `json:"use_case_title"`
	Changes      []Change `json:"changes"`
}

// Security is the security marker of the pending releases for this node.
type Security struct {
	// AffectedApps are the apps the security notes name.
	AffectedApps []string `json:"affected_apps"`
	// AffectsNode is true when a security note names an app activated on this
	// node or all nodes: Techstack then starts the update without approval.
	AffectsNode bool     `json:"affects_node"`
	Changes     []Change `json:"changes"`
}

// Proposal is what a node gains by moving to its pending release.
type Proposal struct {
	Installed    string      `json:"installed"`
	Available    string      `json:"available"`
	ChangelogURL string      `json:"changelog_url"`
	Highlights   []Highlight `json:"highlights"`
	// OtherChanges is the rest in short; OtherChangesTotal counts all of them.
	OtherChanges      []Change  `json:"other_changes"`
	OtherChangesTotal int       `json:"other_changes_total"`
	Security          *Security `json:"security,omitempty"`
}

// otherChangesShown bounds "the rest in short"; the changelog link has all.
const otherChangesShown = 6

// Propose summarizes the releases between installed and available, the
// changes to the node's activated use cases first.
func Propose(notes stackkitrelease.ReleaseNotes, activated []UseCase, installed, available string) Proposal {
	proposal := Proposal{Installed: installed, Available: available, ChangelogURL: notes.ChangelogURL, Highlights: []Highlight{}, OtherChanges: []Change{}}
	owner := map[string]int{}
	for index, useCase := range activated {
		proposal.Highlights = append(proposal.Highlights, Highlight{UseCaseID: useCase.ID, UseCaseTitle: useCase.Title, Changes: []Change{}})
		for _, ref := range useCase.refs {
			if _, taken := owner[ref]; !taken {
				owner[ref] = index
			}
		}
	}
	var security Security
	affected := map[string]bool{}
	for _, release := range notes.Between(installed, available) {
		for _, entry := range release.Entries {
			change := Change{Version: release.Version, Kind: entry.Kind, Scope: entry.Scope, Text: entry.Text}
			index, mine := owner[entry.Scope]
			if entry.Kind == stackkitrelease.ReleaseNoteSecurity {
				security.Changes = append(security.Changes, change)
				if entry.Scope != "" && entry.Scope != SecurityScopeAll && !affected[entry.Scope] {
					affected[entry.Scope] = true
					security.AffectedApps = append(security.AffectedApps, entry.Scope)
				}
				security.AffectsNode = security.AffectsNode || mine || entry.Scope == SecurityScopeAll
			}
			switch {
			case mine:
				proposal.Highlights[index].Changes = append(proposal.Highlights[index].Changes, change)
			case entry.Kind != stackkitrelease.ReleaseNoteSecurity:
				proposal.OtherChangesTotal++
				if len(proposal.OtherChanges) < otherChangesShown {
					proposal.OtherChanges = append(proposal.OtherChanges, change)
				}
			}
		}
	}
	kept := proposal.Highlights[:0]
	for _, highlight := range proposal.Highlights {
		if len(highlight.Changes) > 0 {
			kept = append(kept, highlight)
		}
	}
	proposal.Highlights = kept
	if len(security.Changes) > 0 {
		if security.AffectedApps == nil {
			security.AffectedApps = []string{}
		}
		proposal.Security = &security
	}
	return proposal
}

// Start says why Techstack starts a node's pending update without a click.
type Start string

const (
	// StartNone leaves the update to the owner's Approve.
	StartNone Start = ""
	// StartAutomatic follows the node's "install updates automatically".
	StartAutomatic Start = controlplane.StackKitsUpdateTriggerAutomatic
	// StartSecurity applies a security patch to an affected node, whatever
	// the setting says.
	StartSecurity Start = controlplane.StackKitsUpdateTriggerSecurity
)

// Decide whether Techstack starts the pending update itself. A release is
// started at most once this way and never after the owner already ran it:
// a failed update waits for the owner's Approve.
func Decide(behavior string, proposal Proposal, attempt *controlplane.WorkerStackKitsUpdateAttempt, target *controlplane.WorkerStackKitsTarget) Start {
	available := releaseTag(proposal.Available)
	switch {
	case proposal.Available == "":
		return StartNone
	case attempt != nil && releaseTag(attempt.Version) == available:
		return StartNone
	case target != nil && releaseTag(target.Version) == available:
		return StartNone
	case proposal.Security != nil && proposal.Security.AffectsNode:
		return StartSecurity
	case behavior == controlplane.StackKitsUpdateAutomatic:
		return StartAutomatic
	default:
		return StartNone
	}
}

func releaseTag(version string) string {
	return "v" + strings.TrimPrefix(strings.TrimSpace(version), "v")
}
