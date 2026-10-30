package executioncustody

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	wire "github.com/kombifyio/techstack/internal/selfhostcontracts/advancedcapability"
	"github.com/google/uuid"
)

const (
	EnvRemoteURL              = "TECHSTACK_ADVANCED_ISSUER_URL"
	EnvRemoteCallerPrivateKey = "TECHSTACK_ADVANCED_ISSUER_CALLER_PRIVATE_KEY"
	EnvRemoteCallerKeyID      = "TECHSTACK_ADVANCED_ISSUER_CALLER_KEY_ID"
	EnvRemoteCallerID         = "TECHSTACK_ADVANCED_ISSUER_CALLER_ID"
	EnvRemoteExpectedKeyID    = "TECHSTACK_ADVANCED_ISSUER_EXPECTED_KEY_ID"
	RemoteAudience            = "techstack-execution"
	remoteScope               = "execution.invoke"
	remotePath                = "/internal/execution/v1/"
	remoteBodyLimit           = 64 << 10
)

type Metadata struct {
	IssuerID    string
	KeyID       string
	Bundle      []byte
	CallerKeyID string
	Grant       CallerGrant
}

// Remote satisfies the existing AdvancedIssuer port without access to any
// installation key, wrapping secret, or signing-custody database credential.
type Remote struct {
	endpoint string
	auth     servicecall.Config
	http     *http.Client
	metadata Metadata
}

// CheckRemoteCustody is shared by startup and the deployment verifier. A
// process using delegated issuance cannot retain the credentials it replaces.
func CheckRemoteCustody() error {
	var bundle map[string]string
	if raw := ""; raw != "" && json.Unmarshal([]byte(raw), &bundle) != nil {
		return errors.New("remote Execution requires a valid scoped company bundle")
	}
	for _, name := range []string{EnvIssuerKey, EnvKeyCustodyDatabaseURL, "TECHSTACK_ENCRYPTION_KEY", "TECHSTACK_PROVIDER_CONTROL_RUNTIME_DATABASE_URL", "TECHSTACK_IONOS_PROVIDER_BUNDLE", "TECHSTACK_CENTRON_PROVIDER_BUNDLE", "TECHSTACK_MIGRATION_DATABASE_URL", "TECHSTACK_MANAGED_TEST_ADMIN_DATABASE_URL"} {
		if os.Getenv(name) != "" || bundle[name] != "" {
			return fmt.Errorf("remote advanced issuer forbids installation custody in caller: %s", name)
		}
	}
	return nil
}

