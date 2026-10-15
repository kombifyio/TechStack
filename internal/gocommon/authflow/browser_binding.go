package authflow

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

const (
	stateRole             = "authflow/state/v1"
	browserRole           = "authflow/browser/v1"
	nonceRole             = "authflow/oidc-nonce/v1"
	maxLoginPayloadBytes  = 2048
	maxSealedLoginBytes   = 4096
	maxBrowserCookieBytes = 1024
	maxLoginFieldBytes    = 1024
)

// The cookie holds the PKCE verifier and browser entropy. URL-carried state
// contains only their browser binding, not the cookie or its secrets.
type browserBinding struct {
	Nonce        string `json:"nonce"`
	PKCEVerifier string `json:"pkv"`
	IssuedAt     int64  `json:"iat"`
	ExpiresAt    int64  `json:"exp"`
}

func (s *Service) newBrowserBinding(verifier string) (*browserBinding, error) {
	nonce := make([]byte, sha256.Size)
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	now := s.now().UTC()
	return &browserBinding{Nonce: base64.RawURLEncoding.EncodeToString(nonce), PKCEVerifier: verifier,
		IssuedAt: now.Unix(), ExpiresAt: now.Add(defaultStateLifetime).Unix()}, nil
}

func bindingDigest(role, nonce string) string {
	digest := sha256.Sum256([]byte(role + "\x00" + nonce))
	return base64.RawURLEncoding.EncodeToString(digest[:])
}

func (b *browserBinding) stateHash() string { return bindingDigest(browserRole, b.Nonce) }
func (b *browserBinding) oidcNonce() string { return bindingDigest(nonceRole, b.Nonce) }

func (b *browserBinding) matchesOIDCNonce(value any) bool {
	nonce, ok := value.(string)
	return ok && constantTimeBinding(nonce, b.oidcNonce())
}

func constantTimeBinding(actual, expected string) bool {
	a, e := decodeBrowserDigest(actual), decodeBrowserDigest(expected)
	return len(a) == sha256.Size && len(e) == sha256.Size && subtle.ConstantTimeCompare(a, e) == 1
}

func decodeBrowserDigest(value string) []byte {
	if len(value) != base64.RawURLEncoding.EncodedLen(sha256.Size) {
		return nil
	}
	digest, err := base64.RawURLEncoding.Strict().DecodeString(value)
	if err != nil || len(digest) != sha256.Size {
		return nil
	}
	return digest
}

func validStateFields(state *loginState) bool {
	if state.ProviderID == "" || len(decodeBrowserDigest(state.BrowserBinding)) != sha256.Size {
		return false
	}
	for _, field := range []string{state.ProviderID, state.TenantID, state.ReturnTo} {
		if len(field) > maxLoginFieldBytes {
			return false
		}
	}
	return true
}

func (s *Service) validLoginLifetime(issuedAt, expiresAt int64) bool {
	return issuedAt > 0 && expiresAt > issuedAt && expiresAt-issuedAt <= int64(defaultStateLifetime/time.Second) && expiresAt > s.now().UTC().Unix()
}

func validateBrowserCookiePath(path string) error {
	cookie := &http.Cookie{Name: "kombify_oidc", Path: path, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode}
	if !strings.HasPrefix(path, "/") || strings.ContainsAny(path, "?#") || cookie.Valid() != nil {
		return fmt.Errorf("%w: callback path must be a valid absolute cookie path", ErrInvalidConfig)
	}
	return nil
}

func (s *Service) browserCookieName(binding string) string {
	namespace := sha256.Sum256([]byte(s.cfg.SessionCookieName))
	prefix := "kombify_oidc_"
	if s.cfg.SessionCookieSecure {
		// The browser refuses Domain-cookie injection from sibling subdomains.
		prefix = "__Host-" + prefix
	}
	return prefix + base64.RawURLEncoding.EncodeToString(namespace[:9]) + "_" + binding
}

func (s *Service) setBrowserCookie(w http.ResponseWriter, binding *browserBinding) error {
	payload, err := json.Marshal(binding)
	if err != nil {
		return err
	}
	value, err := s.sealLoginValue(browserRole, payload)
	if err != nil || len(value) > maxBrowserCookieBytes {
		return ErrInvalidState
	}
	// #nosec G124 -- Secure follows the existing explicit SessionCookieSecure
	// production/local-HTTP policy; HttpOnly and SameSite=Lax are always set.
	cookie := s.browserCookie(binding.stateHash())
	cookie.Value = value
	cookie.MaxAge = int(defaultStateLifetime / time.Second)
	cookie.Expires = time.Unix(binding.ExpiresAt, 0).UTC()
	if err := cookie.Valid(); err != nil {
		return ErrInvalidState
	}
	http.SetCookie(w, cookie)
	return nil
}

