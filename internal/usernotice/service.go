// Package usernotice turns an admitted node notice into Feed items. It is the
// Techstack producer of the `homelab.smarthome.notice` topic.
package usernotice

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/pkg/controlplane"
	pkgusernotice "github.com/kombifyio/techstack/pkg/usernotice"
)

const (
	// Topic is the Feed topic of a smart-home notice (Feed contract v1 2.1).
	Topic = "homelab.smarthome.notice"

	sourceApp = "techstack"
)

// Links reads the household identity references of an owner.
type Links interface {
	ListHouseholdRefsByOwner(ctx context.Context, tenantID, ownerSubjectID string) ([]controlplane.HomelabIdentityRef, error)
}

// Stacks reads the stack whose name is shown as the actor.
type Stacks interface {
	GetStack(ctx context.Context, tenantID, stackID string) (*controlplane.Stack, error)
}

// Outbox is the durable notification outbox.
type Outbox interface {
	Enqueue(ctx context.Context, event notifications.ProductEvent) error
}

// Service admits, rate-limits and dispatches node notices.
type Service struct {
	Admission controlplane.UserNoticeAdmissionStore
	Links     Links
	Stacks    Stacks
	Outbox    Outbox
	Now       func() time.Time
}

var _ pkgusernotice.Sink = (*Service)(nil)

// Report validates one notice, resolves its recipients, enforces the per-node
// rate limit and enqueues one Feed item per recipient and channel. Every
// refusal is a structured Result; an error means a dependency failed and the
// node may retry.
func (s *Service) Report(ctx context.Context, node pkgusernotice.Node, notice pkgusernotice.Notice) (pkgusernotice.Result, error) {
	if s == nil || s.Admission == nil || s.Links == nil || s.Outbox == nil {
		return pkgusernotice.Result{RefusalCode: pkgusernotice.RefusalNotConfigured}, nil
	}
	node.TenantID, node.StackID, node.OwnerSubjectID = strings.TrimSpace(node.TenantID), strings.TrimSpace(node.StackID), strings.TrimSpace(node.OwnerSubjectID)
	if node.TenantID == "" || node.StackID == "" || node.OwnerSubjectID == "" {
		return pkgusernotice.Result{RefusalCode: pkgusernotice.RefusalNodeUnresolved}, nil
	}
	notice = notice.Normalize()
	if code := notice.Refusal(); code != "" {
		return pkgusernotice.Result{RefusalCode: code}, nil
	}
	recipients, refusal, err := s.recipients(ctx, node, notice)
	if err != nil {
		return pkgusernotice.Result{}, err
	}
	if refusal != "" {
		return pkgusernotice.Result{RefusalCode: refusal}, nil
	}
	now := s.now()
	admitted, retryAfter, err := s.Admission.AdmitUserNotice(ctx, node.TenantID, node.StackID, now, pkgusernotice.RateLimit, pkgusernotice.RateWindow)
	if err != nil {
		return pkgusernotice.Result{}, err
	}
	if !admitted {
		return pkgusernotice.Result{RefusalCode: pkgusernotice.RefusalRateLimited, RetryAfter: retryAfter}, nil
	}

	actorName := s.actorName(ctx, node)
	noticeID := uuid.NewString()
	subject := notice.Subject()
	deepLink := "/stacks/" + url.PathEscape(node.StackID)
	eventKey := "usernotice." + node.StackID + "." + notice.AutomationID
	for _, recipient := range recipients {
		for _, channel := range []string{"in_app", "push"} {
			event := notifications.ProductEvent{
				Topic: Topic, Channel: channel, Auth0UserID: recipient, OrganizationID: node.TenantID,
				IdempotencyKey: fmt.Sprintf("techstack:usernotice:%s:%s:%s", noticeID, recipient, channel),
				SourceApp:      sourceApp, EventKey: eventKey,
				SubjectRef: "home-assistant:" + node.StackID + ":" + notice.AutomationID,
				DeepLink:   deepLink,
				// One Feed item per automation: a repeated notice replaces the
				// previous one in the recipient's Feed.
				GroupKey: "usernotice:" + node.StackID + ":" + notice.AutomationID,
				Priority: "normal",
				Actor:    &notifications.EventActor{Subject: pkgusernotice.ActorSubject(node.StackID), DisplayName: actorName},
				Detail: map[string]any{
					"kind": "user_notification", "source": notice.Source,
					"automation_id": notice.AutomationID, "stack_id": node.StackID,
				},
				Payload: map[string]any{
					"subject": subject, "body": notice.Text, "link_url": deepLink,
					"occurred_at": now.UTC().Format(time.RFC3339Nano), "source_app": sourceApp,
				},
			}
			if err := s.Outbox.Enqueue(ctx, event); err != nil {
				return pkgusernotice.Result{}, err
			}
		}
	}
	return pkgusernotice.Result{Accepted: true, Recipients: len(recipients)}, nil
}

// recipients resolves the audience. The owner is always an Auth0 subject; a
// household member is a recipient only when a Pocket ID login linked them to
// this stack (D10): an invitation alone never makes anyone a recipient.
func (s *Service) recipients(ctx context.Context, node pkgusernotice.Node, notice pkgusernotice.Notice) ([]string, string, error) {
	if notice.Audience == pkgusernotice.AudienceMe {
		return []string{node.OwnerSubjectID}, "", nil
	}
	refs, err := s.Links.ListHouseholdRefsByOwner(ctx, node.TenantID, node.OwnerSubjectID)
	if err != nil && !errors.Is(err, controlplane.ErrNotFound) {
		return nil, "", err
	}
	linked := map[string]string{}
	for _, ref := range refs {
		if ref.StackID == node.StackID && ref.Status == controlplane.HouseholdLinkLinked {
			linked[strings.ToLower(ref.Username)] = ref.CloudSubject
		}
	}
	if notice.Audience == pkgusernotice.AudienceMember {
		subject, ok := linked[strings.ToLower(notice.MemberUsername)]
		if !ok {
			return nil, pkgusernotice.RefusalMemberNotLinked, nil
		}
		return []string{subject}, "", nil
	}
	set := map[string]struct{}{node.OwnerSubjectID: {}}
	for _, subject := range linked {
		set[subject] = struct{}{}
	}
	out := make([]string, 0, len(set))
	for subject := range set {
		out = append(out, subject)
	}
	sort.Strings(out)
	return out, "", nil
}

func (s *Service) actorName(ctx context.Context, node pkgusernotice.Node) string {
	if s.Stacks != nil {
		if stack, err := s.Stacks.GetStack(ctx, node.TenantID, node.StackID); err == nil && stack != nil {
			if name := strings.TrimSpace(stack.Name); name != "" {
				return name
			}
		}
	}
	return "Homelab"
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now().UTC()
}
