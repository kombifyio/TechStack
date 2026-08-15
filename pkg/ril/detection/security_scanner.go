package detection

import (
	"context"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/pkg/ril/actions"
)

// SecurityScanner checks for CVEs (high/critical), open ports, and
// expired certificates, producing action cards for each finding.
type SecurityScanner struct{}

func NewSecurityScanner() *SecurityScanner { return &SecurityScanner{} }

func (e *SecurityScanner) Name() string { return "security-scanner" }

func (e *SecurityScanner) Evaluate(_ context.Context, input *EvaluationInput) ([]*actions.ActionCard, error) {
	if input == nil || len(input.SecurityFindings) == 0 {
		return nil, nil
	}

	grouped := map[string][]SecurityFinding{
		"cve":          {},
		"open_port":    {},
		"expired_cert": {},
	}

	for _, f := range input.SecurityFindings {
		if f.Severity != "high" && f.Severity != "critical" {
			continue
		}
		grouped[f.Type] = append(grouped[f.Type], f)
	}

	var cards []*actions.ActionCard

	if cves := grouped["cve"]; len(cves) > 0 {
		descs := make([]string, 0, len(cves))
		for _, c := range cves {
			descs = append(descs, c.Description)
		}
		cards = append(cards, &actions.ActionCard{
			ServerID:    input.ServerID,
			OwnerID:     input.OwnerID,
			Type:        actions.TypeSecurity,
			Severity:    actions.SeverityCritical,
			Title:       fmt.Sprintf("%d high/critical CVEs detected", len(cves)),
			Description: strings.Join(descs, "\n"),
			Engine:      e.Name(),
		})
	}

	if ports := grouped["open_port"]; len(ports) > 0 {
		descs := make([]string, 0, len(ports))
		for _, p := range ports {
			descs = append(descs, p.Description)
		}
		cards = append(cards, &actions.ActionCard{
			ServerID:    input.ServerID,
			OwnerID:     input.OwnerID,
			Type:        actions.TypeSecurity,
			Severity:    actions.SeverityWarning,
			Title:       fmt.Sprintf("%d unexpected open ports", len(ports)),
			Description: strings.Join(descs, "\n"),
			Engine:      e.Name(),
		})
	}

	if certs := grouped["expired_cert"]; len(certs) > 0 {
		descs := make([]string, 0, len(certs))
		for _, c := range certs {
			descs = append(descs, c.Description)
		}
		cards = append(cards, &actions.ActionCard{
			ServerID:    input.ServerID,
			OwnerID:     input.OwnerID,
			Type:        actions.TypeSecurity,
			Severity:    actions.SeverityCritical,
			Title:       fmt.Sprintf("%d expired certificates", len(certs)),
			Description: strings.Join(descs, "\n"),
			Engine:      e.Name(),
		})
	}

	return cards, nil
}
