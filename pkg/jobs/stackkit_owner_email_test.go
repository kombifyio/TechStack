package jobs

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/identity"
)

// Managed centron Apply 2026-09-23 stopped at init: StackKits requires a real
// local Owner email and Techstack never sent one. The Owner's own signed-in
// email reaches init; an email from any other principal never becomes the
// Owner identity, and no other operation carries it.
func TestManagedRolloutSendsOnlyTheOwnersEmailWithInit(t *testing.T) {
	for _, principal := range []struct {
		userID string
		want   string
	}{{userID: "auth0|owner", want: "owner@example.com"}, {userID: "auth0|support", want: ""}} {
		payload := map[string]interface{}{}
		ctx := identity.NewContext(context.Background(), &identity.Identity{UserID: principal.userID, Email: "owner@example.com"})
		CaptureStackOwnerEmail(ctx, payload, "auth0|owner")
		request := typedStackKitApplyRequest()
		request.OwnerEmail = stackOwnerEmail(&Job{Payload: payload}, "auth0|owner")

		sender := &recordingStackKitCommandSender{}
		if _, err := runTypedStackKitApplySequence(context.Background(), sender, "job-1", request, stackkitrelease.Release{}); err != nil {
			t.Fatalf("runTypedStackKitApplySequence: %v", err)
		}
		for _, command := range sender.commands {
			want := ""
			if command.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_INIT {
				want = principal.want
			}
			if command.OwnerEmail != want {
				t.Fatalf("principal %s: %s owner_email = %q, want %q", principal.userID, command.Operation, command.OwnerEmail, want)
			}
		}
	}
}
