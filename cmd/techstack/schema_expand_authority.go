package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"

	"github.com/kombifyio/techstack/internal/gocommon/oidcclient"
)

// The reviewed immutable reusable producer is the only admitted authority.
const schemaExpandProducerSHA = "2771f6be2f7612f898f979da4e08b59075efe3d4"
const schemaExpandIssuer = "https://token.actions.githubusercontent.com"
const schemaExpandProducer = "public-schema-producer-unavailable/.github/.github/workflows/techstack-schema-expand.yml"
const schemaExpandRepository = "kombifyio/TechStack"

type schemaExpandAuthority struct {
	Schema             string `json:"schema"`
	SourceSHA          string `json:"source_sha"`
	ExecutionSHA       string `json:"execution_sha"`
	ManifestSHA256     string `json:"manifest_sha256"`
	LocalReceiptSHA256 string `json:"local_receipt_sha256"`
	LocalRunID         string `json:"local_run_id"`
	AuthoritySHA       string `json:"authority_sha"`
	ProviderResource   string `json:"provider_resource"`
	RunID              string `json:"run_id"`
	Nonce              string `json:"nonce"`
}

func verifySchemaExpandAuthority(ctx context.Context) (schemaExpandAuthority, error) {
	var body schemaExpandAuthority
	deny := func() (schemaExpandAuthority, error) { return schemaExpandAuthority{}, errDatabaseDeliveryAdmission }
	raw := []byte(os.Getenv("TECHSTACK_SCHEMA_AUTH_BODY"))
	token := os.Getenv("TECHSTACK_SCHEMA_AUTH_TOKEN")
	if len(raw) == 0 || len(raw) > 8192 || len(token) == 0 || len(token) > 16384 || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(schemaExpandProducerSHA) {
		return deny()
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		return deny()
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return deny()
	}
	canonical, err := json.Marshal(body)
	if err != nil || !bytes.Equal(raw, canonical) {
		return deny()
	}
	sha := regexp.MustCompile(`^[a-f0-9]{40}$`)
	hash := regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)
	positive := regexp.MustCompile(`^[1-9][0-9]*$`)
	if body.Schema != "techstack.schema-expand-company.v1" || !sha.MatchString(body.SourceSHA) || !sha.MatchString(body.ExecutionSHA) ||
		!sha.MatchString(body.AuthoritySHA) || !hash.MatchString(body.ManifestSHA256) ||
		!hash.MatchString(body.LocalReceiptSHA256) || !positive.MatchString(body.LocalRunID) ||
		!positive.MatchString(body.RunID) || !regexp.MustCompile(`^[a-f0-9]{64}$`).MatchString(body.Nonce) {
		return deny()
	}
	sum := sha256.Sum256(raw)
	verifier, err := oidcclient.NewVerifier(oidcclient.VerifierConfig{
		Issuer:   schemaExpandIssuer,
		Audience: fmt.Sprintf("urn:kombify:delivery:techstack-company:%x", sum),
		JWKSURL:  schemaExpandIssuer + "/.well-known/jwks",
	})
	if err != nil {
		return deny()
	}
	claims, err := verifier.Verify(ctx, token)
	if err != nil || claims.IssuedAt <= 0 || claims.Expires <= claims.IssuedAt || claims.Expires-claims.IssuedAt > 600 {
		return deny()
	}
	want := map[string]string{
		"repository": schemaExpandRepository, "repository_id": "1129956899", "repository_owner_id": "249091777",
		"ref": "refs/heads/main", "run_attempt": "1",
		"workflow_ref":     schemaExpandRepository + "/.github/workflows/early-access-merge.yml@refs/heads/main",
		"job_workflow_ref": schemaExpandProducer + "@" + schemaExpandProducerSHA,
		"job_workflow_sha": schemaExpandProducerSHA, "sha": body.ExecutionSHA, "workflow_sha": body.ExecutionSHA,
		"run_id": body.RunID, "sub": "repo:" + schemaExpandRepository + ":environment:techstack-schema-production",
	}
	for key, expected := range want {
		actual, ok := claims.Raw[key].(string)
		if !ok || actual != expected {
			return deny()
		}
	}
	event, ok := claims.Raw["event_name"].(string)
	if !ok || (event != "push" && event != "workflow_dispatch") || (event == "push" && body.SourceSHA != body.ExecutionSHA) {
		return deny()
	}
	jti, ok := claims.Raw["jti"].(string)
	if !ok || !regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`).MatchString(jti) {
		return deny()
	}
	return body, nil
}
