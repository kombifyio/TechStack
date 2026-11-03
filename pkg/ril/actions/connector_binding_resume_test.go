package actions

import (
	"errors"
	"testing"
)

// The fixed value is Gateway's independently computed binding projection for
// this test vector; every changed input must fail before workflow resume.
func TestConnectorBindingFencesVerifyResume(t *testing.T) {
	const digest = "sha256:e3f89c4a800425b4dbb857014abfd74ab3c2afa1acd28de36d9c9ae6239a84d5"
	projection := ConnectorBindingProjection{GrantID: "grant-1", BindingID: "binding-1", BindingHash: digest, ConnectorID: "connector-1", BindingScope: "action-card", ResourceID: "card", ServerID: "server", Scopes: []string{"scope:read"}, Status: "active"}
	card := &GovernedCard{ID: "card", ServerID: "server", Template: ActionTemplate{ConnectorBinding: &ConnectorBindingExpectation{BindingID: "binding-1", BindingHash: digest}}}
	input := BeginExecution{TenantID: "tenant", OwnerSubjectID: "owner", ConnectorProjection: &projection}
	if err := validateConnectorBinding(card, input); err != nil {
		t.Fatalf("valid existing binding refused: %v", err)
	}
	cases := []struct {
		name   string
		change func(*BeginExecution)
	}{
		{"missing", func(x *BeginExecution) { x.ConnectorProjection = nil }},
		{"changed grant", func(x *BeginExecution) {
			p := *x.ConnectorProjection
			p.GrantID = "grant-2"
			x.ConnectorProjection = &p
		}},
		{"changed connector", func(x *BeginExecution) {
			p := *x.ConnectorProjection
			p.ConnectorID = "connector-2"
			x.ConnectorProjection = &p
		}},
		{"changed tenant", func(x *BeginExecution) { x.TenantID = "other" }},
		{"revoked", func(x *BeginExecution) { p := *x.ConnectorProjection; p.Status = "revoked"; x.ConnectorProjection = &p }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			x := input
			tc.change(&x)
			if err := validateConnectorBinding(card, x); !errors.Is(err, ErrConnectorBindingRequired) {
				t.Fatalf("changed projection was admitted: %v", err)
			}
		})
	}
}
