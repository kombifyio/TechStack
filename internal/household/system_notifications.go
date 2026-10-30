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
		delivered := false
		for _, channel := range []string{"in_app", "email"} {
			payload := map[string]any{"topic_slug": "household.invite", "channel": channel, "recipient": map[string]string{"auth0_user_id": p.Member.Subject}, "payload": map[string]any{},
				"source_app": "cloud", "event_key": "household.homelab_activation", "subject_ref": "household-activation:" + digest, "deep_link": result.ActivationURL, "priority": "normal",
				"idempotency_key": "hh-activation:" + digest + ":" + channel, "message": map[string]any{"id": "cloud.household.homelab_activation",
					"params":          map[string]string{"household": p.HouseholdName, "link": result.ActivationURL, "expires": result.ExpiresAt.Format(time.RFC3339)},
					"fallback_locale": "en", "fallback_subject": "Activate your homelab access", "fallback_body": "Activate your access to the homelab of " + p.HouseholdName + ": " + result.ActivationURL}}
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
			if valid && channel == "email" && receipt.Status == "sent" {
				delivered = true
			}
			if valid && channel == "in_app" && strings.TrimSpace(receipt.FeedItemID) != "" {
				switch receipt.Status {
				case "queued", "rendering", "sent", "delivered":
					delivered = true
				}
			}
		}
		if !delivered {
			return errors.New("household activation delivery unconfirmed")
		}
		return nil
	}, nil
}
