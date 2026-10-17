// Package serviceauth applies the per-caller Ed25519 service identity
// (SERVICE-TO-SERVICE-AUTH-STANDARD) to every servicecall.Config Techstack
// builds, so one environment decides how the whole process signs and verifies.
//
// The keys come from SERVICE_AUTH_ED25519_PRIVATE_KEY (this process as a
// caller), SERVICE_AUTH_ED25519_PUBLIC_KEYS (this process as a receiver) and
// SERVICE_AUTH_REQUIRE_ED25519. Absent variables change nothing: the shared
// HS256 secret keeps working until a receiver sets the require flag. A
// malformed value never degrades to HS256; it makes Check, Apply and therefore
// every port that uses them fail with the parse error.
package serviceauth

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"strings"
	"sync"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
)

type loaded struct {
	signingKey   ed25519.PrivateKey
	signingKeyID string
	callerKeys   map[string]servicecall.CallerPublicKey
	require      bool
	err          error
}

var (
	mu    sync.Mutex
	state *loaded
)

// load parses the environment once per process and remembers the outcome,
// including a failure, so a bad key stays bad for every caller.
func load() *loaded {
	mu.Lock()
	defer mu.Unlock()
	if state == nil {
		var probe servicecall.Config
		err := servicecall.LoadEd25519FromEnv(&probe)
		state = &loaded{
			signingKey:   probe.SigningKey,
			signingKeyID: probe.SigningKeyID,
			callerKeys:   probe.CallerPublicKeys,
			require:      probe.RequireEd25519,
			err:          err,
		}
	}
	return state
}

// Check reports a malformed Ed25519 environment. Startup calls it so a typo
// stops the process instead of silently leaving the shared secret in charge.
func Check() error { return load().err }

// Apply returns cfg with the process Ed25519 identity applied. Secret,
// SecretNext, AllowedCallers and every other field stay as the caller set them.
func Apply(cfg servicecall.Config) (servicecall.Config, error) {
	s := load()
	if s.err != nil {
		return cfg, s.err
	}
	if cfg.SigningKey == nil {
		cfg.SigningKey, cfg.SigningKeyID = s.signingKey, s.signingKeyID
	}
	if cfg.CallerPublicKeys == nil {
		cfg.CallerPublicKeys = s.callerKeys
	}
	cfg.RequireEd25519 = cfg.RequireEd25519 || s.require
	return cfg, nil
}

// CanSign reports whether this process can sign service tokens: the shared
// secret or its own Ed25519 key is enough, neither fails closed.
func CanSign(secret string) bool {
	return strings.TrimSpace(secret) != "" || load().signingKey != nil
}

// SignEdDSA signs a JWT payload under the process Ed25519 key, the way
// servicecall signs its own claims. ok is false when no key is configured, so
// the caller keeps its HS256 path.
func SignEdDSA(payload []byte) (token string, ok bool, err error) {
	s := load()
	if s.err != nil {
		return "", false, s.err
	}
	if s.signingKey == nil {
		return "", false, nil
	}
	header, err := json.Marshal(map[string]string{"alg": "EdDSA", "kid": s.signingKeyID, "typ": "JWT"})
	if err != nil {
		return "", false, err
	}
	enc := base64.RawURLEncoding
	signingInput := enc.EncodeToString(header) + "." + enc.EncodeToString(payload)
	return signingInput + "." + enc.EncodeToString(ed25519.Sign(s.signingKey, []byte(signingInput))), true, nil
}

// CanVerify reports whether this process can verify service tokens: the shared
// secret or an Ed25519 public set is enough, neither fails closed.
func CanVerify(secret string) bool {
	return strings.TrimSpace(secret) != "" || len(load().callerKeys) > 0
}

// reset forgets the parsed environment so a test can change it.
func reset() {
	mu.Lock()
	state = nil
	mu.Unlock()
}
