// Package usernotice defines the opt-in `user_notification` agent-channel
// class: one notice a Home Assistant automation author wrote, carried from a
// node to Techstack and dispatched as a Feed item (Companion Feed contract v1
// section 2.5, STACKKITS-PRODUCT-BOUNDARY-STANDARD.md §14.9).
//
// The class is not telemetry. It carries only the text the author wrote, an
// automation id and an audience. Entity states, presence, locks, cameras and
// sensor history are never part of it, and the node holds no
// notifications-engine credential: Core authenticates the agent, bounds the
// text, rate-limits the node, resolves the recipients and dispatches.
package usernotice

import (
	"context"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// SourceHomeAssistant is the only admitted source.
	SourceHomeAssistant = "home_assistant"

	AudienceMe        = "me"
	AudienceHousehold = "household"
	AudienceMember    = "member"

	// MaxTextRunes bounds the user-authored text.
	MaxTextRunes = 280
	// RateLimit is the number of notices one node may send per RateWindow.
	RateLimit = 30
	// RateWindow is the sliding window of the per-node rate limit.
	RateWindow = time.Hour
)

// Refusal codes. A refusal is a structured answer, never a transport error.
const (
	RefusalSourceInvalid       = "source_invalid"
	RefusalAutomationInvalid   = "automation_id_invalid"
	RefusalTextRequired        = "text_required"
	RefusalTextTooLong         = "text_too_long"
	RefusalTextInvalid         = "text_invalid"
	RefusalAudienceInvalid     = "audience_invalid"
	RefusalMemberRequired      = "member_username_required"
	RefusalMemberNotLinked     = "member_not_linked"
	RefusalNoRecipients        = "no_recipients"
	RefusalRateLimited         = "rate_limited"
	RefusalNotConfigured       = "user_notifications_unavailable"
	RefusalNodeUnresolved      = "node_unresolved"
	refusalNone                = ""
	maxAutomationIDLength      = 128
	maxMemberUsernameLength    = 64
	automationIDPatternLiteral = `^[A-Za-z0-9][A-Za-z0-9._:-]*$`
	memberUsernamePattern      = `^[A-Za-z0-9][A-Za-z0-9._-]*$`
)

var (
	automationIDPattern = regexp.MustCompile(automationIDPatternLiteral)
	memberPattern       = regexp.MustCompile(memberUsernamePattern)
)

// Notice is one user-authored notice as the node reports it.
type Notice struct {
	AutomationID   string
	Text           string
	Audience       string
	MemberUsername string
	Source         string
}

// Node is the authenticated node a notice came from. It always comes from the
// agent's authentication, never from the notice.
type Node struct {
	TenantID       string
	StackID        string
	OwnerSubjectID string
	AgentID        string
}

// Result is the structured outcome of one report.
type Result struct {
	Accepted    bool
	RefusalCode string
	RetryAfter  time.Duration
	Recipients  int
}

// Sink admits notices. The Techstack service implements it; the gRPC server
// and the HTTPS worker route call it after authenticating the agent.
type Sink interface {
	Report(ctx context.Context, node Node, notice Notice) (Result, error)
}

// NodeResolver maps an authenticated agent to its node identity.
type NodeResolver interface {
	ResolveNode(ctx context.Context, tenantID, agentID string) (Node, error)
}

// Normalize trims the notice and applies the defaults. The source defaults to
// Home Assistant, the audience to me.
func (n Notice) Normalize() Notice {
	n.AutomationID = strings.TrimSpace(n.AutomationID)
	n.Text = strings.TrimSpace(n.Text)
	n.Audience = strings.ToLower(strings.TrimSpace(n.Audience))
	n.MemberUsername = strings.TrimSpace(n.MemberUsername)
	n.Source = strings.ToLower(strings.TrimSpace(n.Source))
	if n.Source == "" {
		n.Source = SourceHomeAssistant
	}
	if n.Audience == "" {
		n.Audience = AudienceMe
	}
	return n
}

// Refusal returns the refusal code of a malformed notice, or "" when the notice
// is well formed. The notice must be normalized.
func (n Notice) Refusal() string {
	if n.Source != SourceHomeAssistant {
		return RefusalSourceInvalid
	}
	if len(n.AutomationID) > maxAutomationIDLength || !automationIDPattern.MatchString(n.AutomationID) {
		return RefusalAutomationInvalid
	}
	if n.Text == "" {
		return RefusalTextRequired
	}
	if utf8.RuneCountInString(n.Text) > MaxTextRunes {
		return RefusalTextTooLong
	}
	for _, r := range n.Text {
		if r == '\n' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) || r == utf8.RuneError {
			return RefusalTextInvalid
		}
	}
	switch n.Audience {
	case AudienceMe, AudienceHousehold:
		if n.MemberUsername != "" {
			return RefusalAudienceInvalid
		}
	case AudienceMember:
		if n.MemberUsername == "" {
			return RefusalMemberRequired
		}
		if len(n.MemberUsername) > maxMemberUsernameLength || !memberPattern.MatchString(n.MemberUsername) {
			return RefusalAudienceInvalid
		}
	default:
		return RefusalAudienceInvalid
	}
	return refusalNone
}

// Subject is the Feed item subject: the first non-empty line of the text,
// cut to a readable length.
func (n Notice) Subject() string {
	for _, line := range strings.Split(n.Text, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			runes := []rune(line)
			if len(runes) > 120 {
				return string(runes[:119]) + "…"
			}
			return line
		}
	}
	return n.AutomationID
}

// ActorSubject is the Feed actor subject of a node: "node:<stack_id>".
func ActorSubject(stackID string) string { return "node:" + strings.TrimSpace(stackID) }