func (s *Service) browserCookie(binding string) *http.Cookie {
	path := s.cfg.CallbackPath
	if s.cfg.SessionCookieSecure {
		// __Host- requires Path=/; callback isolation is also sealed into AAD.
		path = "/"
	}
	// #nosec G124 -- The existing SessionCookieSecure policy supports local HTTP;
	// production callers enable it, matching authsession cookie policy.
	return &http.Cookie{Name: s.browserCookieName(binding), Path: path,
		HttpOnly: true, Secure: s.cfg.SessionCookieSecure, SameSite: http.SameSiteLaxMode}
}

func (s *Service) clearBrowserCookie(w http.ResponseWriter, binding string) {
	// #nosec G124 -- Deletion mirrors the explicit production/local-HTTP Secure
	// policy and scope of the cookie being consumed, as in authsession.
	cookie := s.browserCookie(binding)
	cookie.MaxAge = -1
	cookie.Expires = time.Unix(1, 0).UTC()
	http.SetCookie(w, cookie)
}

func (s *Service) consumeBrowserBinding(w http.ResponseWriter, r *http.Request) (*loginState, *browserBinding, error) {
	state, err := s.parseState(strings.TrimSpace(r.URL.Query().Get("state")))
	if state != nil {
		// Queue deletion before any response headers, including failure redirects.
		// Only authenticated state can select a cookie; other tabs are untouched.
		s.clearBrowserCookie(w, state.BrowserBinding)
	}
	if err != nil {
		return nil, nil, err
	}
	binding, err := s.readBrowserCookie(r, state)
	return state, binding, err
}

func (s *Service) readBrowserCookie(r *http.Request, state *loginState) (*browserBinding, error) {
	cookie, err := r.Cookie(s.browserCookieName(state.BrowserBinding))
	if err != nil || len(cookie.Value) > maxBrowserCookieBytes {
		return nil, ErrInvalidState
	}
	payload, err := s.openLoginValue(browserRole, cookie.Value)
	if err != nil {
		return nil, ErrInvalidState
	}
	binding := &browserBinding{}
	if err := json.Unmarshal(payload, binding); err != nil {
		return nil, ErrInvalidState
	}
	if !validBrowserSecrets(binding) || !constantTimeBinding(binding.stateHash(), state.BrowserBinding) ||
		binding.IssuedAt != state.IssuedAt || binding.ExpiresAt != state.ExpiresAt || !s.validLoginLifetime(binding.IssuedAt, binding.ExpiresAt) {
		return nil, ErrInvalidState
	}
	return binding, nil
}

func validBrowserSecrets(binding *browserBinding) bool {
	if len(decodeBrowserDigest(binding.Nonce)) != sha256.Size || len(binding.PKCEVerifier) < 43 || len(binding.PKCEVerifier) > 128 {
		return false
	}
	for _, character := range binding.PKCEVerifier {
		alphaNumeric := character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9'
		if !alphaNumeric && !strings.ContainsRune("-._~", character) {
			return false
		}
	}
	return true
}

func (s *Service) loginCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(s.cfg.StateSecret)
	block, err := aes.NewCipher(key[:])
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

func (s *Service) loginAAD(role string) []byte {
	return []byte(role + "\x00" + s.cfg.SessionCookieName + "\x00" + s.cfg.CallbackPath)
}

func (s *Service) sealLoginValue(role string, plaintext []byte) (string, error) {
	if len(plaintext) > maxLoginPayloadBytes {
		return "", ErrInvalidState
	}
	aead, err := s.loginCipher()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := aead.Seal(nil, nonce, plaintext, s.loginAAD(role))
	return base64.RawURLEncoding.EncodeToString(append(nonce, sealed...)), nil
}

func (s *Service) openLoginValue(role, raw string) ([]byte, error) {
	if len(raw) > maxSealedLoginBytes {
		return nil, ErrInvalidState
	}
	aead, err := s.loginCipher()
	if err != nil {
		return nil, ErrInvalidState
	}
	sealed, err := base64.RawURLEncoding.Strict().DecodeString(raw)
	if err != nil || len(sealed) < aead.NonceSize()+aead.Overhead() {
		return nil, ErrInvalidState
	}
	plaintext, err := aead.Open(nil, sealed[:aead.NonceSize()], sealed[aead.NonceSize():], s.loginAAD(role))
	if err != nil || len(plaintext) > maxLoginPayloadBytes {
		return nil, ErrInvalidState
	}
	return plaintext, nil
}
