package stackkitcommand

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const ApplicationConnectorCapability = "stackkit.application-connector.v1"
const ApplicationConnectorRequestSchema = "stackkit.home-assistant-connector-request/v1"
const ApplicationConnectorResultSchema = "stackkit.home-assistant-connector-result/v1"
const MaxApplicationConnectorRequestBytes = 16 << 10

// These are the published JSON wire fields, not a second lifecycle authority.
// Resource/revision correlate SaaS intent; StackKits verifies node-local facts.
type ConnectorNode struct {
	SiteRef             string `json:"siteRef"`
	NodeRef             string `json:"nodeRef"`
	ExecutionChannelRef string `json:"executionChannelRef"`
}
type ConnectorSource struct {
	ResourceID      string        `json:"resourceId"`
	ContainerID     string        `json:"containerId"`
	ImageDigest     string        `json:"imageDigest"`
	ServiceRevision int64         `json:"serviceRevision"`
	Node            ConnectorNode `json:"node"`
}
type ConnectorRequest struct {
	SchemaVersion string `json:"schemaVersion"`
	Connector     struct {
		Binding      ConnectorSource `json:"binding"`
		Action       string          `json:"action"`
		LifespanDays int             `json:"lifespanDays"`
	} `json:"connector"`
}
type ConnectorDeployment struct {
	Node        ConnectorNode `json:"node"`
	InstanceRef string        `json:"instanceRef"`
	ContainerID string        `json:"containerId"`
	ImageDigest string        `json:"imageDigest"`
}
type ConnectorSignature struct {
	OwnerRef string `json:"ownerRef"`
	KeyID    string `json:"keyId"`
	Value    string `json:"value"`
}
type ConnectorIssuer struct {
	OwnerRef         string             `json:"ownerRef"`
	HAUserID         string             `json:"haUserId"`
	HAVersion        string             `json:"haVersion"`
	RequestedBinding ConnectorSource    `json:"requestedBinding"`
	SecretRef        string             `json:"secretRef"`
	Status           string             `json:"status"`
	ObservedAt       time.Time          `json:"observedAt"`
	ExpiresAt        time.Time          `json:"expiresAt"`
	Signature        ConnectorSignature `json:"signature"`
}
type ConnectorSetupResult struct {
	APIVersion             string               `json:"apiVersion"`
	Authority              ApplicationAuthority `json:"authority"`
	WorkloadRef            string               `json:"workloadRef"`
	OperationID            string               `json:"operationId"`
	ActionRef              string               `json:"actionRef"`
	ApplyResultHash        string               `json:"applyResultHash"`
	ArtifactDigest         string               `json:"artifactDigest"`
	InstanceRef            string               `json:"instanceRef"`
	ApplicationVersion     string               `json:"applicationVersion"`
	AccountRef             string               `json:"accountRef"`
	Initialized            bool                 `json:"initialized"`
	AdminLoginVerified     bool                 `json:"adminLoginVerified"`
	OnboardingComplete     bool                 `json:"onboardingComplete"`
	Preparation            string               `json:"preparation,omitempty"`
	EmailVerification      string               `json:"emailVerification,omitempty"`
	VerifiedAt             time.Time            `json:"verifiedAt"`
	HomeAssistantConnector struct {
		Issuer                  ConnectorIssuer     `json:"issuer"`
		VerifiedLocalDeployment ConnectorDeployment `json:"verifiedLocalDeployment"`
	} `json:"homeAssistantConnector"`
}
type ConnectorSignedReceipt struct {
	Result    ConnectorSetupResult `json:"result"`
	Signature ConnectorSignature   `json:"signature"`
}
type ApplicationConnectorEvidence struct {
	SchemaVersion          string              `json:"schemaVersion"`
	SignatureVerified      bool                `json:"signatureVerified"`
	ReceiptBase64          string              `json:"receiptBase64"`
	ReceiptDigest          string              `json:"receiptDigest"`
	CurrentLocalDeployment ConnectorDeployment `json:"currentLocalDeployment"`
}

