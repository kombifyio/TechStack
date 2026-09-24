package managedstackkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/stackkits/pkg/backupbinding"
)

const (
	maxResolvedPlanBytes = 32 << 20
	apiVersionField      = "apiVersion"
)

type CustodyReceipt = backupstore.CustodyEvidence

type OperationsProcess struct {
	ChannelRef       string
	SiteRef          string
	NodeRef          string
	Executable       string
	ExecutableSHA256 string
}

type Candidate struct {
	StackKitsVersion string
	Digest           string
	ValidFor         time.Duration
}

func BuildInventory(resolvedPlan []byte, custody CustodyReceipt, process OperationsProcess, candidate Candidate) ([]byte, error) {
	requirement, siteRef, nodeRef, err := validateInventoryAuthority(resolvedPlan, process, candidate)
	if err != nil {
		return nil, err
	}
	inventory := map[string]any{
		"schemaVersion": "stackkit.inventory/v1",
		"executionChannels": map[string]any{
			process.ChannelRef: map[string]any{
				apiVersionField: "stackkit.standard-execution-channel/v1", "kind": "StandardExecutionChannel",
				"channelRef": process.ChannelRef, "siteRef": siteRef, "nodeRef": nodeRef, "operationClass": "standard",
				"operationsProcess": map[string]any{"executable": process.Executable, "executableSha256": process.ExecutableSHA256},
			},
		},
	}
	if requirement != nil {
		issuedAt := custody.ObservedAt.UTC()
		if custody.ObservedAt.IsZero() || len(custody.BindingEvidence) == 0 || len(custody.TargetEvidence) == 0 || len(custody.AttestationEvidence) == 0 {
			return nil, errors.New("managed backup custody receipt is incomplete")
		}
		bindingRef, referenceErr := backupbinding.OpaqueReference("backup-target-binding", custody.BindingEvidence)
		if referenceErr != nil {
			return nil, referenceErr
		}
		targetRef, referenceErr := backupbinding.OpaqueReference("backup-target", custody.TargetEvidence)
		if referenceErr != nil {
			return nil, referenceErr
		}
		attestationRef, referenceErr := backupbinding.OpaqueReference("backup-custody-attestation", custody.AttestationEvidence)
		if referenceErr != nil {
			return nil, referenceErr
		}
		binding, buildErr := backupbinding.Build(backupbinding.Document(requirement), backupbinding.Input{
			BindingRef: bindingRef, BackupTargetRef: targetRef, CustodyAttestationRef: attestationRef,
			StackKitsVersion: candidate.StackKitsVersion, CandidateDigest: candidate.Digest,
			IssuedAt: issuedAt, ValidUntil: issuedAt.Add(candidate.ValidFor),
		})
		if buildErr != nil {
			return nil, fmt.Errorf("build managed backup target binding: %w", buildErr)
		}
		inventory["externalBackupTargetBindings"] = map[string]any{
			siteRef: map[string]any{backupbinding.Capability: binding},
		}
	}
	encoded, err := json.Marshal(inventory)
	if err != nil {
		return nil, fmt.Errorf("encode managed StackKits Inventory: %w", err)
	}
	return append(encoded, '\n'), nil
}

func validateInventoryAuthority(resolvedPlan []byte, process OperationsProcess, candidate Candidate) (map[string]any, string, string, error) {
	if len(resolvedPlan) == 0 || len(resolvedPlan) > maxResolvedPlanBytes {
		return nil, "", "", errors.New("managed StackKits Inventory requires a bounded ResolvedPlan")
	}
	plan, err := decodeObject(resolvedPlan)
	if err != nil {
		return nil, "", "", fmt.Errorf("decode managed StackKits ResolvedPlan: %w", err)
	}
	if plan[apiVersionField] != "stackkit.resolved-plan/v1" {
		return nil, "", "", errors.New("managed StackKits Inventory requires stackkit.resolved-plan/v1")
	}
	requirement, siteRef, nodeRef, err := exactBackupRequirement(plan)
	if err != nil {
		return nil, "", "", err
	}
	if requirement == nil {
		siteRef, nodeRef = process.SiteRef, process.NodeRef
	}
	if processErr := validateOperationsProcess(process, siteRef, nodeRef); processErr != nil {
		return nil, "", "", processErr
	}
	if candidateErr := validateCandidate(candidate); candidateErr != nil {
		return nil, "", "", candidateErr
	}
	return requirement, siteRef, nodeRef, nil
}

