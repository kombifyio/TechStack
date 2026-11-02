package household

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
)

// NewActivationDelivery uses the existing Notifications owner. The private
// process has only dispatch custody; it never manufactures human OBO claims.
func NewActivationDelivery(origin, privateKey string) (ActivationDelivery, error) {
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("fixed notifications HTTPS origin required")
	}
	kid, key, err := servicecall.ParseSigningKey(privateKey)
	if err != nil {
		return nil, errors.New("household notifications signing custody required")
	}
	cfg := servicecall.Config{ServiceName: "household-worker", SigningKeyID: kid, SigningKey: key, Scope: "notifications:dispatch", TokenTTL: time.Minute}
	client := &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	endpoint := strings.TrimRight(origin, "/") + "/v1/notifications/dispatch"
	return func(ctx context.Context, p PendingProjection, result ProjectionResult) error {
		link, err := url.Parse(result.ActivationURL)
		if err != nil || link.Scheme != "https" || link.Host == "" || link.User != nil || result.ExpiresAt == nil || !result.ExpiresAt.After(time.Now()) {
			return errors.New("household activation is unavailable or expired")
		}
		sum := sha256.Sum256([]byte(result.ActivationURL))
		digest := hex.EncodeToString(sum[:16])
		delivered, ownerDelivered := false, false
		for _, target := range activationTargets(p, result) {
			channel := target.channel
			payload := map[string]any{"topic_slug": "household.invite", "channel": channel, "recipient": target.recipient, "payload": map[string]any{},
				"source_app": "cloud", "event_key": "household.homelab_activation", "subject_ref": "household-activation:" + digest, "deep_link": result.ActivationURL, "priority": "normal",
				"idempotency_key": "hh-activation:" + digest + ":" + target.key, "message": target.message}
			body, err := json.Marshal(payload)
			if err != nil {
				return errors.New("household notification encoding failed")
			}
			// Notifications' existing finite dispatch contract accepts service JWTs;
			// unlike the internal relay, its receiver has no bound-request contract.
			token, err := servicecall.IssueToken(cfg, "notifications", nil, "")
			if err != nil {
				clear(body)
				return errors.New("household notification signing failed")
			}
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
			if err != nil {
				clear(body)
				return errors.New("household notification request failed")
			}
			req.Header.Set(servicecall.HeaderServiceAuth, token)
			req.Header.Set("Content-Type", "application/json")
			response, err := client.Do(req)
			clear(body)
			if err != nil {
				continue
			}
			raw, readErr := io.ReadAll(io.LimitReader(response.Body, (64<<10)+1))
			response.Body.Close()
			var receipt struct {
				DispatchID string `json:"dispatch_id"`
				FeedItemID string `json:"feed_item_id"`
				Status     string `json:"status"`
			}
			valid := readErr == nil && len(raw) <= 64<<10 && response.StatusCode >= 200 && response.StatusCode < 300 && json.Unmarshal(raw, &receipt) == nil && strings.TrimSpace(receipt.DispatchID) != ""
			clear(raw)
			confirmed := valid && channel == "email" && receipt.Status == "sent"
			if valid && channel == "in_app" && strings.TrimSpace(receipt.FeedItemID) != "" {
				switch receipt.Status {
				case "queued", "rendering", "sent", "delivered":
					confirmed = true
				}
			}
			delivered = delivered || confirmed
			ownerDelivered = ownerDelivered || (confirmed && strings.HasPrefix(target.key, "owner:"))
		}
		// A homelab member's link is handed over by the owner; the owner must have it.
		if !delivered || (p.Homelab && !ownerDelivered) {
			return errors.New("household activation delivery unconfirmed")
		}
		return nil
	}, nil
}

type activationTarget struct {
	channel, key string
	recipient    map[string]string
	message      map[string]any
}

// activationTargets names who receives the one-time activation link. A kombify
// member receives it in the Feed and by mail. A homelab member has no account:
// the owner receives it to pass on, in the Feed and by mail, and the address
// the owner entered for the member receives it by mail (D-2026-11-01-05).
func activationTargets(p PendingProjection, result ProjectionResult) []activationTarget {
	expires := result.ExpiresAt.Format(time.RFC3339)
	member := map[string]any{"id": "cloud.household.homelab_activation",
		"params":          map[string]string{"household": p.HouseholdName, "link": result.ActivationURL, "expires": expires},
		"fallback_locale": "en", "fallback_subject": "Activate your homelab access", "fallback_body": "Activate your access to the homelab of " + p.HouseholdName + ": " + result.ActivationURL}
	if !p.Homelab {
		recipient := map[string]string{"auth0_user_id": p.Member.Subject}
		return []activationTarget{{"in_app", "in_app", recipient, member}, {"email", "email", recipient, member}}
	}
	owner := map[string]string{"auth0_user_id": p.OwnerSubject}
	handOver := map[string]any{"id": "cloud.household.homelab_member_activation",
		"params":          map[string]string{"household": p.HouseholdName, "member": p.Member.DisplayName, "link": result.ActivationURL, "expires": expires},
		"fallback_locale": "en", "fallback_subject": "Homelab access for " + p.Member.DisplayName,
		"fallback_body": "Pass this link to " + p.Member.DisplayName + " to activate their access to the homelab of " + p.HouseholdName + ": " + result.ActivationURL}
	targets := []activationTarget{{"in_app", "owner:in_app", owner, handOver}, {"email", "owner:email", owner, handOver}}
	if p.ContactEmail != "" {
		targets = append(targets, activationTarget{"email", "member:email", map[string]string{"email": p.ContactEmail}, member})
	}
	return targets
}
