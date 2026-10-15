package grpcserver

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/kombifyio/techstack/internal/notifications"
	internalusernotice "github.com/kombifyio/techstack/internal/usernotice"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/usernotice"
)

type recordingOutbox struct {
	mu     sync.Mutex
	events []notifications.ProductEvent
}

func (o *recordingOutbox) Enqueue(_ context.Context, event notifications.ProductEvent) error {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.events = append(o.events, event)
	return nil
}

func (o *recordingOutbox) count() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.events)
}

type staticNodes map[string]usernotice.Node

func (n staticNodes) ResolveNode(_ context.Context, _, agentID string) (usernotice.Node, error) {
	return n[agentID], nil
}

// Sensitive boundary of the opt-in user_notification class: a node may carry
// user-authored text of at most 280 characters, and at most 30 notices per hour
// per node. A refusal is a structured ack and dispatches nothing.
func TestReportUserNotificationRefusesLongTextAndRateLimitsPerNode(t *testing.T) {
	outbox := &recordingOutbox{}
	service := &internalusernotice.Service{
		Admission: controlplane.NewMemoryUserNoticeAdmissionStore(),
		Links:     controlplane.NewMemoryHouseholdLinkStore(),
		Outbox:    outbox,
	}
	srv, err := New(Config{ListenAddr: ":0"}, logger.Get())
	if err != nil {
		t.Fatal(err)
	}
	srv.SetUserNotices(service, staticNodes{
		"node-a": {TenantID: "tenant-1", StackID: "stack-a", OwnerSubjectID: "auth0|owner"},
		"node-b": {TenantID: "tenant-1", StackID: "stack-b", OwnerSubjectID: "auth0|owner"},
	})
	for _, id := range []string{"node-a", "node-b"} {
		if err := srv.RegisterAgent(&ConnectedAgent{ID: id, Tenant: "tenant-1"}); err != nil {
			t.Fatal(err)
		}
	}
	send := func(agent, text string) *agentpb.UserNotificationAck {
		t.Helper()
		ack, err := srv.ReportUserNotification(context.Background(), &agentpb.UserNotification{
			AgentId: agent, AutomationId: "washer", Text: text, Audience: usernotice.AudienceMe, Source: usernotice.SourceHomeAssistant,
		})
		if err != nil {
			t.Fatal(err)
		}
		return ack
	}

	if ack := send("node-a", strings.Repeat("ä", usernotice.MaxTextRunes+1)); ack.GetAccepted() || ack.GetRefusalCode() == "" || outbox.count() != 0 {
		t.Fatalf("text over the bound must be refused without dispatch: %v (dispatched %d)", ack, outbox.count())
	}
	if ack := send("node-a", strings.Repeat("ä", usernotice.MaxTextRunes)); !ack.GetAccepted() {
		t.Fatalf("text at the bound must be accepted: %v", ack)
	}
	for i := 1; i < usernotice.RateLimit; i++ {
		if ack := send("node-a", "Waschmaschine fertig"); !ack.GetAccepted() {
			t.Fatalf("notice %d of the hourly allowance was refused: %v", i+1, ack)
		}
	}
	dispatched := outbox.count()
	over := send("node-a", "Waschmaschine fertig")
	if over.GetAccepted() || over.GetRetryAfterSeconds() <= 0 || outbox.count() != dispatched {
		t.Fatalf("the notice over the node limit must be refused with a retry delay and no dispatch: %v", over)
	}
	if ack := send("node-b", "Tür offen"); !ack.GetAccepted() {
		t.Fatalf("another node must not share the limit: %v", ack)
	}
}
