package stackkitcommand

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// MinWalletAIProviderRelease is the first StackKits release whose CLI binds an
// external AI provider key to a delivered Wallet item (`stackkit setup
// ai-provider`, StackKits #2037). The Wallet item and host delivery recipient
// commands are older; the three ship to a node together.
const MinWalletAIProviderRelease = "v0.53.0"

const (
	maxAuthorityKeyIDBytes = 128
	maxAIProviderURLBytes  = 512
)

var (
	walletItemRefPattern = regexp.MustCompile(`^itm_[a-z2-7]{26}$`)
	authorityKeyIDRegexp = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)
)

// IsWalletItemDeliveryOperation reports the operations that bring a kombify
// Wallet item to the node and bind it to the external AI provider. They carry
// public values and ciphertext only: the Wallet seals the value to the node.
func IsWalletItemDeliveryOperation(operation agentpb.StackKitOperation) bool {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT,
		agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP:
		return true
	default:
		return false
	}
}

// NormalizeAIProviderBaseURL mirrors StackKits: an https base URL without
// credentials, query or fragment, of safe ASCII, without a trailing slash.
func NormalizeAIProviderBaseURL(raw string) (string, error) {
	invalid := fmt.Errorf("the external provider needs an https base URL without credentials, query or fragment")
	value := strings.TrimRight(strings.TrimSpace(raw), "/")
	if value == "" || len(value) > maxAIProviderURLBytes {
		return "", invalid
	}
	for _, c := range value {
		if c <= 0x20 || c >= 0x7f || strings.ContainsRune(`"\<>&$'`+"`", c) {
			return "", invalid
		}
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme != "https" || parsed.Hostname() == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" || parsed.Opaque != "" {
		return "", invalid
	}
	return value, nil
}

// ValidWalletItemRef reports a kombify Wallet item id.
func ValidWalletItemRef(value string) bool { return walletItemRefPattern.MatchString(value) }

// validateWalletItemDeliveryFields keeps each input on the operation that
// consumes it.
func validateWalletItemDeliveryFields(command *agentpb.StackKitCommand) error {
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT &&
		(command.DeliveryAuthorityKey != "" || command.DeliveryAuthorityKeyId != "") {
		return fmt.Errorf("StackKit delivery authority is only valid for host delivery recipient")
	}
	if command.Operation != agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP &&
		(command.AiProviderBaseUrl != "" || command.WalletItemRef != "" || command.AiProviderDisable) {
		return fmt.Errorf("StackKit AI provider fields are only valid for AI provider setup")
	}
	return nil
}

