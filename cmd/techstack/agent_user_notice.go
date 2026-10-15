package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/noticelistener"
	"github.com/kombifyio/techstack/pkg/usernotice"
)

const userNotificationDirEnv = "TECHSTACK_USER_NOTIFICATION_DIR"

// userNotificationDir is the kombify-notify directory StackKits provisions next
// to the access manifest. Empty means the opt-in class is not configured.
func userNotificationDir(cfg *agentModeConfig) string {
	if dir := strings.TrimSpace(envDefault(userNotificationDirEnv, "")); dir != "" {
		return dir
	}
	return noticelistener.DirFor(cfg.accessManifest)
}

// startUserNotificationListener runs the node-local door for smart-home notices
// when StackKits provisioned a token. It never blocks the agent: without the
// directory nothing runs.
func startUserNotificationListener(ctx context.Context, cfg *agentModeConfig, forwarder noticelistener.Forwarder, log *slog.Logger) {
	dir := userNotificationDir(cfg)
	if dir == "" || forwarder == nil {
		return
	}
	go noticelistener.Run(ctx, noticelistener.Config{Dir: dir, Forwarder: forwarder, Logger: log})
}

// httpsNoticeForwarder sends one notice over the Guard's authenticated HTTPS
// channel (key-bound transport, or the bearer credential of a legacy Guard).
type httpsNoticeForwarder struct {
	url        string
	client     *http.Client
	agentToken string
	tenantID   string
}

func newHTTPSNoticeForwarder(enrollment agentEnrollment, identity guardIdentityRuntime) *httpsNoticeForwarder {
	url := derivedAgentControlURL(enrollment.HeartbeatURL, "user-notification")
	if url == "" {
		return nil
	}
	return &httpsNoticeForwarder{
		url: url, client: identity.httpClient(30 * time.Second), agentToken: enrollment.AgentToken, tenantID: enrollment.TenantID,
	}
}

func (f *httpsNoticeForwarder) Forward(ctx context.Context, notice usernotice.Notice) (usernotice.Result, error) {
	body, err := json.Marshal(map[string]string{
		"automation_id": notice.AutomationID, "text": notice.Text, "audience": notice.Audience,
		"member_username": notice.MemberUsername, "source": notice.Source,
	})
	if err != nil {
		return usernotice.Result{}, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, f.url, bytes.NewReader(body))
	if err != nil {
		return usernotice.Result{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if token := strings.TrimSpace(f.agentToken); token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	if tenant := strings.TrimSpace(f.tenantID); tenant != "" {
		request.Header.Set("X-Kombify-Tenant-ID", tenant)
	}
	response, err := f.client.Do(request)
	if err != nil {
		return usernotice.Result{}, err
	}
	defer func() { _ = response.Body.Close() }()
	payload, _ := io.ReadAll(io.LimitReader(response.Body, 64<<10))
	switch response.StatusCode {
	case http.StatusAccepted:
		var envelope struct {
			Data struct {
				UserNotification struct {
					Recipients int `json:"recipients"`
				} `json:"user_notification"`
			} `json:"data"`
		}
		_ = json.Unmarshal(payload, &envelope)
		return usernotice.Result{Accepted: true, Recipients: envelope.Data.UserNotification.Recipients}, nil
	case http.StatusUnprocessableEntity, http.StatusTooManyRequests, http.StatusConflict:
		var envelope struct {
			Error struct {
				Details struct {
					ReasonCode        string `json:"reason_code"`
					RetryAfterSeconds int    `json:"retry_after_seconds"`
				} `json:"details"`
			} `json:"error"`
		}
		reason := ""
		if json.Unmarshal(payload, &envelope) == nil {
			reason = envelope.Error.Details.ReasonCode
		}
		retry := envelope.Error.Details.RetryAfterSeconds
		if retry == 0 {
			retry, _ = strconv.Atoi(response.Header.Get("Retry-After"))
		}
		if reason == "" && response.StatusCode == http.StatusTooManyRequests {
			// The edge's own per-address limiter answers without a reason code.
			reason = usernotice.RefusalRateLimited
		}
		if reason == "" {
			return usernotice.Result{}, errors.New("the control plane returned an unusable refusal")
		}
		return usernotice.Result{RefusalCode: reason, RetryAfter: time.Duration(retry) * time.Second}, nil
	default:
		return usernotice.Result{}, fmt.Errorf("control plane answered HTTP %d", response.StatusCode)
	}
}
