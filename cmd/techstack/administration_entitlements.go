package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"

	"github.com/kombifyio/techstack/pkg/serviceauth"
)

const (
	administrationCurrentPath   = "/api/v1/entitlements/current"
	administrationCurrentScope  = "entitlements:current"
	administrationCurrentSchema = "kombify.entitlements.current.v1"
	defaultAdministrationURL    = "https://admin.kombify.io"
	administrationReadTimeout   = 3 * time.Second
	administrationMaxKeys       = 1024
)

var (
	administrationEntitlementKey = regexp.MustCompile(`^[a-z0-9][a-z0-9._:-]{0,127}$`)
	administrationOrganization   = regexp.MustCompile(`^org_[A-Za-z0-9]{1,128}$`)
)

// cloudEntitlementReader returns a person's entitlements from Administration's
// committed broker projection. An error means "unknown", never "none": the
// caller grants nothing on it.
type cloudEntitlementReader interface {
	Current(ctx context.Context, subject, tenantID string) ([]string, error)
}

// administrationEntitlements reads `GET /api/v1/entitlements/current` with a
// short Techstack service token on behalf of the person: caller `techstack`,
// scope `entitlements:current`, the subject and (when it has an accepted form)
// the organization, and no roles (service verifiers refuse a token that asserts
// them).
type administrationEntitlements struct {
	url    string
	client *servicecall.Client
}

// newAdministrationEntitlements builds the reader from the process service
// identity (SERVICE_AUTH_SECRET or the Ed25519 key) and
// TECHSTACK_ADMINISTRATION_URL (default the production Administration). It
// returns nil when the process cannot sign, so no grant is ever derived without
// the projection.
func newAdministrationEntitlements() cloudEntitlementReader {
	secret := os.Getenv("SERVICE_AUTH_SECRET")
	if !serviceauth.CanSign(secret) {
		return nil
	}
	base := strings.TrimRight(strings.TrimSpace(os.Getenv("TECHSTACK_ADMINISTRATION_URL")), "/")
	if base == "" {
		base = defaultAdministrationURL
	}
	if !strings.HasPrefix(base, "https://") && !strings.HasPrefix(base, "http://127.0.0.1:") && !strings.HasPrefix(base, "http://localhost:") {
		return nil
	}
	cfg, err := serviceauth.Apply(servicecall.Config{
		ServiceName: "techstack",
		Target:      "administration",
		Scope:       administrationCurrentScope,
		Secret:      secret,
		TokenTTL:    30 * time.Second,
	})
	if err != nil {
		return nil
	}
	client, err := servicecall.NewClient(cfg)
	if err != nil {
		return nil
	}
	client.WithHTTPClient(&http.Client{Timeout: administrationReadTimeout})
	return &administrationEntitlements{url: base + administrationCurrentPath, client: client}
}

func (a *administrationEntitlements) Current(ctx context.Context, subject, tenantID string) ([]string, error) {
	subject = strings.TrimSpace(subject)
	if subject == "" {
		return nil, errors.New("subject required")
	}
	obo := &servicecall.OnBehalfOf{Sub: subject}
	if tenantID == "usr:"+subject || administrationOrganization.MatchString(tenantID) {
		obo.OrgID = tenantID
	}
	ctx, cancel := context.WithTimeout(ctx, administrationReadTimeout)
	defer cancel()
	response, err := a.client.Get(ctx, a.url, obo)
	if err != nil {
		return nil, fmt.Errorf("administration current entitlements: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("administration current entitlements: status %d", response.StatusCode)
	}
	var body struct {
		Schema       string   `json:"schema"`
		Subject      string   `json:"subject"`
		Entitlements []string `json:"entitlements"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 160<<10)).Decode(&body); err != nil {
		return nil, fmt.Errorf("administration current entitlements: %w", err)
	}
	if body.Schema != administrationCurrentSchema || body.Subject != subject || len(body.Entitlements) > administrationMaxKeys {
		return nil, errors.New("administration current entitlements: projection invalid")
	}
	for _, key := range body.Entitlements {
		if !administrationEntitlementKey.MatchString(key) {
			return nil, errors.New("administration current entitlements: projection invalid")
		}
	}
	return body.Entitlements, nil
}
