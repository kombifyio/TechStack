package signals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/kombifyio/go-common/servicecall"
)

const DefaultGatewaySignalURL = "https://api.kombify.io/v1/agents/ril-ops/signals"

type GatewayPublisher struct {
	url    string
	client *servicecall.Client
}

func NewGatewayPublisher(url, secret string, httpClient *http.Client) (*GatewayPublisher, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		url = DefaultGatewaySignalURL
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://127.0.0.1:") && !strings.HasPrefix(url, "http://localhost:") {
		return nil, errors.New("ril signals: Gateway URL must use HTTPS outside localhost")
	}
	client, err := servicecall.NewClient(servicecall.Config{
		ServiceName: "techstack",
		Target:      "gateway",
		Secret:      strings.TrimSpace(secret),
		TokenTTL:    time.Minute,
	})
	if err != nil {
		return nil, err
	}
	if httpClient != nil {
		client.WithHTTPClient(httpClient)
	}
	return &GatewayPublisher{url: url, client: client}, nil
}

func (p *GatewayPublisher) Publish(ctx context.Context, envelope Envelope) error {
	if p == nil || p.client == nil || strings.TrimSpace(p.url) == "" {
		return errors.New("ril signals: Gateway publisher not configured")
	}
	if !validTenantID(envelope.TenantID) || !stableID(envelope.SignalID) ||
		strings.TrimSpace(envelope.ActionCardID) == "" || strings.TrimSpace(envelope.UserID) == "" {
		return errors.New("ril signals: Gateway publish requires tenant, user, signal, and action-card identity")
	}
	response, err := p.client.PostJSON(ctx, p.url, envelope, &servicecall.OnBehalfOf{
		Sub: envelope.UserID, OrgID: envelope.TenantID,
	})
	if err != nil {
		return fmt.Errorf("ril signals: publish through Gateway: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, readErr := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	if readErr != nil {
		return fmt.Errorf("ril signals: read Gateway response: %w", readErr)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return fmt.Errorf("ril signals: Gateway rejected signal with status %d", response.StatusCode)
	}
	var receipt struct {
		OK         bool   `json:"ok"`
		SignalID   string `json:"signalId"`
		ActionCard struct {
			ID string `json:"id"`
		} `json:"actionCard"`
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		return fmt.Errorf("ril signals: invalid Gateway receipt: %w", err)
	}
	if !receipt.OK || receipt.SignalID != envelope.SignalID || receipt.ActionCard.ID != envelope.ActionCardID {
		return errors.New("ril signals: Gateway receipt identity mismatch")
	}
	return nil
}
