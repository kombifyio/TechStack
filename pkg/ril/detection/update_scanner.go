package detection

import (
	"context"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

// UpdateScanner checks for available OS/package updates and produces
// action cards when updates with security fixes are available.
type UpdateScanner struct{}

func NewUpdateScanner() *UpdateScanner { return &UpdateScanner{} }

func (e *UpdateScanner) Name() string { return "update-scanner" }

func (e *UpdateScanner) Evaluate(_ context.Context, input *EvaluationInput) ([]*actions.ActionCard, error) {
	if input == nil || len(input.OSPackages) == 0 {
		return nil, nil
	}

	var securityPkgs, normalPkgs []PackageInfo
	for _, pkg := range input.OSPackages {
		if pkg.CurrentVersion == pkg.LatestVersion {
			continue
		}
		if pkg.CVESeverity == "high" || pkg.CVESeverity == "critical" {
			securityPkgs = append(securityPkgs, pkg)
		} else if pkg.LatestVersion != "" {
			normalPkgs = append(normalPkgs, pkg)
		}
	}

	var cards []*actions.ActionCard

	if len(securityPkgs) > 0 {
		names := make([]string, 0, len(securityPkgs))
		steps := make([]actions.ActionStep, 0, len(securityPkgs))
		for _, pkg := range securityPkgs {
			names = append(names, fmt.Sprintf("%s %s → %s", pkg.Name, pkg.CurrentVersion, pkg.LatestVersion))
			steps = append(steps, actions.ActionStep{
				Description:   fmt.Sprintf("Update %s to %s (CVE: %s)", pkg.Name, pkg.LatestVersion, strings.Join(pkg.CVEIDs, ", ")),
				Command:       fmt.Sprintf("apt-get install -y %s=%s", pkg.Name, pkg.LatestVersion),
				TargetService: pkg.Name,
			})
		}

		cards = append(cards, &actions.ActionCard{
			ServerID:    input.ServerID,
			OwnerID:     input.OwnerID,
			Type:        actions.TypeSecurity,
			Severity:    actions.SeverityCritical,
			Title:       fmt.Sprintf("Security updates available (%d packages)", len(securityPkgs)),
			Description: strings.Join(names, "\n"),
			Engine:      e.Name(),
			SuggestedPlan: &actions.ActionPlan{
				Steps:       steps,
				Rollback:    "apt-get install <package>=<previous-version>",
				EstDuration: fmt.Sprintf("%dm", len(securityPkgs)*2),
			},
		})
	}

	if len(normalPkgs) > 0 {
		names := make([]string, 0, len(normalPkgs))
		for _, pkg := range normalPkgs {
			names = append(names, fmt.Sprintf("%s %s → %s", pkg.Name, pkg.CurrentVersion, pkg.LatestVersion))
		}

		cards = append(cards, &actions.ActionCard{
			ServerID:    input.ServerID,
			OwnerID:     input.OwnerID,
			Type:        actions.TypeUpdate,
			Severity:    actions.SeverityInfo,
			Title:       fmt.Sprintf("Updates available (%d packages)", len(normalPkgs)),
			Description: strings.Join(names, "\n"),
			Engine:      e.Name(),
		})
	}

	return cards, nil
}
