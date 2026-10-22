package edgeauth

import (
	"crypto/hmac"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/dataprofile"
)

// Signed data protection profile headers (kombify workspace
// platform/standards/DATA-ARCHITECTURE.md section 5.3, D-2026-10-21-12).
//
// The Gateway resolves the subject's profile per request and forwards it as a
// separately signed decision bound to the exact Edge identity envelope, method,
// path, audience, subject, tenant and request id. The profile key is its own
// secret (EDGE_PROFILE_SECRET[_NEXT]); it never falls back to the identity or
// flags secrets. A missing or invalid profile is an error: origins deny, they
// never assume Standard.
const (
	// HeaderProfile carries dataprofile.Profile.Encode().
	HeaderProfile = "X-Kombify-Profile"
	// HeaderProfileSignature is "v1=<base64url-hmac>" over the profile payload.
	HeaderProfileSignature = "X-Kombify-Profile-Signature"
	// HeaderProfileTimestamp repeats the Edge identity timestamp it is bound to.
	HeaderProfileTimestamp = "X-Kombify-Profile-Timestamp"
	// HeaderProfileKeyID identifies the profile secret that signed the payload.
	HeaderProfileKeyID = "X-Kombify-Profile-Key-ID"

	profileSignatureVersion = "v1"
	profileDomain           = "kombify-profile-v1"
)

// ProfileSignInput binds one profile to the already minted Edge identity
// envelope of the same upstream request.
type ProfileSignInput struct {
	Secret        string
	KeyID         string
	Method        string
	SignedPath    string
	Audience      string
	PublicPrefix  string
	SubjectID     string
	TenantID      string
	RequestID     string
	EdgeKeyID     string
	EdgeTimestamp string
	EdgeNonce     string
	EdgeSignature string
	Profile       dataprofile.Profile
}

// ProfileVerifyConfig fixes the trusted profile keys and the origin audience.
type ProfileVerifyConfig struct {
	PrimarySecret        string
	NextSecret           string
	PrimaryKeyID         string
	NextKeyID            string
	ExpectedAudience     string
	ExpectedPublicPrefix string
	SignatureWindow      time.Duration
	Now                  func() time.Time
	// IdentityConfig verifies the Edge identity envelope the profile is bound to.
	IdentityConfig Config
}

// ProfileBinding is the provenance of a verified profile.
type ProfileBinding struct {
	KeyID      string
	SubjectID  string
	TenantID   string
	Audience   string
	Method     string
	SignedPath string
	RequestID  string
	IssuedAt   time.Time
}

// BuildProfileSignaturePayload is the canonical profile payload. Its field
// order is a cross-language wire contract shared with kombify-Gateway.
func BuildProfileSignaturePayload(
	keyID, method, signedPath, audience, publicPrefix, subjectID, tenantID,
	requestID, edgeKeyID, timestamp, nonce, edgeSignature, encodedProfile string,
) string {
	return strings.Join([]string{
		profileDomain, keyID, strings.ToUpper(method), signedPath, audience, publicPrefix,
		subjectID, tenantID, requestID, edgeKeyID, timestamp, nonce, edgeSignature, encodedProfile,
	}, "\n")
}

// SignProfileHeaders signs a profile for one upstream request.
func SignProfileHeaders(input ProfileSignInput) (http.Header, error) {
	secret := strings.TrimSpace(input.Secret)
	if secret == "" {
		return nil, fmt.Errorf("edge_profile_secret_missing")
	}
	keyID := strings.TrimSpace(firstNonEmpty(input.KeyID, defaultEdgeKeyID))
	for name, value := range map[string]string{
		"method": input.Method, "signed_path": input.SignedPath, "audience": input.Audience,
		"subject": input.SubjectID, "request_id": input.RequestID, "edge_key_id": input.EdgeKeyID,
		"edge_timestamp": input.EdgeTimestamp, "edge_nonce": input.EdgeNonce, "edge_signature": input.EdgeSignature,
	} {
		if strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("edge_profile_binding_missing: %s", name)
		}
	}
	if !supportsEntitlementIdentitySignature(input.EdgeSignature) {
		return nil, fmt.Errorf("edge_profile_identity_version_invalid")
	}
	encoded, err := input.Profile.Encode()
	if err != nil {
		return nil, err
	}
	payload := BuildProfileSignaturePayload(
		keyID, input.Method, strings.TrimSpace(input.SignedPath), strings.TrimSpace(input.Audience),
		strings.TrimSpace(input.PublicPrefix), strings.TrimSpace(input.SubjectID), strings.TrimSpace(input.TenantID),
		strings.TrimSpace(input.RequestID), strings.TrimSpace(input.EdgeKeyID), strings.TrimSpace(input.EdgeTimestamp),
		strings.TrimSpace(input.EdgeNonce), strings.TrimSpace(input.EdgeSignature), encoded,
	)
	h := http.Header{}
	h.Set(HeaderProfile, encoded)
	h.Set(HeaderProfileTimestamp, strings.TrimSpace(input.EdgeTimestamp))
	h.Set(HeaderProfileKeyID, keyID)
	h.Set(HeaderProfileSignature, profileSignatureVersion+"="+signPayload(secret, payload))
	return h, nil
}