func validateWalletItemDeliveryCommand(command *agentpb.StackKitCommand) error {
	if !command.OwnerApproved {
		return fmt.Errorf("StackKit Wallet item delivery requires Owner approval")
	}
	if !ReleaseAtLeast(command.Release.Version, MinWalletAIProviderRelease) {
		return fmt.Errorf("StackKit Wallet item delivery requires StackKits %s or newer", MinWalletAIProviderRelease)
	}
	if len(command.InventoryJson) != 0 || len(command.CandidateSpecJson) != 0 || len(command.AdvancedCapability) != 0 || command.DryRun || command.Offline {
		return fmt.Errorf("StackKit Wallet item delivery takes no Inventory, candidate, capability, dry run or offline mode")
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
		challenge := command.RecipientChallenge
		if challenge == "" || challenge != strings.TrimSpace(challenge) || len(challenge) > MaxRecipientChallengeBytes {
			return fmt.Errorf("StackKit host delivery recipient requires a bare challenge within %d bytes", MaxRecipientChallengeBytes)
		}
		if (command.DeliveryAuthorityKey == "") != (command.DeliveryAuthorityKeyId == "") {
			return fmt.Errorf("StackKit delivery authority needs both its key and key id")
		}
		if command.DeliveryAuthorityKey != "" {
			key, err := base64.RawStdEncoding.Strict().DecodeString(command.DeliveryAuthorityKey)
			if err != nil || len(key) != 32 || !authorityKeyIDRegexp.MatchString(command.DeliveryAuthorityKeyId) {
				return fmt.Errorf("StackKit delivery authority must be an Ed25519 public key and a bare key id")
			}
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT:
		tenant := command.DeliveryTenantRef
		if len(command.SealedDelivery) == 0 || len(command.SealedDelivery) > MaxSealedDeliveryBytes || tenant == "" || tenant != strings.TrimSpace(tenant) || len(tenant) > MaxDeliveryTenantRefBytes {
			return fmt.Errorf("StackKit Wallet item import requires one sealed delivery within %d bytes and its tenant", MaxSealedDeliveryBytes)
		}
	case agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP:
		if command.AiProviderDisable {
			if command.AiProviderBaseUrl != "" || command.WalletItemRef != "" || command.DeliveryTenantRef != "" {
				return fmt.Errorf("StackKit AI provider removal takes no provider, item or tenant")
			}
			return nil
		}
		if normalized, err := NormalizeAIProviderBaseURL(command.AiProviderBaseUrl); err != nil || normalized != command.AiProviderBaseUrl {
			return fmt.Errorf("StackKit AI provider setup requires a normalized https base URL")
		}
		tenant := command.DeliveryTenantRef
		if !ValidWalletItemRef(command.WalletItemRef) || tenant == "" || tenant != strings.TrimSpace(tenant) || len(tenant) > MaxDeliveryTenantRefBytes {
			return fmt.Errorf("StackKit AI provider setup requires a Wallet item and its tenant")
		}
	}
	return nil
}

// WalletItemDeliveryArgs is the argv of one Wallet item delivery operation.
// No value ever reaches it: the envelope travels on stdin.
func WalletItemDeliveryArgs(command *agentpb.StackKitCommand) ([]string, error) {
	if err := validateWalletItemDeliveryCommand(command); err != nil {
		return nil, err
	}
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
		args := []string{"host", "delivery", "recipient", "--challenge", command.RecipientChallenge}
		if command.DeliveryAuthorityKey != "" {
			args = append(args, "--authority-key", command.DeliveryAuthorityKey, "--authority-key-id", command.DeliveryAuthorityKeyId)
		}
		return append(args, "--owner-approve"), nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT:
		return []string{"wallet-item", "import", "--tenant-ref", command.DeliveryTenantRef, "--owner-approve"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP:
		if command.AiProviderDisable {
			return []string{"setup", "ai-provider", "--disable", "--json"}, nil
		}
		return []string{"setup", "ai-provider", "--base-url", command.AiProviderBaseUrl, "--tenant-ref", command.DeliveryTenantRef, "--wallet-item", command.WalletItemRef, "--json"}, nil
	default:
		return nil, fmt.Errorf("unsupported StackKit Wallet item operation %s", command.Operation.String())
	}
}

func walletItemDeliveryCommandName(operation agentpb.StackKitOperation) string {
	switch operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
		return "stackkit host delivery recipient"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT:
		return "stackkit wallet-item import"
	case agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP:
		return "stackkit setup ai-provider"
	default:
		return ""
	}
}

// HostDeliveryStatement is the node's owner-signed recipient statement, still
// unverified here: the Wallet verifies its signature against the owner key the
// item's host target names before it seals anything to the key.
type HostDeliveryStatement struct {
	Statement      json.RawMessage
	SiteRef        string
	NodeRef        string
	OwnerPublicKey string
}