func OpenRemote(ctx context.Context) (*Remote, error) {
	if err := CheckRemoteCustody(); err != nil {
		return nil, err
	}
	endpoint, err := url.Parse(strings.TrimSpace(os.Getenv(EnvRemoteURL)))
	if err != nil || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || (endpoint.Path != "" && endpoint.Path != "/") {
		return nil, errors.New("advanced issuer requires an origin URL")
	}
	ip := net.ParseIP(endpoint.Hostname())
	if endpoint.Scheme != "https" && !(endpoint.Scheme == "http" && (endpoint.Hostname() == "localhost" || ip != nil && ip.IsLoopback())) {
		return nil, errors.New("advanced issuer requires HTTPS or native loopback transport")
	}
	key, err := callerKeyFromSeed(os.Getenv(EnvRemoteCallerPrivateKey))
	if err != nil {
		return nil, errors.New("advanced issuer requires its dedicated caller seed")
	}
	caller, kid := strings.TrimSpace(os.Getenv(EnvRemoteCallerID)), strings.TrimSpace(os.Getenv(EnvRemoteCallerKeyID))
	expected := strings.TrimSpace(os.Getenv(EnvRemoteExpectedKeyID))
	if !stackIDPattern.MatchString(caller) || !callerKeyIDPattern.MatchString(kid) || !strings.HasPrefix(expected, "ed25519://sha256/") || len(expected) != len("ed25519://sha256/")+64 {
		return nil, errors.New("advanced issuer requires caller identity and retained installation key pin")
	}
	client := &Remote{endpoint: strings.TrimSuffix(endpoint.String(), "/"), auth: servicecall.Config{ServiceName: caller, SigningKey: key, SigningKeyID: kid, Scope: remoteScope, TokenTTL: time.Minute}, http: &http.Client{Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if err := client.call(ctx, "metadata", struct{}{}, &client.metadata); err != nil {
		return nil, err
	}
	if client.metadata.IssuerID != TrustedIssuerID || client.metadata.KeyID != expected {
		return nil, errors.New("advanced issuer identity differs from retained installation")
	}
	var bundle wire.TrustBundle
	if json.Unmarshal(client.metadata.Bundle, &bundle) != nil || bundle.Validate() != nil || len(bundle.Keys) != 1 || bundle.Keys[0].IssuerID != TrustedIssuerID || bundle.Keys[0].KeyID != expected {
		return nil, errors.New("advanced issuer public trust bundle is invalid")
	}
	public, err := decodePublicKey(bundle.Keys[0].PublicKey)
	if err != nil || wire.KeyID(public) != expected {
		return nil, errors.New("advanced issuer public key differs from retained pin")
	}
	grant := client.metadata.Grant
	ownedRecords := grant.DeploymentAdmission == "owned-records" && grant.OwnerRef == "" && len(grant.DeploymentIDs) == 0
	fixed := grant.DeploymentAdmission == "" && ownerRefPattern.MatchString(grant.OwnerRef) && len(grant.DeploymentIDs) > 0
	if grant.Caller != caller || client.metadata.CallerKeyID != kid || grant.PublicKey != base64.RawStdEncoding.EncodeToString(key.Public().(ed25519.PublicKey)) || grant.TenantID == "" || grant.OwnerID == "" || (!ownedRecords && !fixed) {
		return nil, errors.New("native Execution returned a different caller grant")
	}
	return client, nil
}

func (client *Remote) IssuerID() string { return client.metadata.IssuerID }
func (client *Remote) Metadata() Metadata {
	out := client.metadata
	out.Bundle = bytes.Clone(out.Bundle)
	out.Grant.DeploymentIDs = append([]string{}, out.Grant.DeploymentIDs...)
	return out
}
func (client *Remote) KeyID() string       { return client.metadata.KeyID }
func (client *Remote) TrustBundle() []byte { return bytes.Clone(client.metadata.Bundle) }
func (client *Remote) TrustBundleSHA256() string {
	sum := sha256.Sum256(client.metadata.Bundle)
	return "sha256:" + hex.EncodeToString(sum[:])
}
func (client *Remote) Issue(ctx context.Context, request Request) (Capability, error) {
	var result Capability
	err := client.call(ctx, "issue", request, &result)
	return result, err
}
func (client *Remote) RecordTrustBinding(ctx context.Context, binding TrustBinding) error {
	return client.call(ctx, "bind", binding, nil)
}
func (client *Remote) TrustBindingFor(ctx context.Context, tenantID, deploymentID string) (TrustBinding, error) {
	var result TrustBinding
	err := client.call(ctx, "binding", TrustBinding{TenantID: tenantID, DeploymentID: deploymentID}, &result)
	return result, err
}

func (client *Remote) call(ctx context.Context, action string, input, output any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return err
	}
	path := remotePath + action
	binding, err := servicecall.NewRequestBinding(http.MethodPost, path, raw)
	if err != nil {
		return err
	}
	id, err := uuid.NewV7()
	if err != nil {
		return err
	}
	token, err := servicecall.IssueBoundToken(client.auth, RemoteAudience, binding, id.String())
	if err != nil {
		return errors.New("advanced issuer caller authentication is unavailable")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, client.endpoint+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set(servicecall.HeaderServiceAuth, token)
	req.Header.Set("Content-Type", "application/json")
	response, err := client.http.Do(req)
	if err != nil {
		return errors.New("advanced issuer transport unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		if response.StatusCode == http.StatusNotFound {
			return ErrTrustBindingMissing
		}
		return fmt.Errorf("advanced issuer request denied (%d)", response.StatusCode)
	}
	if output == nil {
		return nil
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, remoteBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(output); err != nil {
		return errors.New("advanced issuer response invalid")
	}
	return nil
}

func callerKeyFromSeed(value string) (ed25519.PrivateKey, error) {
	seed, err := decodePublicKey(value)
	if err != nil {
		return nil, err
	}
	return ed25519.NewKeyFromSeed(seed), nil
}