// VerifyProfileHeaders verifies the profile with keys from cfg, falling back
// only to EDGE_PROFILE_SECRET[_NEXT] and EDGE_PROFILE_KEY_ID[_NEXT].
func VerifyProfileHeaders(r *http.Request, cfg ProfileVerifyConfig) (dataprofile.Profile, ProfileBinding, error) {
	cfg.PrimarySecret = firstNonEmpty(cfg.PrimarySecret, os.Getenv("EDGE_PROFILE_SECRET"))
	cfg.NextSecret = firstNonEmpty(cfg.NextSecret, os.Getenv("EDGE_PROFILE_SECRET_NEXT"))
	cfg.PrimaryKeyID = firstNonEmpty(cfg.PrimaryKeyID, os.Getenv("EDGE_PROFILE_KEY_ID"), defaultEdgeKeyID)
	cfg.NextKeyID = firstNonEmpty(cfg.NextKeyID, os.Getenv("EDGE_PROFILE_KEY_ID_NEXT"), defaultEdgeNextKeyID)
	return VerifyProfileHeadersWithKeys(r, cfg)
}

// VerifyProfileHeadersWithKeys verifies with only the keys in cfg.
func VerifyProfileHeadersWithKeys(r *http.Request, cfg ProfileVerifyConfig) (dataprofile.Profile, ProfileBinding, error) {
	if r == nil {
		return dataprofile.Profile{}, ProfileBinding{}, fmt.Errorf("edge_profile_request_missing")
	}
	if strings.TrimSpace(cfg.ExpectedAudience) == "" {
		return dataprofile.Profile{}, ProfileBinding{}, fmt.Errorf("edge_profile_audience_missing")
	}
	keys, err := profileVerificationKeys(cfg)
	if err != nil {
		return dataprofile.Profile{}, ProfileBinding{}, err
	}
	envelope, err := readProfileEnvelope(r, cfg)
	if err != nil {
		return dataprofile.Profile{}, ProfileBinding{}, err
	}
	if !verifyProfileSignature(r.Method, envelope, keys) {
		return dataprofile.Profile{}, ProfileBinding{}, fmt.Errorf("edge_profile_signature_invalid")
	}
	keyID, timestamp, signedPath := envelope.keyID, envelope.timestamp, envelope.signedPath
	subjectID, tenantID, audience, requestID := envelope.subjectID, envelope.tenantID, envelope.audience, envelope.requestID
	profile, err := dataprofile.Decode(envelope.encoded)
	if err != nil {
		return dataprofile.Profile{}, ProfileBinding{}, err
	}
	// validateTimestampAt accepted the timestamp, so it parses.
	seconds, _ := strconv.ParseInt(timestamp, 10, 64)
	return profile, ProfileBinding{
		KeyID: keyID, SubjectID: subjectID, TenantID: tenantID, Audience: audience,
		Method: strings.ToUpper(r.Method), SignedPath: signedPath, RequestID: requestID, IssuedAt: time.Unix(seconds, 0).UTC(),
	}, nil
}

// RequireProfile verifies the profile on every request and stores it with
// dataprofile.WithProfile; a request without a valid profile gets a 403 with
// the data_profile_unresolvable denial envelope.
func RequireProfile(cfg ProfileVerifyConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			profile, _, err := VerifyProfileHeaders(r, cfg)
			if err != nil {
				envelope := dataprofile.UnresolvableDenial(r.Header.Get(HeaderRequestID))
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusForbidden)
				_ = json.NewEncoder(w).Encode(envelope)
				return
			}
			next.ServeHTTP(w, r.WithContext(dataprofile.WithProfile(r.Context(), profile)))
		})
	}
}