func validateOperationsProcess(process OperationsProcess, siteRef, nodeRef string) error {
	if process.ChannelRef == "" || process.ChannelRef != strings.TrimSpace(process.ChannelRef) ||
		process.SiteRef == "" || process.SiteRef != strings.TrimSpace(process.SiteRef) ||
		process.NodeRef == "" || process.NodeRef != strings.TrimSpace(process.NodeRef) ||
		process.SiteRef != siteRef || process.NodeRef != nodeRef || !path.IsAbs(process.Executable) ||
		process.Executable != path.Clean(process.Executable) || !validDigest(process.ExecutableSHA256) {
		return errors.New("managed StackKits operations process does not exactly match the managed Site/node/channel contract")
	}
	return nil
}

func validateCandidate(candidate Candidate) error {
	if candidate.ValidFor <= 0 || candidate.ValidFor > backupbinding.MaxValidity ||
		strings.TrimSpace(candidate.StackKitsVersion) == "" || !validDigest(candidate.Digest) {
		return errors.New("managed StackKits candidate identity or binding validity is invalid")
	}
	return nil
}

func exactBackupRequirement(plan map[string]any) (map[string]any, string, string, error) {
	raw, exists := plan["backupTargetRequirements"]
	if !exists || raw == nil {
		return nil, "", "", nil
	}
	rawSites, ok := raw.(map[string]any)
	if !ok {
		return nil, "", "", errors.New("ResolvedPlan backup target requirements must be an object")
	}
	if len(rawSites) == 0 {
		return nil, "", "", nil
	}
	siteRef, rawCapabilities, ok := singleMapEntry(rawSites)
	if !ok {
		return nil, "", "", errors.New("ResolvedPlan must contain exactly one managed Cloud backup target requirement")
	}
	capabilities, ok := rawCapabilities.(map[string]any)
	if !ok {
		return nil, "", "", errors.New("ResolvedPlan backup target requirement is not a closed capability map")
	}
	capability, rawRequirement, ok := singleMapEntry(capabilities)
	if !ok {
		return nil, "", "", errors.New("ResolvedPlan backup target requirement is not a closed capability map")
	}
	if capability != backupbinding.Capability {
		return nil, "", "", errors.New("ResolvedPlan does not select offsite-object-backup")
	}
	requirement, ok := rawRequirement.(map[string]any)
	if !ok || requirement["siteRef"] != siteRef {
		return nil, "", "", errors.New("ResolvedPlan backup target requirement has inconsistent Site identity")
	}
	rawNodes, ok := requirement["targetNodeRefs"].([]any)
	if !ok || len(rawNodes) != 1 {
		return nil, "", "", errors.New("managed Cloud backup requires exactly one target node")
	}
	nodeRef, ok := rawNodes[0].(string)
	if !ok || strings.TrimSpace(nodeRef) == "" {
		return nil, "", "", errors.New("managed Cloud backup target node is invalid")
	}
	return requirement, siteRef, nodeRef, nil
}

func singleMapEntry(values map[string]any) (string, any, bool) {
	if len(values) != 1 {
		return "", nil, false
	}
	for key, value := range values {
		return key, value, true
	}
	return "", nil, false
}

func decodeObject(data []byte) (map[string]any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var object map[string]any
	if err := decoder.Decode(&object); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, errors.New("document contains trailing JSON")
	}
	return object, nil
}

func validDigest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+64 {
		return false
	}
	for _, character := range strings.TrimPrefix(value, "sha256:") {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
