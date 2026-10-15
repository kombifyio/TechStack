package mcporiginauth

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type envelope struct {
	Version            int             `json:"v"`
	Issuer             string          `json:"iss"`
	Audience           string          `json:"aud"`
	Subject            string          `json:"sub"`
	OrganizationID     string          `json:"org_id"`
	Scopes             []string        `json:"scopes"`
	ActorSubject       string          `json:"actor_sub"`
	AgentClass         string          `json:"agent_class"`
	AgentInstanceID    json.RawMessage `json:"agent_instance_id"`
	ClientID           string          `json:"client_id"`
	PrincipalType      string          `json:"principal_type"`
	RequestID          string          `json:"request_id"`
	TraceID            string          `json:"trace_id"`
	ServerKey          string          `json:"server_key"`
	ToolName           string          `json:"tool_name"`
	OperationIDs       []string        `json:"operation_ids"`
	ArgumentsSHA256    string          `json:"arguments_sha256"`
	IdempotencyKey     string          `json:"idempotency_key"`
	CatalogRevision    string          `json:"catalog_revision"`
	DelegatedAuthority json.RawMessage `json:"delegated_authority"`
	Transport          struct {
		Method     string `json:"method"`
		Path       string `json:"path"`
		BodySHA256 string `json:"body_sha256"`
		Nonce      string `json:"nonce"`
		IssuedAt   int64  `json:"iat"`
		ExpiresAt  int64  `json:"exp"`
	} `json:"transport"`
}

type toolCall struct {
	Method string `json:"method"`
	Params struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	} `json:"params"`
}

func verify(r *http.Request, cfg Config, nonces *nonceCache) (Context, error) {
	headers, err := parseOriginHeaders(r.Header, cfg.Keys)
	if err != nil {
		return Context{}, err
	}
	body, path, signingVersion, err := verifySignedTransport(r, cfg, headers)
	if err != nil {
		return Context{}, err
	}
	env, agentInstanceID, now, err := decodeOriginPrincipal(headers.encoded, signingVersion, cfg)
	if err != nil {
		return Context{}, err
	}
	if bindingErr := env.verifyTransport(r.Method, path, headers, now); bindingErr != nil {
		return Context{}, bindingErr
	}
	canonicalArgs, err := verifyToolArguments(body, env)
	if err != nil {
		return Context{}, err
	}
	verified := Context{
		Subject: env.Subject, OrganizationID: env.OrganizationID, Scopes: append([]string(nil), env.Scopes...),
		ActorSubject: env.ActorSubject, AgentClass: env.AgentClass, ClientID: env.ClientID,
		AgentInstanceID: agentInstanceID,
		PrincipalType:   env.PrincipalType, RequestID: env.RequestID, ToolName: env.ToolName,
		TraceID:      env.TraceID,
		OperationIDs: append([]string(nil), env.OperationIDs...), IdempotencyKey: env.IdempotencyKey,
		CatalogRevision: env.CatalogRevision,
		ExpiresAt:       time.Unix(env.Transport.ExpiresAt, 0),
	}
	if env.Version == 2 {
		return verifyDelegatedDispatch(r.Context(), cfg, env, verified, canonicalArgs, headers, now)
	}
	if len(env.DelegatedAuthority) != 0 {
		return Context{}, errors.New("delegated authority requires v2")
	}
	if !nonces.consume(headers.nonce, verified.ExpiresAt, cfg.Now()) {
		return Context{}, errors.New("nonce replayed")
	}
	return verified, nil
}

type originHeaders struct {
	encoded, signature, timestampText, nonce, bodyHash, secret string
	timestamp                                                  int64
}

func parseOriginHeaders(header http.Header, keys map[string]string) (originHeaders, error) {
	encoded := strings.TrimSpace(header.Get(contextHeader))
	signature := strings.TrimSpace(header.Get(signatureHeader))
	keyID := strings.TrimSpace(header.Get(keyIDHeader))
	timestampText := strings.TrimSpace(header.Get(timestampHeader))
	nonce := strings.TrimSpace(header.Get(nonceHeader))
	bodyHash := strings.TrimSpace(header.Get(bodyHashHeader))
	secret := keys[keyID]
	if encoded == "" || signature == "" || keyID == "" || timestampText == "" || nonce == "" || bodyHash == "" || secret == "" {
		return originHeaders{}, errors.New("incomplete origin envelope")
	}
	timestamp, err := strconv.ParseInt(timestampText, 10, 64)
	if err != nil {
		return originHeaders{}, errors.New("invalid timestamp")
	}
	return originHeaders{encoded: encoded, signature: signature, timestampText: timestampText, nonce: nonce, bodyHash: bodyHash, secret: secret, timestamp: timestamp}, nil
}

