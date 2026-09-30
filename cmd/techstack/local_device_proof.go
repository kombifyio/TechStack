package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	commonauthlocal "github.com/kombifyio/techstack/internal/gocommon/authlocal"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	localDeviceChallengePath = "/api/v1/auth/device-session-proof/challenge"
	localDeviceProofPath     = "/api/v1/auth/device-session-proof"
	localDeviceProofHeader   = "X-TechStack-Device-Proof"
	localDeviceChallengeTTL  = 30 * time.Second
	localDeviceChallengeMax  = 64
)

type localDeviceChallenge struct {
	origin, clientNonce, serverNonce string
	expires                          time.Time
}

var localDeviceChallenges = struct {
	sync.Mutex
	entries map[string]localDeviceChallenge
}{entries: make(map[string]localDeviceChallenge)}

func registerLocalDeviceProofRoutes(router *httpx.Router, deps routeDeps, store commonauthlocal.Store) {
	router.POST(localDeviceChallengePath, func(e *httpx.Event) error {
		secret := strings.TrimSpace(os.Getenv(localDeviceTokenEnv))
		origin, ok := localDeviceProofOrigin(e.Request, deps, secret)
		if !ok {
			return httpx.NotFound(e, "Local device proof is not available")
		}
		var body struct {
			Origin      string `json:"origin"`
			ClientNonce string `json:"client_nonce"`
		}
		if !readLocalDeviceProofBody(e.Request, &body) || body.Origin != origin || !localDeviceValidNonce(body.ClientNonce) {
			return httpx.Error(e, http.StatusBadRequest, ksapi.ErrCodeValidation, "Invalid device proof challenge", nil)
		}
		nonce := make([]byte, 16)
		if _, err := rand.Read(nonce); err != nil {
			return httpx.InternalError(e, "Local device challenge could not be issued")
		}
		challenge := localDeviceChallenge{origin: origin, clientNonce: body.ClientNonce,
			serverNonce: hex.EncodeToString(nonce), expires: time.Now().Add(localDeviceChallengeTTL)}
		localDeviceChallenges.Lock()
		for key, candidate := range localDeviceChallenges.entries {
			if time.Now().After(candidate.expires) {
				delete(localDeviceChallenges.entries, key)
			}
		}
		if len(localDeviceChallenges.entries) >= localDeviceChallengeMax {
			localDeviceChallenges.Unlock()
			return httpx.Error(e, http.StatusTooManyRequests, ksapi.ErrCodeRateLimited, "Too many device challenges", nil)
		}
		localDeviceChallenges.entries[challenge.serverNonce] = challenge
		localDeviceChallenges.Unlock()
		return httpx.Success(e, http.StatusOK, map[string]string{
			"server_nonce": challenge.serverNonce,
			"proof":        localDeviceMAC(secret, localDeviceProofMessage("server", origin, body.ClientNonce, challenge.serverNonce)),
		})
	})

	router.POST(localDeviceProofPath, func(e *httpx.Event) error {
		secret := strings.TrimSpace(os.Getenv(localDeviceTokenEnv))
		origin, ok := localDeviceProofOrigin(e.Request, deps, secret)
		if !ok {
			return httpx.NotFound(e, "Local device proof is not available")
		}
		if !localDeviceSessionAttempts.allow() {
			return httpx.Error(e, http.StatusTooManyRequests, ksapi.ErrCodeRateLimited, "Too many failed device proof attempts", nil)
		}
		var body struct {
			Origin      string `json:"origin"`
			ClientNonce string `json:"client_nonce"`
			ServerNonce string `json:"server_nonce"`
		}
		if !readLocalDeviceProofBody(e.Request, &body) || body.Origin != origin ||
			!localDeviceValidNonce(body.ClientNonce) || !localDeviceValidNonce(body.ServerNonce) {
			localDeviceSessionAttempts.fail()
			return httpx.Unauthorized(e, "Invalid local device proof")
		}
		localDeviceChallenges.Lock()
		challenge, found := localDeviceChallenges.entries[body.ServerNonce]
		delete(localDeviceChallenges.entries, body.ServerNonce) // consume even on a bad proof
		localDeviceChallenges.Unlock()
		expected := localDeviceMAC(secret, localDeviceProofMessage("client", origin,
			body.ClientNonce, body.ServerNonce, http.MethodPost, localDeviceProofPath))
		provided := strings.TrimSpace(e.Request.Header.Get(localDeviceProofHeader))
		if !found || time.Now().After(challenge.expires) || challenge.origin != origin ||
			challenge.clientNonce != body.ClientNonce || !localDeviceMACMatches(provided, expected) {
			localDeviceSessionAttempts.fail()
			return httpx.Unauthorized(e, "Invalid local device proof")
		}
		localDeviceSessionAttempts.reset()
		return issueLocalDeviceSession(e, deps, store, secret, &challenge)
	})
}

func localDeviceProofOrigin(r *http.Request, deps routeDeps, secret string) (string, bool) {
	if !localDeviceSessionAvailable(deps, secret) || deps.v2.browserSessions == nil || !localDeviceSessionLoopback(r) {
		return "", false
	}
	origin := config.PublicOriginFromEnv()
	u, err := url.Parse(origin)
	if err != nil || u.Scheme != "http" || u.Host == "" || r.Host != u.Host {
		return "", false
	}
	ip := net.ParseIP(u.Hostname())
	if !(strings.EqualFold(u.Hostname(), "localhost") || ip != nil && ip.IsLoopback()) {
		return "", false
	}
	return origin, true
}

func readLocalDeviceProofBody(r *http.Request, target any) bool {
	if r.Body == nil {
		return false
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 1025))
	return err == nil && len(data) <= 1024 && json.Unmarshal(data, target) == nil
}

func localDeviceValidNonce(value string) bool {
	if len(value) != 32 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil && value == strings.ToLower(value)
}

func localDeviceProofMessage(parts ...string) string {
	return "techstack-local-device/v1\n" + strings.Join(parts, "\n")
}

func localDeviceCanonicalCookies(cookies []string) string {
	ordered := append([]string(nil), cookies...)
	sort.Strings(ordered)
	return strings.Join(ordered, "\n")
}

func localDeviceMAC(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func localDeviceMACMatches(provided, expected string) bool {
	if len(provided) != len(expected) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) == 1
}
