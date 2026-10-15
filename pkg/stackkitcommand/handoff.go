package stackkitcommand

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

const (
	// FederationHandoffCapability marks an agent that runs FEDERATION_HANDOFF.
	FederationHandoffCapability = "stackkit.federation-handoff.v1"
	// MaxHandoffDocumentBytes bounds one public hand-off document.
	MaxHandoffDocumentBytes = 128 << 10

	// HandoffResultVersion is the data schema of a hand-off command result.
	HandoffResultVersion = "techstack.federation-handoff-result/v1"

	HandoffMemberAdmit   = "member-admit"
	HandoffMemberJoin    = "member-join"
	HandoffMemberCertify = "member-certify"
	HandoffMemberImport  = "member-import"
	HandoffBindingAdopt  = "binding-adopt"
	// HandoffLinkRequirement returns the node's own inter-site link
	// requirement, the exact document an external binding must match.
	HandoffLinkRequirement = "link-requirement"
	HandoffPeerRequest     = "peer-request"
	HandoffPeerEnroll      = "peer-enroll"
	HandoffPeerInstall     = "peer-install"
	HandoffLinkBind        = "link-bind"
)

// HandoffStep describes what one step needs and returns. Every document is
// public: admissions, evidence-key requests and certificates, workload-peer
// CSRs and certificates, an opaque external binding, link custody handles.
type HandoffStep struct {
	Name string
	// Authority is true for a step the Home authority node runs, false for the
	// Cloud member. link-bind runs on both and is marked Authority=false.
	Authority bool
	// RefPattern and AuxPattern, when set, require handoff_ref/handoff_ref2 to
	// match; when nil the field must be empty.
	RefPattern *regexp.Regexp
	AuxPattern *regexp.Regexp
	// NeedsDocument is true when the step consumes a document;
	// ProducesDocument when the result carries one back.
	NeedsDocument    bool
	ProducesDocument bool
}

