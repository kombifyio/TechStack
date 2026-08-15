package monitoring

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Alert represents a fired alert with context.
type Alert struct {
	RuleName   string            `json:"rule_name"`
	Severity   string            `json:"severity"` // info, warning, critical
	Message    string            `json:"message"`
	Value      float64           `json:"value"`
	Labels     map[string]string `json:"labels,omitempty"`
	FiredAt    time.Time         `json:"fired_at"`
	ResolvedAt *time.Time        `json:"resolved_at,omitempty"`
}

// Notifier sends alert notifications to external systems.
type Notifier interface {
	// Notify sends an alert. Implementations must respect context cancellation.
	Notify(ctx context.Context, alert Alert) error
	// Name returns the notifier type name.
	Name() string
}

// TelegramNotifier sends alerts via the Telegram Bot API.
type TelegramNotifier struct {
	botToken string
	chatID   string
	client   *http.Client
}

// NewTelegramNotifier creates a Telegram notifier. botToken and chatID are required.
func NewTelegramNotifier(botToken, chatID string) *TelegramNotifier {
	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		client:   &http.Client{Timeout: 10 * time.Second},
	}
}

func (t *TelegramNotifier) Name() string { return "telegram" }

func (t *TelegramNotifier) Notify(ctx context.Context, alert Alert) error {
	icon := severityIcon(alert.Severity)
	text := fmt.Sprintf("%s *%s* [%s]\n\n%s\nValue: `%.2f`\nFired: %s",
		icon, alert.RuleName, alert.Severity, alert.Message, alert.Value, alert.FiredAt.Format(time.RFC3339))

	if len(alert.Labels) > 0 {
		text += "\n\nLabels:"
		for k, v := range alert.Labels {
			text += fmt.Sprintf("\n  `%s`: `%s`", k, v)
		}
	}

	payload := map[string]any{
		"chat_id":    t.chatID,
		"text":       text,
		"parse_mode": "Markdown",
	}

	body, _ := json.Marshal(payload)
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", t.botToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.client.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram message: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("telegram API error %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// WebhookNotifier sends alerts as JSON POST to a configurable URL.
type WebhookNotifier struct {
	url    string
	client *http.Client
}

// NewWebhookNotifier creates a webhook notifier that POSTs JSON alerts.
func NewWebhookNotifier(url string) *WebhookNotifier {
	return &WebhookNotifier{
		url:    url,
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

func (w *WebhookNotifier) Name() string { return "webhook" }

func (w *WebhookNotifier) Notify(ctx context.Context, alert Alert) error {
	body, err := json.Marshal(alert)
	if err != nil {
		return fmt.Errorf("marshal alert: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create webhook request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := w.client.Do(req)
	if err != nil {
		return fmt.Errorf("send webhook: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("webhook error %d: %s", resp.StatusCode, string(respBody))
	}

	return nil
}

// MultiNotifier fans out to multiple notifiers. Continues on individual failures.
type MultiNotifier struct {
	notifiers []Notifier
}

func NewMultiNotifier(notifiers ...Notifier) *MultiNotifier {
	return &MultiNotifier{notifiers: notifiers}
}

func (m *MultiNotifier) Name() string { return "multi" }

func (m *MultiNotifier) Notify(ctx context.Context, alert Alert) error {
	var lastErr error
	for _, n := range m.notifiers {
		if err := n.Notify(ctx, alert); err != nil {
			lastErr = fmt.Errorf("%s: %w", n.Name(), err)
		}
	}
	return lastErr
}

func severityIcon(severity string) string {
	switch severity {
	case "critical":
		return "[CRITICAL]"
	case "warning":
		return "[WARNING]"
	case "info":
		return "[INFO]"
	default:
		return "[ALERT]"
	}
}