func profileVerificationKeys(cfg ProfileVerifyConfig) ([]flagSigningKey, error) {
	keys := []flagSigningKey{
		{strings.TrimSpace(firstNonEmpty(cfg.PrimaryKeyID, defaultEdgeKeyID)), strings.TrimSpace(cfg.PrimarySecret)},
		{strings.TrimSpace(firstNonEmpty(cfg.NextKeyID, defaultEdgeNextKeyID)), strings.TrimSpace(cfg.NextSecret)},
	}
	if keys[0].secret == "" && keys[1].secret == "" {
		return nil, fmt.Errorf("edge_profile_secret_missing")
	}
	if keys[0].secret != "" && keys[1].secret != "" && keys[0].keyID == keys[1].keyID {
		return nil, fmt.Errorf("edge_profile_rotation_key_id_conflict")
	}
	return keys, nil
}

type profileEnvelope struct {
	encoded, signature, keyID, timestamp                       string
	audience, publicPrefix, subjectID, tenantID, requestID     string
	edgeKeyID, nonce, edgeSignature, signedPath, edgeTimestamp string
}

func readProfileEnvelope(r *http.Request, cfg ProfileVerifyConfig) (profileEnvelope, error) {
	get := func(name string) string { return strings.TrimSpace(r.Header.Get(name)) }
	e := profileEnvelope{
		encoded: get(HeaderProfile), signature: get(HeaderProfileSignature), keyID: get(HeaderProfileKeyID),
		timestamp: get(HeaderProfileTimestamp), audience: get(HeaderEdgeService), publicPrefix: get(HeaderPublicPrefix),
		subjectID: get(HeaderUserID), tenantID: get(HeaderOrgID), requestID: get(HeaderRequestID),
		edgeKeyID: get(HeaderEdgeKeyID), nonce: get(HeaderEdgeNonce), edgeSignature: get(HeaderEdgeSignature),
		signedPath: get(HeaderEdgeSignedPath), edgeTimestamp: get(HeaderEdgeTimestamp),
	}
	if err := profileEnvelopeShape(r, e, cfg); err != nil {
		return profileEnvelope{}, err
	}
	identityConfig := cfg.IdentityConfig
	if identityConfig.SignatureWindow <= 0 {
		identityConfig.SignatureWindow = cfg.SignatureWindow
	}
	if err := verifyEdgeSignature(r, identityConfig); err != nil {
		return profileEnvelope{}, fmt.Errorf("edge_profile_identity_invalid: %w", err)
	}
	now := time.Now()
	if cfg.Now != nil {
		now = cfg.Now()
	}
	if err := validateTimestampAt(e.timestamp, cfg.SignatureWindow, now); err != nil {
		return profileEnvelope{}, err
	}
	return e, nil
}

// profileEnvelopeShape checks presence and the request binding before any
// cryptographic work.
func profileEnvelopeShape(r *http.Request, e profileEnvelope, cfg ProfileVerifyConfig) error {
	for _, value := range []string{e.encoded, e.signature, e.keyID, e.timestamp, e.audience, e.subjectID, e.requestID, e.edgeKeyID, e.nonce, e.edgeSignature, e.signedPath} {
		if value == "" {
			return fmt.Errorf("edge_profile_binding_missing")
		}
	}
	switch {
	case !strings.HasPrefix(e.signature, profileSignatureVersion+"="):
		return fmt.Errorf("edge_profile_signature_version_invalid")
	case e.audience != strings.TrimSpace(cfg.ExpectedAudience):
		return fmt.Errorf("edge_profile_audience_mismatch")
	case strings.TrimSpace(cfg.ExpectedPublicPrefix) != "" && e.publicPrefix != strings.TrimSpace(cfg.ExpectedPublicPrefix):
		return fmt.Errorf("edge_profile_public_prefix_mismatch")
	case e.signedPath != requestSignedPath(r):
		return fmt.Errorf("edge_profile_path_mismatch")
	case e.timestamp != e.edgeTimestamp:
		return fmt.Errorf("edge_profile_timestamp_mismatch")
	case !supportsEntitlementIdentitySignature(e.edgeSignature):
		return fmt.Errorf("edge_profile_identity_version_invalid")
	}
	return nil
}

func verifyProfileSignature(method string, e profileEnvelope, keys []flagSigningKey) bool {
	payload := BuildProfileSignaturePayload(e.keyID, method, e.signedPath, e.audience, e.publicPrefix,
		e.subjectID, e.tenantID, e.requestID, e.edgeKeyID, e.timestamp, e.nonce, e.edgeSignature, e.encoded)
	presented := strings.TrimPrefix(e.signature, profileSignatureVersion+"=")
	for _, candidate := range keys {
		if candidate.secret != "" && candidate.keyID == e.keyID && hmac.Equal([]byte(presented), []byte(signPayload(candidate.secret, payload))) {
			return true
		}
	}
	return false
}