func IsApplicationConnectorOperation(op agentpb.StackKitOperation) bool {
	return op == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR || op == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY
}
func ApplicationConnectorOperationName(op agentpb.StackKitOperation) string {
	if op == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR {
		return "application.connector"
	}
	if op == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY {
		return "application.connector.verify"
	}
	return ""
}
func decodeConnectorJSON(raw []byte, into any) error {
	if err := rejectConnectorDuplicateKeys(raw); err != nil {
		return err
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(into); err != nil {
		return err
	}
	if err := d.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("connector document contains trailing data")
	}
	return nil
}
func DecodeConnectorRequest(raw []byte) (ConnectorRequest, error) {
	var r ConnectorRequest
	if len(raw) == 0 || len(raw) > MaxApplicationConnectorRequestBytes {
		return r, fmt.Errorf("connector request exceeds its bound")
	}
	if err := decodeConnectorJSON(raw, &r); err != nil {
		return r, err
	}
	b := r.Connector.Binding
	if r.SchemaVersion != ApplicationConnectorRequestSchema || !commandIDPattern.MatchString(b.ResourceID) || !sha256Pattern.MatchString(b.ContainerID) || !specHashPattern.MatchString(b.ImageDigest) || b.ServiceRevision <= 0 || r.Connector.LifespanDays < 1 || r.Connector.LifespanDays > 365 || (r.Connector.Action != "ensure" && r.Connector.Action != "revoke") {
		return r, fmt.Errorf("connector request has no closed source/action binding")
	}
	for _, ref := range []string{b.Node.SiteRef, b.Node.NodeRef, b.Node.ExecutionChannelRef} {
		if !commandIDPattern.MatchString(ref) {
			return r, fmt.Errorf("connector request has no exact node binding")
		}
	}
	return r, nil
}
func validateApplicationConnectorCommand(c *agentpb.StackKitCommand) error {
	r, err := DecodeConnectorRequest(c.ApplicationConnectorRequestJson)
	if err != nil {
		return err
	}
	if c.ServiceKey != "smart-home" || c.StackkitInstanceId == "" || !commandIDPattern.MatchString(c.ApplicationOperationId) || c.ApplicationContainerId != "" || c.ApplicationPlanDigest != "" || c.ApplicationAction != "" || handoffFieldsPresent(c) || c.LogTail != 0 || c.LogCursor != "" || c.WorkloadRef != "" || c.DryRun || c.Offline || c.TargetRelease != "" || len(c.InventoryJson) != 0 || len(c.CandidateSpecJson) != 0 || len(c.SealedDelivery) != 0 {
		return fmt.Errorf("connector operation requires only exact workspace and metadata")
	}
	if err := validateLocalExecutionPlacement(c, "connector"); err != nil {
		return err
	}
	if r.Connector.Binding.Node != (ConnectorNode{c.LocalSiteRef, c.LocalNodeRef, c.LocalExecutionChannelRef}) {
		return fmt.Errorf("connector requested node differs from admitted node")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR_VERIFY && c.OwnerApproved {
		return fmt.Errorf("connector verification cannot carry mutation approval")
	}
	if c.Operation == agentpb.StackKitOperation_STACKKIT_OPERATION_APPLICATION_CONNECTOR && !c.OwnerApproved {
		return fmt.Errorf("connector mutation requires Owner approval")
	}
	if c.ExpectedPlanHash != "" && !specHashPattern.MatchString(c.ExpectedPlanHash) {
		return fmt.Errorf("connector plan hash is invalid")
	}
	return nil
}

// ParseApplicationConnectorEvidence relies on the authenticated exact pinned
// CLI attestation. It does not manufacture an Owner signature or own its key.
func ParseApplicationConnectorEvidence(c *agentpb.StackKitCommand, result *agentpb.StackKitResult) (ApplicationConnectorEvidence, ConnectorSignedReceipt, error) {
	var e ApplicationConnectorEvidence
	var receipt ConnectorSignedReceipt
	fail := func() (ApplicationConnectorEvidence, ConnectorSignedReceipt, error) {
		return e, receipt, fmt.Errorf("connector evidence differs from authenticated current setup authority")
	}
	if c == nil || result == nil || !IsApplicationConnectorOperation(c.Operation) || !result.Success {
		return fail()
	}
	r, err := DecodeConnectorRequest(c.ApplicationConnectorRequestJson)
	if err != nil {
		return e, receipt, err
	}
	digest := sha256.Sum256(result.CommandResultJson)
	if !result.PinnedCliEvidenceVerified || result.PinnedCliEvidenceSha256 != "sha256:"+hex.EncodeToString(digest[:]) {
		return fail()
	}
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Status        string          `json:"status"`
		Data          json.RawMessage `json:"data"`
	}
	if json.Unmarshal(result.CommandResultJson, &envelope) != nil || envelope.SchemaVersion != CommandResultVersion || envelope.Command != ResultCommandName(c.Operation) || envelope.Status != "success" || decodeConnectorJSON(envelope.Data, &e) != nil || e.SchemaVersion != ApplicationConnectorResultSchema || !e.SignatureVerified || len(e.ReceiptBase64) > 87384 {
		return fail()
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(e.ReceiptBase64)
	if err != nil || len(raw) == 0 || len(raw) > 64<<10 {
		return fail()
	}
	receiptHash := sha256.Sum256(raw)
	if e.ReceiptDigest != "sha256:"+hex.EncodeToString(receiptHash[:]) || decodeConnectorJSON(raw, &receipt) != nil {
		return fail()
	}
	s := receipt.Result
	i := s.HomeAssistantConnector.Issuer
	b := r.Connector.Binding
	local := e.CurrentLocalDeployment
	wantStatus := "active"
	if r.Connector.Action == "revoke" {
		wantStatus = "revoked"
	}
	validSignature := func(signature ConnectorSignature) bool {
		return signature.OwnerRef != "" && signature.KeyID != "" && signature.Value != ""
	}
	if s.APIVersion != "stackkit.application-setup-result/v1" || s.WorkloadRef != "smart-home" || s.OperationID != c.ApplicationOperationId || s.ActionRef != "home-assistant-owner-bootstrap" || !specHashPattern.MatchString(s.ApplyResultHash) || !specHashPattern.MatchString(s.ArtifactDigest) || !specHashPattern.MatchString(s.Authority.PlanHash) || !specHashPattern.MatchString(s.Authority.LifecycleContractHash) || s.Authority.LifecycleVersion == "" || s.Authority.PackageRef == "" || (c.ExpectedPlanHash != "" && c.ExpectedPlanHash != s.Authority.PlanHash) || s.InstanceRef == "" || s.AccountRef == "" || !s.Initialized || !s.AdminLoginVerified || !s.OnboardingComplete || s.VerifiedAt.IsZero() || s.VerifiedAt.After(time.Now().Add(time.Minute)) || !validSignature(receipt.Signature) || !validSignature(i.Signature) || i.OwnerRef != receipt.Signature.OwnerRef || i.OwnerRef != i.Signature.OwnerRef || i.RequestedBinding != b || i.Status != wantStatus || i.HAUserID != s.AccountRef || i.HAVersion != s.ApplicationVersion || !strings.HasPrefix(i.SecretRef, "secret://home-assistant/connector/") || !sha256Pattern.MatchString(strings.TrimPrefix(i.SecretRef, "secret://home-assistant/connector/")) || i.ObservedAt.IsZero() || i.ObservedAt.After(time.Now().Add(time.Minute)) || (wantStatus == "active" && !i.ExpiresAt.After(time.Now())) || local != s.HomeAssistantConnector.VerifiedLocalDeployment || local.Node != b.Node || local.ContainerID != b.ContainerID || local.ImageDigest != b.ImageDigest || local.InstanceRef != s.InstanceRef {
		return fail()
	}
	return e, receipt, nil
}

// Reject duplicate keys as well as unknown fields: parsers at the producer and
// transport boundary must agree on one exact security-sensitive document.
func rejectConnectorDuplicateKeys(raw []byte) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var read func() error
	read = func() error {
		token, err := d.Token()
		if err != nil {
			return err
		}
		delim, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delim {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return fmt.Errorf("connector document has a duplicate key")
				}
				seen[name] = true
				if err = read(); err != nil {
					return err
				}
			}
		case '[':
			for d.More() {
				if err := read(); err != nil {
					return err
				}
			}
		default:
			return fmt.Errorf("connector document contains an invalid delimiter")
		}
		_, err = d.Token()
		return err
	}
	if err := read(); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return fmt.Errorf("connector document contains trailing data")
	}
	return nil
}