var (
	handoffNodePattern   = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)
	handoffKeyIDPattern  = regexp.MustCompile(`^ed25519://sha256/[0-9a-f]{64}$`)
	handoffServerPattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9.-]{0,251}[a-z0-9])?$`)
	handoffRootPattern   = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	handoffSteps         = []HandoffStep{
		{Name: HandoffMemberAdmit, Authority: true, RefPattern: handoffNodePattern, ProducesDocument: true},
		{Name: HandoffMemberJoin, RefPattern: handoffKeyIDPattern, NeedsDocument: true, ProducesDocument: true},
		{Name: HandoffMemberCertify, Authority: true, RefPattern: handoffNodePattern, NeedsDocument: true, ProducesDocument: true},
		{Name: HandoffMemberImport, NeedsDocument: true},
		{Name: HandoffBindingAdopt, Authority: true, NeedsDocument: true},
		{Name: HandoffLinkRequirement, Authority: true, ProducesDocument: true},
		{Name: HandoffPeerRequest, RefPattern: handoffNodePattern, ProducesDocument: true},
		{Name: HandoffPeerEnroll, Authority: true, RefPattern: handoffNodePattern, AuxPattern: handoffServerPattern, NeedsDocument: true, ProducesDocument: true},
		{Name: HandoffPeerInstall, AuxPattern: handoffRootPattern, NeedsDocument: true},
		{Name: HandoffLinkBind, NeedsDocument: true},
	}
)

// LookupHandoffStep returns the closed step of that name.
func LookupHandoffStep(name string) (HandoffStep, bool) {
	for _, step := range handoffSteps {
		if step.Name == name {
			return step, true
		}
	}
	return HandoffStep{}, false
}

func IsHandoffOperation(operation agentpb.StackKitOperation) bool {
	return operation == agentpb.StackKitOperation_STACKKIT_OPERATION_FEDERATION_HANDOFF
}

func handoffFieldsPresent(command *agentpb.StackKitCommand) bool {
	return command.HandoffStep != "" || command.HandoffRef != "" || command.HandoffRef2 != "" || len(command.HandoffDocument) != 0
}

func validateHandoffCommand(command *agentpb.StackKitCommand) error {
	if !command.OwnerApproved {
		return fmt.Errorf("StackKit federation hand-off requires Owner approval")
	}
	step, ok := LookupHandoffStep(strings.TrimSpace(command.HandoffStep))
	if !ok {
		return fmt.Errorf("StackKit federation hand-off step is not recognized")
	}
	for _, field := range []struct {
		name    string
		value   string
		pattern *regexp.Regexp
	}{
		{"handoff_ref", command.HandoffRef, step.RefPattern},
		{"handoff_ref2", command.HandoffRef2, step.AuxPattern},
	} {
		switch {
		case field.pattern == nil && field.value != "":
			return fmt.Errorf("StackKit federation hand-off %s is not valid for step %s", field.name, step.Name)
		case field.pattern != nil && !field.pattern.MatchString(field.value):
			return fmt.Errorf("StackKit federation hand-off %s is invalid for step %s", field.name, step.Name)
		}
	}
	size := len(command.HandoffDocument)
	switch {
	case step.NeedsDocument && (size == 0 || size > MaxHandoffDocumentBytes):
		return fmt.Errorf("StackKit federation hand-off step %s requires one public document within %d bytes", step.Name, MaxHandoffDocumentBytes)
	case !step.NeedsDocument && size != 0:
		return fmt.Errorf("StackKit federation hand-off step %s takes no document", step.Name)
	case step.NeedsDocument && !json.Valid(command.HandoffDocument):
		return fmt.Errorf("StackKit federation hand-off document is not JSON")
	}
	if command.InventoryJson != nil && len(command.InventoryJson) != 0 {
		return fmt.Errorf("StackKit federation hand-off takes no Inventory")
	}
	return nil
}

// HandoffResult is the evidence a successful hand-off step returns.
type HandoffResult struct {
	Step string
	// Document is the public document the step produced, empty when it
	// produces none.
	Document []byte
	// HomeKeyID is the authority's owner key id, reported by member-admit for
	// the member to compare out of band.
	HomeKeyID string
}

// ParseHandoffResult accepts a successful hand-off result only when it names
// the dispatched step and pins the document it carries.
func ParseHandoffResult(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (HandoffResult, error) {
	if command == nil || result == nil || !result.Success || !IsHandoffOperation(command.Operation) {
		return HandoffResult{}, fmt.Errorf("successful StackKit federation hand-off evidence is required")
	}
	step, ok := LookupHandoffStep(command.HandoffStep)
	if !ok {
		return HandoffResult{}, fmt.Errorf("StackKit federation hand-off step is not recognized")
	}
	var envelope struct {
		Status string `json:"status"`
		Data   struct {
			SchemaVersion  string `json:"schemaVersion"`
			Step           string `json:"step"`
			DocumentSHA256 string `json:"documentSha256"`
			HomeKeyID      string `json:"homeKeyId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return HandoffResult{}, fmt.Errorf("decode StackKit federation hand-off evidence: %w", err)
	}
	if envelope.Status != commandResultStatusSuccess || envelope.Data.SchemaVersion != HandoffResultVersion || envelope.Data.Step != step.Name {
		return HandoffResult{}, fmt.Errorf("StackKit federation hand-off evidence does not name the dispatched step")
	}
	document := result.HandoffDocument
	if len(document) > MaxHandoffDocumentBytes {
		return HandoffResult{}, fmt.Errorf("StackKit federation hand-off document exceeds %d bytes", MaxHandoffDocumentBytes)
	}
	if !step.ProducesDocument {
		if len(document) != 0 {
			return HandoffResult{}, fmt.Errorf("StackKit federation hand-off step %s returns no document", step.Name)
		}
	} else {
		digest := sha256.Sum256(document)
		if len(document) == 0 || !json.Valid(document) || envelope.Data.DocumentSHA256 != "sha256:"+hex.EncodeToString(digest[:]) {
			return HandoffResult{}, fmt.Errorf("StackKit federation hand-off document does not match its evidence")
		}
	}
	if step.Name == HandoffMemberAdmit && !handoffKeyIDPattern.MatchString(envelope.Data.HomeKeyID) {
		return HandoffResult{}, fmt.Errorf("StackKit member admission did not report the Home owner key id")
	}
	return HandoffResult{Step: step.Name, Document: append([]byte(nil), document...), HomeKeyID: envelope.Data.HomeKeyID}, nil
}
