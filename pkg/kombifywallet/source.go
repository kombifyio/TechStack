// Package kombifywallet is Techstack's adapter to the central kombify Wallet
// (workspace Wallet plan, slice P3.3). In a hosted deployment that selects
// TECHSTACK_WALLET_SOURCE=kombify, Techstack reads and writes every credential
// through the Wallet's product-service API instead of its legacy built-in
// wallet (wallet_items, encrypted with TECHSTACK_ENCRYPTION_KEY).
//
// The package wraps kombify-go-common/walletservice. It adds what Techstack
// needs on top of the shared client: the boot switch, the node-access profile
// convention shared with Cloud and the Techstack UI, idempotent item writes, a
// create-right preflight and the mapping of Wallet failures to structured API
// denials. It never reads the environment outside Boot, never logs a value and
// never falls back to the legacy store: a Wallet that cannot answer fails the
// operation closed.
package kombifywallet

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/walletservice"

	"github.com/kombifyio/techstack/pkg/config"
)

// Source says where Techstack keeps the credentials of its users.
type Source string

const (
	// SourceLegacy is the built-in wallet (wallet_items). It is the default and
	// the only source of Windows Local and self-hosted installations.
	SourceLegacy Source = "legacy"
	// SourceKombify is the central kombify Wallet. It is valid only in SaaS.
	SourceKombify Source = "kombify"
)

// Environment names. The service secret is the Techstack-only Gateway secret
// that already signs Gateway Connect calls; the Wallet routes accept no other.
const (
	EnvSource               = "TECHSTACK_WALLET_SOURCE"
	EnvServiceURL           = "TECHSTACK_WALLET_SERVICE_URL"
	EnvRecipientKey         = "TECHSTACK_WALLET_RECIPIENT_KEY"
	EnvRecipientGeneration  = "TECHSTACK_WALLET_RECIPIENT_GENERATION"
	EnvCaptureKeyThumbprint = "TECHSTACK_WALLET_CAPTURE_KEY_THUMBPRINT"
	EnvServiceSecret        = "TECHSTACK_GATEWAY_CONNECT_SERVICE_SECRET"
	envSharedServiceSecret  = "SERVICE_AUTH_SECRET"

	// ServiceID is Techstack's service id at the Wallet.
	ServiceID = "techstack"
	// DefaultServiceURL is the Gateway origin that serves /v1/wallet/service/*.
	DefaultServiceURL = "https://api.kombify.io"
)

// ParseSource reads TECHSTACK_WALLET_SOURCE. An empty value is the legacy
// default; anything else must name a source.
func ParseSource(raw string) (Source, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(SourceLegacy):
		return SourceLegacy, nil
	case string(SourceKombify):
		return SourceKombify, nil
	default:
		return "", fmt.Errorf("%s must be %q or %q", EnvSource, SourceLegacy, SourceKombify)
	}
}

// IsKombify reports whether the source is the central kombify Wallet.
func (s Source) IsKombify() bool { return s == SourceKombify }

// Normalized returns the source, or the legacy default for an unset value, so
// an API response never reports an empty source.
func (s Source) Normalized() Source {
	if s == SourceKombify {
		return SourceKombify
	}
	return SourceLegacy
}

// Boot resolves the Wallet source for a deployment and builds the Wallet
// client when the source is kombify. The client is nil for the legacy source,
// which is how every caller tells the two modes apart.
//
// A kombify source fails the boot, with a message that names the setting and
// never a value, when the deployment is not SaaS or when the Wallet client
// configuration is incomplete. Nothing falls back to the legacy store.
func Boot(mode config.DeploymentMode, getenv func(string) string) (*Client, Source, error) {
	if getenv == nil {
		return nil, "", errors.New("kombify wallet boot requires an environment reader")
	}
	source, err := ParseSource(getenv(EnvSource))
	if err != nil {
		return nil, "", err
	}
	if !source.IsKombify() {
		return nil, SourceLegacy, nil
	}
	if !mode.IsSaaS() {
		return nil, "", fmt.Errorf("%s=%s is valid only in SaaS deployments; self-hosted and Windows Local installations keep the legacy wallet", EnvSource, SourceKombify)
	}
	secret := strings.TrimSpace(getenv(EnvServiceSecret))
	switch {
	case secret == "":
		return nil, "", fmt.Errorf("%s=%s requires %s (Techstack's own Gateway service secret)", EnvSource, SourceKombify, EnvServiceSecret)
	case secret == strings.TrimSpace(getenv(envSharedServiceSecret)):
		return nil, "", fmt.Errorf("%s must differ from %s; the Wallet routes never accept the shared secret", EnvServiceSecret, envSharedServiceSecret)
	}
	origin := strings.TrimSpace(getenv(EnvServiceURL))
	if origin == "" {
		origin = DefaultServiceURL
	}
	wallet := walletservice.New(walletservice.Config{
		Origin:               origin,
		ServiceID:            ServiceID,
		ServiceSecret:        secret,
		RecipientKey:         getenv(EnvRecipientKey),
		RecipientGeneration:  getenv(EnvRecipientGeneration),
		CaptureKeyThumbprint: getenv(EnvCaptureKeyThumbprint),
	})
	if !wallet.Enabled() {
		return nil, "", fmt.Errorf("%s=%s is not usable: wallet client %s (check %s, %s, %s and %s)",
			EnvSource, SourceKombify, wallet.DisabledReason(), EnvServiceURL, EnvServiceSecret, EnvRecipientKey, EnvRecipientGeneration)
	}
	return New(wallet), SourceKombify, nil
}
