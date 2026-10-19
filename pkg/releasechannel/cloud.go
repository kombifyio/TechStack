package releasechannel

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/kombifyio/techstack/pkg/serviceauth"
)

const (
	// EnvCloudOrigin overrides the Cloud origin stable reads the list from.
	EnvCloudOrigin = "TECHSTACK_CLOUD_ORIGIN"
	// HandOverPath is stable's route serving the list it honors to Preview.
	HandOverPath = "/api/v1/internal/release-channel/preview-owners"

	defaultCloudOrigin = "https://app.kombify.io"
	previewSubjectsURI = "/api/v1/internal/release-channel/preview-subjects"
	maxListBytes       = 2 << 20
)

// CloudOrigin is stable Cloud, the one store of the choice.
func CloudOrigin() string {
	if origin := httpsOrigin(os.Getenv(EnvCloudOrigin)); origin != "" {
		return origin
	}
	return defaultCloudOrigin
}

// StableOrigin is stable Techstack, which Preview reads the hand-over from.
func StableOrigin() string {
	return httpsOrigin(os.Getenv("TECHSTACK_STABLE_ORIGIN"))
}

func httpsOrigin(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" {
		return ""
	}
	return parsed.Scheme + "://" + parsed.Host
}

// NewSource returns the list source of this channel: stable reads Cloud,
// Preview reads stable's hand-over. Both use a short-lived servicecall token.
func NewSource(secret string, client *http.Client) (Source, error) {
	if Running() == "preview" {
		origin := StableOrigin()
		if origin == "" {
			return nil, fmt.Errorf("TECHSTACK_STABLE_ORIGIN is required on Preview")
		}
		return newServiceSource(origin+HandOverPath, "techstack", secret, client)
	}
	return newServiceSource(CloudOrigin()+previewSubjectsURI, "cloud", secret, client)
}

func newServiceSource(endpoint, target, secret string, client *http.Client) (Source, error) {
	auth, err := serviceauth.Apply(servicecall.Config{ServiceName: "techstack", Target: target, Secret: strings.TrimSpace(secret)})
	if err != nil {
		return nil, err
	}
	if auth.SigningKey == nil && auth.Secret == "" {
		return nil, servicecall.ErrEmptySecret
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	return func(ctx context.Context) ([]string, bool, error) {
		token, err := servicecall.IssueToken(auth, target, nil, "")
		if err != nil {
			return nil, false, fmt.Errorf("issue service token: %w", err)
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, false, err
		}
		request.Header.Set("Accept", "application/json")
		request.Header.Set(servicecall.HeaderServiceAuth, token)
		response, err := client.Do(request)
		if err != nil {
			return nil, false, fmt.Errorf("read release channel list: %w", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			return nil, false, fmt.Errorf("read release channel list: http_%d", response.StatusCode)
		}
		var body struct {
			Subjects []string `json:"subjects"`
			Complete *bool    `json:"complete"`
		}
		if err := json.NewDecoder(io.LimitReader(response.Body, maxListBytes)).Decode(&body); err != nil {
			return nil, false, fmt.Errorf("decode release channel list: %w", err)
		}
		if body.Subjects == nil || body.Complete == nil {
			return nil, false, fmt.Errorf("decode release channel list: missing fields")
		}
		return body.Subjects, *body.Complete, nil
	}, nil
}

// HandOverHandler serves the list stable honors to Preview, only to a
// Techstack service token; 503 until stable read Cloud once.
func HandOverHandler(secret, secretNext string) (http.Handler, error) {
	auth, err := serviceauth.Apply(servicecall.Config{
		ServiceName:    "techstack",
		Secret:         strings.TrimSpace(secret),
		SecretNext:     strings.TrimSpace(secretNext),
		AllowedCallers: []string{"techstack"},
		Enabled:        true,
	})
	if err != nil {
		return nil, err
	}
	return servicecall.RequireServiceAuth(auth)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		subjects, ok := current().Honored()
		if !ok || Running() == "preview" {
			http.Error(w, `{"error":"release_channel_list_unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{"subjects": subjects, "complete": true})
	})), nil
}