func verifySignedTransport(r *http.Request, cfg Config, headers originHeaders) ([]byte, string, string, error) {
	body, err := io.ReadAll(io.LimitReader(r.Body, cfg.MaxBodyBytes+1))
	if err != nil {
		return nil, "", "", fmt.Errorf("read body: %w", err)
	}
	if int64(len(body)) > cfg.MaxBodyBytes {
		return nil, "", "", errors.New("body too large")
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	actualBodyHash := digest(body)
	if !constantEqual(headers.bodyHash, actualBodyHash) {
		return nil, "", "", errors.New("body hash mismatch")
	}
	path := r.URL.EscapedPath()
	if r.URL.RawQuery != "" {
		path += "?" + r.URL.RawQuery
	}
	signingVersion := version
	if strings.HasPrefix(headers.signature, "v2=") {
		signingVersion = "v2"
	}
	signingInput := strings.Join([]string{signingVersion, strings.ToUpper(r.Method), path, headers.timestampText, headers.nonce, headers.bodyHash, headers.encoded}, "\n")
	wantSignature := signingVersion + "=" + hmacDigest(headers.secret, signingInput)
	if !constantEqual(headers.signature, wantSignature) {
		return nil, "", "", errors.New("signature mismatch")
	}
	return body, path, signingVersion, nil
}

func decodeOriginPrincipal(encoded, signingVersion string, cfg Config) (envelope, string, int64, error) {
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return envelope{}, "", 0, errors.New("invalid context encoding")
	}
	var env envelope
	if contextErr := json.Unmarshal(decoded, &env); contextErr != nil {
		return envelope{}, "", 0, errors.New("invalid context json")
	}
	now := cfg.Now().Unix()
	if (env.Version != 1 && env.Version != 2) || (env.Version == 2) != (signingVersion == "v2") || env.Issuer != "kombify-gateway" || env.Audience != cfg.Audience || env.ServerKey != cfg.ServerKey || strings.TrimSpace(env.Subject) == "" {
		return envelope{}, "", 0, errors.New("context authority mismatch")
	}
	// This optional additive v1 member is covered by the existing full-envelope
	// signature. Never normalize it or infer it from the owner, actor or class.
	agentInstanceID, identityErr := parseAgentInstanceID(env.AgentInstanceID)
	if identityErr != nil {
		return envelope{}, "", 0, identityErr
	}
	return env, agentInstanceID, now, nil
}

func (env envelope) verifyTransport(method, path string, headers originHeaders, now int64) error {
	if env.Transport.Method != strings.ToUpper(method) || env.Transport.Path != path || env.Transport.BodySHA256 != headers.bodyHash || env.Transport.Nonce != headers.nonce || env.Transport.IssuedAt != headers.timestamp || env.Transport.ExpiresAt < now || env.Transport.IssuedAt > now+30 || env.Transport.ExpiresAt-env.Transport.IssuedAt > 120 {
		return errors.New("transport binding mismatch")
	}
	return nil
}

func verifyToolArguments(body []byte, env envelope) ([]byte, error) {
	var call toolCall
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if callErr := decoder.Decode(&call); callErr != nil || call.Method != "tools/call" || call.Params.Name == "" || call.Params.Name != env.ToolName {
		return nil, errors.New("tool binding mismatch")
	}
	canonicalArgs, err := json.Marshal(call.Params.Arguments)
	if err != nil || !constantEqual(env.ArgumentsSHA256, digest(canonicalArgs)) {
		return nil, errors.New("arguments binding mismatch")
	}
	return canonicalArgs, nil
}

func parseAgentInstanceID(value json.RawMessage) (string, error) {
	if len(value) == 0 {
		return "", nil
	}
	var instanceID string
	if err := json.Unmarshal(value, &instanceID); err != nil {
		return "", errors.New("invalid agent instance identity")
	}
	if instanceID == "" || strings.IndexFunc(instanceID, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) >= 0 {
		return "", errors.New("invalid agent instance identity")
	}
	return instanceID, nil
}

func digest(value []byte) string {
	sum := sha256.Sum256(value)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func hmacDigest(secret, value string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(value))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func constantEqual(left, right string) bool {
	return hmac.Equal([]byte(left), []byte(right))
}
