package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/authlocal"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
)

const walletDeviceChallengeLifetime = 2 * time.Minute

type walletDeviceAuthority struct {
	deps    routeDeps
	store   authlocal.Store
	now     func() time.Time
	mu      sync.Mutex
	pending map[string]routes.WalletDeviceChallenge
}

func newWalletLocalDeviceReauth(deps routeDeps) *routes.WalletLocalDeviceReauth {
	if !localDeviceSessionAvailable(deps, strings.TrimSpace(os.Getenv(localDeviceTokenEnv))) || deps.v2.browserSessions == nil {
		return nil
	}
	a := &walletDeviceAuthority{deps: deps, store: optionalLocalOwnerStore(deps), now: time.Now,
		pending: make(map[string]routes.WalletDeviceChallenge)}
	return &routes.WalletLocalDeviceReauth{Challenge: a.challenge, Verify: a.verify}
}

// Length framing avoids ambiguous transcripts even for Unicode display names.
func walletDeviceMessage(parts ...string) string {
	var b strings.Builder
	b.WriteString("techstack-wallet-device/v1")
	for _, part := range parts {
		b.WriteString(strconv.Itoa(len(part)))
		b.WriteByte(':')
		b.WriteString(part)
	}
	return b.String()
}

func walletDeviceTranscript(c routes.WalletDeviceChallenge, action string, extra ...string) string {
	parts := []string{action, c.Origin, c.OwnerID, c.TenantID, c.Provider, c.ItemID, c.Snapshot,
		c.ReasonDigest, c.SessionDigest, c.DisplayName, c.ClientNonce, c.ServerNonce, c.ExpiresAt}
	return walletDeviceMessage(append(parts, extra...)...)
}

func (a *walletDeviceAuthority) current(e *httpx.Event, binding routes.WalletDeviceBinding) (string, string, string, string, error) {
	secret := strings.TrimSpace(os.Getenv(localDeviceTokenEnv))
	origin, available := localDeviceProofOrigin(e.Request, a.deps, secret)
	if !available || a.store == nil {
		return "", "", "", "", errors.New("local device authority unavailable")
	}
	transfer, err := a.deps.v2.browserSessions.VerifyTransfer(e.Request)
	if err != nil {
		return "", "", "", "", err
	}
	claims := transfer.Claims
	effective := identity.FromContext(e.Request.Context())
	if claims.Subject != binding.OwnerID || claims.TenantID != binding.TenantID || effective == nil ||
		effective.UserID != claims.Subject || effective.OrgID != claims.TenantID || claims.TenantID != localDeviceSessionTenant(a.deps) {
		return "", "", "", "", errors.New("device confirmation owner mismatch")
	}
	owner, err := a.store.Get(e.Request.Context())
	if err != nil || owner == nil || owner.PasswordHash == "" {
		return "", "", "", "", errors.New("canonical local owner unavailable")
	}
	if owner.Claimed {
		if claims.Subject != "breakglass:"+authlocal.BreakGlassRecordID || claims.Provider != authlocal.DefaultProviderID ||
			claims.Email != strings.TrimSpace(owner.Email) {
			return "", "", "", "", errors.New("canonical local owner changed")
		}
	} else {
		deviceID := sha256.Sum256([]byte(secret))
		if claims.Subject != fmt.Sprintf("device:%x", deviceID) || claims.Provider != localDeviceProvider || claims.Email != localDeviceEmail {
			return "", "", "", "", errors.New("canonical device owner mismatch")
		}
	}
	names := []string{a.deps.v2.cookieName, a.deps.v2.cookieName + "_origin"}
	values := make([]string, len(names))
	for i, name := range names {
		found := false
		for _, cookie := range e.Request.Cookies() {
			if cookie.Name != name {
				continue
			}
			if found || cookie.Value == "" {
				return "", "", "", "", errors.New("ambiguous device session cookies")
			}
			found = true
			values[i] = name + "=" + cookie.Value
		}
		if !found {
			return "", "", "", "", errors.New("device session pair missing")
		}
	}
	digest := sha256.Sum256([]byte(strings.Join(values, "\n")))
	return secret, origin, claims.Provider, hex.EncodeToString(digest[:]), nil
}

func (a *walletDeviceAuthority) challenge(e *httpx.Event, binding routes.WalletDeviceBinding, nonce string) (*routes.WalletDeviceChallenge, error) {
	if !localDeviceValidNonce(nonce) {
		return nil, errors.New("invalid device nonce")
	}
	secret, origin, provider, sessionDigest, err := a.current(e, binding)
	if err != nil {
		return nil, err
	}
	var serverNonce [16]byte
	if _, err := rand.Read(serverNonce[:]); err != nil {
		return nil, err
	}
	c := routes.WalletDeviceChallenge{Origin: origin, OwnerID: binding.OwnerID, TenantID: binding.TenantID,
		Provider: provider, ItemID: binding.ItemID, Snapshot: binding.Snapshot, ReasonDigest: binding.ReasonDigest,
		SessionDigest: sessionDigest, DisplayName: binding.DisplayName, ClientNonce: nonce,
		ServerNonce: hex.EncodeToString(serverNonce[:]), ExpiresAt: a.now().Add(walletDeviceChallengeLifetime).UTC().Format(time.RFC3339Nano)}
	c.Proof = localDeviceMAC(secret, walletDeviceTranscript(c, "challenge"))
	a.mu.Lock()
	defer a.mu.Unlock()
	for nonce, pending := range a.pending {
		deadline, _ := time.Parse(time.RFC3339Nano, pending.ExpiresAt)
		if !a.now().Before(deadline) {
			delete(a.pending, nonce)
		}
	}
	if len(a.pending) >= 128 {
		return nil, errors.New("too many pending device confirmations")
	}
	a.pending[c.ServerNonce] = c
	return &c, nil
}

func (a *walletDeviceAuthority) verify(e *httpx.Event, binding routes.WalletDeviceBinding, proof routes.WalletDeviceProof) (func(string) string, error) {
	a.mu.Lock()
	c, found := a.pending[proof.ServerNonce]
	delete(a.pending, proof.ServerNonce)
	a.mu.Unlock()
	if !found {
		return nil, errors.New("device confirmation unavailable or consumed")
	}
	secret, origin, provider, sessionDigest, err := a.current(e, binding)
	if err != nil {
		return nil, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, c.ExpiresAt)
	if err != nil || !a.now().Before(deadline) || c.Origin != origin || c.OwnerID != binding.OwnerID || c.TenantID != binding.TenantID ||
		c.Provider != provider || c.SessionDigest != sessionDigest || c.ItemID != binding.ItemID || c.Snapshot != binding.Snapshot ||
		c.ReasonDigest != binding.ReasonDigest || c.DisplayName != binding.DisplayName || c.ClientNonce != proof.ClientNonce ||
		!localDeviceMACMatches(proof.Proof, localDeviceMAC(secret, walletDeviceTranscript(c, "reveal", http.MethodPost,
			"/api/v1/wallet/"+c.ItemID+"/reveal"))) {
		return nil, errors.New("device confirmation changed or expired")
	}
	return func(payload string) string {
		return localDeviceMAC(secret, walletDeviceTranscript(c, "result", "200", payload))
	}, nil
}
