package guardreach

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const maintenanceOriginPath = "/v1/internal/mcp/maintenance-authority"

// MaintenanceOriginProof retains the original transport bytes. It is evidence,
// never a principal: callers must first verify the shared MCP origin envelope.
type MaintenanceOriginProof struct {
	Method        string `json:"method"`
	Path          string `json:"path"`
	Context       string `json:"context"`
	Signature     string `json:"signature"`
	KeyID         string `json:"keyId"`
	Timestamp     string `json:"timestamp"`
	Nonce         string `json:"nonce"`
	BodySHA256    string `json:"bodySha256"`
	BodyBase64url string `json:"bodyBase64url"`
}

// Digest uses the Gateway's existing domain-separated proof correlation.
func (p MaintenanceOriginProof) Digest() string {
	sum := sha256.Sum256([]byte(strings.Join([]string{
		"kombify.mcp-maintenance-origin-proof.v1", p.Method, p.Path, p.Context,
		p.Signature, p.KeyID, p.Timestamp, p.Nonce, p.BodySHA256, p.BodyBase64url,
	}, "\n")))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// CheckMaintenanceOrigin refreshes current authority without spending the MCP nonce.
func (g *Gateway) CheckMaintenanceOrigin(ctx context.Context, proof MaintenanceOriginProof, owner, tenant string, expires time.Time) error {
	return g.maintenanceOrigin(ctx, "check", proof, owner, tenant, expires)
}

// ReserveMaintenanceOrigin spends the original nonce exactly once. An unknown
// acknowledgement is an error; this client never retries the operation.
func (g *Gateway) ReserveMaintenanceOrigin(ctx context.Context, proof MaintenanceOriginProof, owner, tenant string, expires time.Time) error {
	return g.maintenanceOrigin(ctx, "reserve", proof, owner, tenant, expires)
}

func (g *Gateway) maintenanceOrigin(ctx context.Context, operation string, proof MaintenanceOriginProof, owner, tenant string, expires time.Time) error {
	if g == nil || g.http == nil || owner == "" || tenant == "" || !expires.After(time.Now()) {
		return errors.New("maintenance origin authority unavailable")
	}
	deadline := time.Now().Add(10 * time.Second)
	if expires.Before(deadline) {
		deadline = expires
	}
	ctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	body, err := json.Marshal(struct {
		SchemaVersion string                 `json:"schemaVersion"`
		Operation     string                 `json:"operation"`
		Origin        MaintenanceOriginProof `json:"origin"`
	}{"kombify.mcp-maintenance-origin-check.v1", operation, proof})
	if err != nil {
		return errors.New("maintenance origin proof invalid")
	}
	var raw json.RawMessage
	// Even an injected client cannot follow a redirect carrying service custody.
	client := *g
	httpClient := *g.http
	httpClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	client.http = &httpClient
	if _, err := client.do(ctx, "mcp-maintenance:"+operation, http.MethodPost, maintenanceOriginPath, body, g.obo(owner, tenant, nil), &raw); err != nil {
		return err
	}
	result, err := decodeMaintenanceOriginResult(raw)
	if err != nil {
		return err
	}
	if result.SchemaVersion != "kombify.mcp-maintenance-origin-check-result.v1" ||
		result.Operation != operation || !result.Allowed || result.RequestDigest != proof.Digest() ||
		result.ExpiresAt != expires.Unix() || !expires.After(time.Now()) {
		return errors.New("maintenance origin result binding invalid")
	}
	return nil
}

type maintenanceOriginResult struct {
	SchemaVersion string `json:"schemaVersion"`
	Operation     string `json:"operation"`
	Allowed       bool   `json:"allowed"`
	RequestDigest string `json:"requestDigest"`
	ExpiresAt     int64  `json:"expiresAt"`
}

func decodeMaintenanceOriginResult(raw []byte) (maintenanceOriginResult, error) {
	var result maintenanceOriginResult
	fields := map[string]json.RawMessage{}
	allowedFields := map[string]bool{"schemaVersion": true, "operation": true, "allowed": true, "requestDigest": true, "expiresAt": true}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return result, errors.New("maintenance origin result invalid")
	}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok || !allowedFields[key] || fields[key] != nil {
			return result, errors.New("maintenance origin result ambiguous")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return result, errors.New("maintenance origin result invalid")
		}
		fields[key] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') || len(fields) != 5 {
		return result, errors.New("maintenance origin result invalid")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return result, errors.New("maintenance origin result invalid")
	}
	decoder = json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&result) != nil {
		return result, errors.New("maintenance origin result invalid")
	}
	return result, nil
}