func decodeWalletItemEnvelope(command *agentpb.StackKitCommand, result *agentpb.StackKitResult, operation agentpb.StackKitOperation) (json.RawMessage, error) {
	if command == nil || result == nil || !result.Success || command.Operation != operation {
		return nil, fmt.Errorf("successful StackKit %s evidence is required", walletItemDeliveryCommandName(operation))
	}
	var envelope struct {
		SchemaVersion string          `json:"schemaVersion"`
		Command       string          `json:"command"`
		Status        string          `json:"status"`
		Data          json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(result.CommandResultJson, &envelope); err != nil {
		return nil, fmt.Errorf("decode StackKit %s evidence: %w", walletItemDeliveryCommandName(operation), err)
	}
	if envelope.SchemaVersion != CommandResultVersion || envelope.Command != walletItemDeliveryCommandName(operation) || envelope.Status != commandResultStatusSuccess {
		return nil, fmt.Errorf("StackKit %s evidence identity is invalid", walletItemDeliveryCommandName(operation))
	}
	return envelope.Data, nil
}

// ParseHostDeliveryStatement admits a recipient statement that answers the
// dispatched challenge and names the node's Site and node.
func ParseHostDeliveryStatement(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (HostDeliveryStatement, error) {
	data, err := decodeWalletItemEnvelope(command, result, agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT)
	if err != nil {
		return HostDeliveryStatement{}, err
	}
	var statement struct {
		Challenge   string `json:"challenge"`
		SiteRef     string `json:"siteRef"`
		NodeRef     string `json:"nodeRef"`
		OwnerPublic string `json:"ownerPublicKey"`
	}
	if json.Unmarshal(data, &statement) != nil || statement.Challenge != command.RecipientChallenge ||
		statement.SiteRef == "" || statement.NodeRef == "" || statement.OwnerPublic == "" {
		return HostDeliveryStatement{}, fmt.Errorf("StackKit host delivery recipient evidence does not answer the dispatched challenge")
	}
	return HostDeliveryStatement{Statement: data, SiteRef: statement.SiteRef, NodeRef: statement.NodeRef, OwnerPublicKey: statement.OwnerPublic}, nil
}

// WalletItemImportEvidence names the item and version the node now holds.
type WalletItemImportEvidence struct {
	ItemRef string
	Version uint64
}

// ParseWalletItemImportEvidence admits a successful import for the tenant the
// delivery was addressed to.
func ParseWalletItemImportEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (WalletItemImportEvidence, error) {
	data, err := decodeWalletItemEnvelope(command, result, agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT)
	if err != nil {
		return WalletItemImportEvidence{}, err
	}
	var receipt struct {
		TenantRef string `json:"tenantRef"`
		ItemRef   string `json:"itemRef"`
		Version   uint64 `json:"version"`
	}
	if json.Unmarshal(data, &receipt) != nil || receipt.TenantRef != command.DeliveryTenantRef || !ValidWalletItemRef(receipt.ItemRef) || receipt.Version == 0 {
		return WalletItemImportEvidence{}, fmt.Errorf("StackKit Wallet item import evidence is invalid")
	}
	return WalletItemImportEvidence{ItemRef: receipt.ItemRef, Version: receipt.Version}, nil
}

// AIProviderEvidence is the secret-free outcome of AI provider setup.
type AIProviderEvidence struct {
	Enabled  bool
	BaseURL  string
	ItemRef  string
	SpecHash string
}

// ParseAIProviderEvidence admits a setup result that matches the dispatched
// provider and item, or a removal.
func ParseAIProviderEvidence(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) (AIProviderEvidence, error) {
	data, err := decodeWalletItemEnvelope(command, result, agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP)
	if err != nil {
		return AIProviderEvidence{}, err
	}
	var outcome struct {
		WorkloadRef string `json:"workloadRef"`
		Provider    string `json:"provider"`
		BaseURL     string `json:"baseUrl"`
		ItemRef     string `json:"itemRef"`
		SpecHash    string `json:"specHash"`
	}
	if json.Unmarshal(data, &outcome) != nil || outcome.WorkloadRef != "ai" || !specHashPattern.MatchString(outcome.SpecHash) {
		return AIProviderEvidence{}, fmt.Errorf("StackKit AI provider evidence is invalid")
	}
	if command.AiProviderDisable {
		if outcome.Provider != "disabled" {
			return AIProviderEvidence{}, fmt.Errorf("StackKit AI provider evidence does not confirm the removal")
		}
		return AIProviderEvidence{SpecHash: outcome.SpecHash}, nil
	}
	if outcome.Provider != "enabled" || outcome.BaseURL != command.AiProviderBaseUrl || outcome.ItemRef != command.WalletItemRef {
		return AIProviderEvidence{}, fmt.Errorf("StackKit AI provider evidence does not match the dispatched provider")
	}
	return AIProviderEvidence{Enabled: true, BaseURL: outcome.BaseURL, ItemRef: outcome.ItemRef, SpecHash: outcome.SpecHash}, nil
}

func validateWalletItemDeliveryResult(command *agentpb.StackKitCommand, result *agentpb.StackKitResult) error {
	var err error
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_DELIVERY_RECIPIENT:
		_, err = ParseHostDeliveryStatement(command, result)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_WALLET_ITEM_IMPORT:
		_, err = ParseWalletItemImportEvidence(command, result)
	case agentpb.StackKitOperation_STACKKIT_OPERATION_AI_PROVIDER_SETUP:
		_, err = ParseAIProviderEvidence(command, result)
	}
	return err
}
