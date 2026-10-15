package notifications

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

// DigestScope is the service-JWT scope of the digest contributions route.
const DigestScope = "notifications:digest"

// DigestProducerHomelab is the producer id of Techstack's Homelab numbers.
const DigestProducerHomelab = "techstack.homelab"

// DigestSectionHomelab is the Feed section the Homelab numbers belong to.
const DigestSectionHomelab = "homelab"

// DigestMetric is one number of a digest contribution. The tone is
// neutral, good, warn or critical.
type DigestMetric struct {
	ID    string `json:"id"`
	Value int    `json:"value"`
	Tone  string `json:"tone"`
}

// DigestMessage is the language-neutral digest line: a message id and its
// parameters. The engine renders it in the recipient's locale.
type DigestMessage struct {
	ID     string         `json:"id"`
	Params map[string]any `json:"params"`
}

// DigestPointer is a pointer a digest row offers; Techstack sends none.
type DigestPointer struct {
	SubjectRef string `json:"subject_ref"`
	DeepLink   string `json:"deep_link"`
}

// DigestContribution is one producer's numbers for one recipient and local day
// (Feed contract v1 section 2.4). The engine keys it by
// (recipient, section, producer, day); the latest write wins.
type DigestContribution struct {
	Auth0UserID string
	Section     string
	Producer    string
	Day         string // YYYY-MM-DD in the recipient's local day
	Message     DigestMessage
	Metrics     []DigestMetric
	Pointers    []DigestPointer
	DeepLink    string
}

// UpsertDigestContribution writes one contribution with the
// notifications:digest scope. A non-2xx answer is returned as a DispatchError
// so callers can tell a retryable outage from a refusal.
func (e *Engine) UpsertDigestContribution(ctx context.Context, contribution DigestContribution) error {
	if strings.TrimSpace(contribution.Auth0UserID) == "" {
		return ErrRecipientRequired
	}
	if contribution.Section == "" || contribution.Producer == "" || contribution.Day == "" || contribution.Message.ID == "" {
		return fmt.Errorf("notifications: digest contribution requires section, producer, day and message id")
	}
	pointers := contribution.Pointers
	if pointers == nil {
		pointers = []DigestPointer{}
	}
	metrics := contribution.Metrics
	if metrics == nil {
		metrics = []DigestMetric{}
	}
	params := contribution.Message.Params
	if params == nil {
		params = map[string]any{}
	}
	body, err := json.Marshal(map[string]any{
		"recipient": map[string]string{"auth0_user_id": strings.TrimSpace(contribution.Auth0UserID)},
		"section":   contribution.Section,
		"producer":  contribution.Producer,
		"day":       contribution.Day,
		"message":   map[string]any{"id": contribution.Message.ID, "params": params},
		"metrics":   metrics,
		"pointers":  pointers,
		"deep_link": contribution.DeepLink,
	})
	if err != nil {
		return err
	}
	result, err := e.do(ctx, http.MethodPost, "/v1/notifications/digest/contributions", DigestScope, body)
	if err != nil {
		return err
	}
	if result.Status < 200 || result.Status >= 300 {
		var envelope struct {
			Error     string `json:"error"`
			ErrorCode string `json:"error_code"`
		}
		_ = json.Unmarshal(result.Body, &envelope)
		code := envelope.ErrorCode
		if code == "" {
			code = envelope.Error
		}
		return &DispatchError{StatusCode: result.Status, Code: code,
			Retryable: result.Status == http.StatusTooManyRequests || result.Status >= 500}
	}
	return nil
}
