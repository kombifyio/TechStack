package actions

import (
	"errors"
	"testing"

	"github.com/kombifyio/techstack/pkg/ril/actioncontract"
)

func TestConnectorBindingDigestMatchesGatewayCanonicalTuple(t *testing.T) {
	projection := testConnectorProjection()
	want := "sha256:ab24eab143fc3d55f1bed75314a1f62af5aaaf545125f3d49983ff7fb565b113"
	if got := connectorBindingDigest("tenant-1", "owner-1", projection); got != want {
		t.Fatalf("binding digest = %q, want Gateway vector %q", got, want)
	}
}

func TestConnectorBindingAdmissionRejectsNonExactOrInsufficientProjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*GovernedCard, *BeginExecution)
		want   error
	}{
		{name: "missing binding", mutate: func(_ *GovernedCard, input *BeginExecution) { input.ConnectorProjection = nil }, want: ErrConnectorBindingRequired},
		{name: "wrong server", mutate: func(_ *GovernedCard, input *BeginExecution) { input.ConnectorProjection.ServerID = "server-other" }, want: ErrConnectorBindingRequired},
		{name: "wrong action card", mutate: func(_ *GovernedCard, input *BeginExecution) { input.ConnectorProjection.ResourceID = "card-other" }, want: ErrConnectorBindingRequired},
		{name: "revoked", mutate: func(_ *GovernedCard, input *BeginExecution) { input.ConnectorProjection.Status = "revoked" }, want: ErrConnectorBindingRequired},
		{name: "read-only grant for mutation", mutate: func(card *GovernedCard, input *BeginExecution) {
			card.Template.Primitive.OperationClass = "product-apply"
			input.ConnectorProjection.Scopes = []string{"mcp:tools:read"}
			input.ConnectorProjection.BindingHash = connectorBindingDigest(input.TenantID, input.OwnerSubjectID, *input.ConnectorProjection)
			card.Template.ConnectorBinding.BindingHash = input.ConnectorProjection.BindingHash
		}, want: ErrConnectorGrantInsufficient},
		{name: "accepted exact scoped binding", mutate: func(_ *GovernedCard, _ *BeginExecution) {}, want: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			projection := testConnectorProjection()
			projection.BindingHash = connectorBindingDigest("tenant-1", "owner-1", projection)
			card := &GovernedCard{
				ID: "card-1", ServerID: "server-1",
				Template: ActionTemplate{
					Primitive:        rilaction.PrimitiveBinding{OperationClass: "verification"},
					ConnectorBinding: &ConnectorBindingExpectation{BindingID: projection.BindingID, BindingHash: projection.BindingHash},
				},
			}
			input := BeginExecution{TenantID: "tenant-1", OwnerSubjectID: "owner-1", ConnectorProjection: &projection}
			test.mutate(card, &input)
			err := validateConnectorBinding(card, input)
			if !errors.Is(err, test.want) || (test.want == nil && err != nil) {
				t.Fatalf("error = %v, want %v", err, test.want)
			}
		})
	}
}

func testConnectorProjection() ConnectorBindingProjection {
	return ConnectorBindingProjection{
		GrantID: "grant-1", BindingID: "bind-1", ConnectorID: "kombify-tools",
		BindingScope: "action-card", ResourceID: "card-1", ServerID: "server-1",
		Scopes: []string{"mcp:tools"}, Status: "active",
	}
}
