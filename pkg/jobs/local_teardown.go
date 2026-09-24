package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/stackkits/pkg/workloadremoval"
)

const (
	LocalStackKitTeardownAuthorityResultField = "local_stackkit_teardown_authority"
	LocalStackKitRemovalEvidenceResultField   = "local_stackkit_removal_evidence"
	localStackKitTeardownAuthorityVersion     = "techstack.local-stackkit-teardown-authority/v2"
)

// LocalStackKitTeardownAuthority is the durable, secret-free correlation
// between one completed StackKits Apply and the workload removals required
// before Techstack may release host-port claims for that exact plan.
type LocalStackKitTeardownAuthority struct {
	SchemaVersion        string                                 `json:"schema_version"`
	TechstackID          string                                 `json:"techstack_id"`
	StackKitInstanceID   string                                 `json:"stackkit_instance_id"`
	TenantID             string                                 `json:"tenant_id"`
	OwnerID              string                                 `json:"owner_id"`
	StackKit             string                                 `json:"stackkit"`
	PlanHash             string                                 `json:"plan_hash"`
	AppliedRequestDigest string                                 `json:"applied_request_digest"`
	Servers              []LocalStackKitTeardownServerAuthority `json:"servers"`
}

type LocalStackKitTeardownServerAuthority struct {
	ServerID  string                                   `json:"server_id"`
	AgentID   string                                   `json:"agent_id"`
	Workloads []LocalStackKitTeardownWorkloadAuthority `json:"workloads"`
}

type LocalStackKitTeardownWorkloadAuthority struct {
	WorkloadRef         string   `json:"workload_ref"`
	RequirementID       string   `json:"requirement_id"`
	InstanceRef         string   `json:"instance_ref"`
	RuntimeOwnerRef     string   `json:"runtime_owner_ref"`
	SiteRef             string   `json:"site_ref"`
	NodeRef             string   `json:"node_ref"`
	ExecutionChannelRef string   `json:"execution_channel_ref"`
	ArtifactDigests     []string `json:"artifact_digests"`
}

type LocalStackKitTeardownNodeBinding struct {
	NodeRef  string
	ServerID string
	AgentID  string
}

func BuildLocalStackKitTeardownAuthority(techstackID, stackKitInstanceID, tenantID, ownerID string, runtimeSummary map[string]any, snapshot portinventory.TeardownSnapshot, bindings []LocalStackKitTeardownNodeBinding) (LocalStackKitTeardownAuthority, error) {
	techstackID, stackKitInstanceID = strings.TrimSpace(techstackID), strings.TrimSpace(stackKitInstanceID)
	tenantID, ownerID = strings.TrimSpace(tenantID), strings.TrimSpace(ownerID)
	if err := portinventory.ValidateTeardownSnapshot(snapshot); err != nil || snapshot.TechstackID != techstackID || snapshot.TenantID != tenantID || snapshot.OwnerSubjectID != ownerID {
		return LocalStackKitTeardownAuthority{}, errors.New("local StackKits teardown snapshot does not match the stack authority")
	}
	outputs := mapFromInterface(runtimeSummary[metadataKeyStackKitOutputs])
	apply := mapFromInterface(outputs["apply"])
	servers, err := appliedWorkloadServers(apply["appliedWorkloads"], snapshot, bindings)
	if err != nil {
		return LocalStackKitTeardownAuthority{}, err
	}
	authority := LocalStackKitTeardownAuthority{
		SchemaVersion: localStackKitTeardownAuthorityVersion,
		TechstackID:   techstackID, StackKitInstanceID: stackKitInstanceID,
		TenantID: tenantID, OwnerID: ownerID,
		StackKit:             strings.TrimSpace(stringFromInterface(runtimeSummary[metadataKeyStackKitCatalogRef])),
		PlanHash:             strings.TrimSpace(stringFromInterface(apply["planHash"])),
		AppliedRequestDigest: strings.TrimSpace(stringFromInterface(apply["appliedRequestDigest"])),
		Servers:              servers,
	}
	if err := authority.Validate(); err != nil {
		return LocalStackKitTeardownAuthority{}, err
	}
	return authority, nil
}

func DecodeLocalStackKitTeardownAuthority(value any) (LocalStackKitTeardownAuthority, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return LocalStackKitTeardownAuthority{}, fmt.Errorf("encode local StackKits teardown authority: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var authority LocalStackKitTeardownAuthority
	if err := decoder.Decode(&authority); err != nil {
		return LocalStackKitTeardownAuthority{}, fmt.Errorf("decode local StackKits teardown authority: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return LocalStackKitTeardownAuthority{}, errors.New("local StackKits teardown authority contains trailing data")
	}
	if err := authority.Validate(); err != nil {
		return LocalStackKitTeardownAuthority{}, err
	}
	return authority, nil
}

func (authority LocalStackKitTeardownAuthority) Validate() error {
	for _, value := range []string{
		authority.TechstackID, authority.StackKitInstanceID, authority.TenantID,
		authority.OwnerID, authority.StackKit,
	} {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.New("local StackKits teardown authority identity is incomplete")
		}
	}
	if authority.SchemaVersion != localStackKitTeardownAuthorityVersion ||
		!resolvedPlanHashPattern.MatchString(authority.PlanHash) ||
		!resolvedPlanHashPattern.MatchString(authority.AppliedRequestDigest) || len(authority.Servers) == 0 {
		return errors.New("local StackKits teardown authority Apply identity is invalid")
	}
	previousServer := ""
	seenPlacements := map[string]struct{}{}
	for _, server := range authority.Servers {
		if server.ServerID == "" || server.AgentID == "" || server.ServerID != strings.TrimSpace(server.ServerID) ||
			server.AgentID != strings.TrimSpace(server.AgentID) || server.ServerID <= previousServer || len(server.Workloads) == 0 {
			return errors.New("local StackKits teardown authority server partitions are not canonical")
		}
		previousServer = server.ServerID
		previousWorkload := ""
		for _, workload := range server.Workloads {
			key, err := validateLocalTeardownWorkload(workload)
			if err != nil || key <= previousWorkload {
				return errors.New("local StackKits teardown authority workloads are not canonical")
			}
			if _, duplicate := seenPlacements[key]; duplicate {
				return errors.New("local StackKits teardown authority repeats a workload placement")
			}
			seenPlacements[key] = struct{}{}
			previousWorkload = key
		}
	}
	return nil
}

func validateLocalTeardownWorkload(workload LocalStackKitTeardownWorkloadAuthority) (string, error) {
	for _, value := range []string{workload.WorkloadRef, workload.RequirementID, workload.InstanceRef, workload.RuntimeOwnerRef,
		workload.SiteRef, workload.NodeRef, workload.ExecutionChannelRef} {
		if value == "" || value != strings.TrimSpace(value) {
			return "", errors.New("local StackKits teardown workload identity is incomplete")
		}
	}
	if workload.WorkloadRef != strings.ToLower(workload.WorkloadRef) || len(workload.ArtifactDigests) == 0 || !sort.StringsAreSorted(workload.ArtifactDigests) {
		return "", errors.New("local StackKits teardown workload identity is invalid")
	}
	for index, digest := range workload.ArtifactDigests {
		if !resolvedPlanHashPattern.MatchString(digest) || (index > 0 && digest == workload.ArtifactDigests[index-1]) {
			return "", errors.New("local StackKits teardown workload artifacts are invalid")
		}
	}
	return strings.Join([]string{workload.WorkloadRef, workload.RequirementID, workload.InstanceRef, workload.NodeRef}, "\x00"), nil
}

func appliedWorkloadServers(value any, snapshot portinventory.TeardownSnapshot, bindings []LocalStackKitTeardownNodeBinding) ([]LocalStackKitTeardownServerAuthority, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 {
		return nil, errors.New("local StackKits teardown authority has no applied workloads")
	}
	byNode := make(map[string]LocalStackKitTeardownNodeBinding, len(bindings))
	for _, binding := range bindings {
		binding.NodeRef = strings.TrimSpace(binding.NodeRef)
		binding.ServerID = strings.TrimSpace(binding.ServerID)
		binding.AgentID = strings.TrimSpace(binding.AgentID)
		if binding.NodeRef == "" || binding.ServerID == "" || binding.AgentID == "" {
			return nil, errors.New("local StackKits teardown node binding is incomplete")
		}
		if existing, duplicate := byNode[binding.NodeRef]; duplicate && existing != binding {
			return nil, errors.New("local StackKits teardown node binding is ambiguous")
		}
		byNode[binding.NodeRef] = binding
	}
	for _, generation := range snapshot.Generations {
		for _, nodeRef := range generation.NodeRefs {
			binding, exists := byNode[nodeRef]
			if !exists || binding.ServerID != generation.ServerID {
				return nil, errors.New("local StackKits teardown snapshot has no exact node/server custody")
			}
		}
	}
	servers := map[string]*LocalStackKitTeardownServerAuthority{}
	for _, item := range items {
		workload := mapFromInterface(item)
		base := LocalStackKitTeardownWorkloadAuthority{
			WorkloadRef: strings.TrimSpace(stringFromInterface(workload["workloadRef"])), RequirementID: strings.TrimSpace(stringFromInterface(workload["requirementId"])),
			InstanceRef: strings.TrimSpace(stringFromInterface(workload["instanceRef"])), RuntimeOwnerRef: strings.TrimSpace(stringFromInterface(workload["runtimeOwnerRef"])),
		}
		artifacts, artifactErr := appliedWorkloadArtifacts(workload["artifacts"])
		placements, placementErr := appliedWorkloadPlacements(workload["placements"])
		if artifactErr != nil || placementErr != nil || len(artifacts) == 0 || len(placements) == 0 {
			return nil, errors.New("local StackKits teardown authority has an invalid applied workload")
		}
		for _, placement := range placements {
			binding, exists := byNode[placement.NodeRef]
			if !exists {
				return nil, errors.New("local StackKits teardown workload has no exact node custody")
			}
			partition := servers[binding.ServerID]
			if partition == nil {
				partition = &LocalStackKitTeardownServerAuthority{ServerID: binding.ServerID, AgentID: binding.AgentID}
				servers[binding.ServerID] = partition
			} else if partition.AgentID != binding.AgentID {
				return nil, errors.New("local StackKits teardown server has ambiguous agent custody")
			}
			candidate := base
			candidate.SiteRef, candidate.NodeRef, candidate.ExecutionChannelRef = placement.SiteRef, placement.NodeRef, placement.ExecutionChannelRef
			candidate.ArtifactDigests = append([]string(nil), artifacts...)
			partition.Workloads = append(partition.Workloads, candidate)
		}
	}
	result := make([]LocalStackKitTeardownServerAuthority, 0, len(servers))
	for _, server := range servers {
		sort.Slice(server.Workloads, func(i, j int) bool {
			left, _ := validateLocalTeardownWorkload(server.Workloads[i])
			right, _ := validateLocalTeardownWorkload(server.Workloads[j])
			return left < right
		})
		result = append(result, *server)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ServerID < result[j].ServerID })
	return result, nil
}

func appliedWorkloadArtifacts(value any) ([]string, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 {
		return nil, errors.New("applied workload has no artifacts")
	}
	digests := make([]string, 0, len(items))
	for _, item := range items {
		digest := strings.TrimSpace(stringFromInterface(mapFromInterface(item)["digest"]))
		if !resolvedPlanHashPattern.MatchString(digest) {
			return nil, errors.New("applied workload has an invalid artifact digest")
		}
		digests = append(digests, digest)
	}
	sort.Strings(digests)
	for index := 1; index < len(digests); index++ {
		if digests[index] == digests[index-1] {
			return nil, errors.New("applied workload repeats an artifact digest")
		}
	}
	return digests, nil
}

func appliedWorkloadPlacements(value any) ([]localExecutionBinding, error) {
	items, ok := value.([]interface{})
	if !ok || len(items) == 0 {
		return nil, errors.New("applied workload has no placements")
	}
	placements := make([]localExecutionBinding, 0, len(items))
	for _, item := range items {
		mapped := mapFromInterface(item)
		placement := localExecutionBinding{
			SiteRef: strings.TrimSpace(stringFromInterface(mapped["siteRef"])), NodeRef: strings.TrimSpace(stringFromInterface(mapped["nodeRef"])),
			ExecutionChannelRef: strings.TrimSpace(stringFromInterface(mapped["executionChannelRef"])),
		}
		if placement.SiteRef == "" || placement.NodeRef == "" || placement.ExecutionChannelRef == "" {
			return nil, errors.New("applied workload has an incomplete placement")
		}
		placements = append(placements, placement)
	}
	sort.Slice(placements, func(i, j int) bool {
		return strings.Join([]string{placements[i].SiteRef, placements[i].NodeRef, placements[i].ExecutionChannelRef}, "\x00") <
			strings.Join([]string{placements[j].SiteRef, placements[j].NodeRef, placements[j].ExecutionChannelRef}, "\x00")
	})
	for index := 1; index < len(placements); index++ {
		if placements[index] == placements[index-1] {
			return nil, errors.New("applied workload repeats a placement")
		}
	}
	return placements, nil
}

func removeLocalStackKitWorkloads(ctx context.Context, cfg *ProvisionConfig, job *Job, snapshot portinventory.TeardownSnapshot) error {
	if cfg == nil || cfg.StackKitCommander == nil {
		return errors.New("typed StackKits dispatcher is not configured for local teardown")
	}
	if _, err := boundLocalStackKitTeardownAuthority(job, snapshot); err != nil {
		return err
	}
	release, err := configuredTargetStackKitRelease()
	if err != nil {
		return fmt.Errorf("resolve pinned StackKits release for local teardown: %w", err)
	}
	if release == nil {
		return errors.New("pinned StackKits release is missing for local teardown")
	}
	return removeLocalStackKitWorkloadsWithRelease(ctx, cfg, job, snapshot, *release)
}

func removeLocalStackKitWorkloadsWithRelease(ctx context.Context, cfg *ProvisionConfig, job *Job, snapshot portinventory.TeardownSnapshot, release stackkitrelease.Release) error {
	if cfg == nil || cfg.StackKitCommander == nil {
		return errors.New("typed StackKits dispatcher is not configured for local teardown")
	}
	authority, err := boundLocalStackKitTeardownAuthority(job, snapshot)
	if err != nil {
		return err
	}
	if persisted, exists := job.Snapshot().Result[LocalStackKitRemovalEvidenceResultField]; exists {
		return validatePersistedLocalRemovalEvidence(authority, persisted)
	}
	evidenceSet := make([]interface{}, 0)
	commandIndex := 0
	for _, server := range authority.Servers {
		for _, workload := range server.Workloads {
			commandIndex++
			request, err := NormalizeStackKitLifecycleRequest(StackKitLifecycleRequest{
				StackID: authority.TechstackID, StackKitInstanceID: authority.StackKitInstanceID,
				TenantID: authority.TenantID, OwnerID: authority.OwnerID, AgentID: server.AgentID,
				Operation: StackKitLifecycleRemove, OwnerApproved: true, StackKit: authority.StackKit,
				WorkloadRef: workload.WorkloadRef,
			})
			if err != nil {
				return err
			}
			command, err := stackKitLifecycleCommand(fmt.Sprintf("%s-remove-%d", job.ID, commandIndex), request, release)
			if err != nil {
				return err
			}
			command.LocalSiteRef, command.LocalNodeRef, command.LocalExecutionChannelRef = workload.SiteRef, workload.NodeRef, workload.ExecutionChannelRef
			result, err := sendStackKitCommandBoundedForTenant(ctx, cfg.StackKitCommander, authority.TenantID, server.AgentID, command)
			if err != nil {
				return fmt.Errorf("typed StackKits remove dispatch failed: %w", err)
			}
			normalized, err := normalizeStackKitLifecycleResult(request, result)
			if err != nil {
				return err
			}
			if result == nil || !result.Success {
				return fmt.Errorf("StackKits remove failed for workload %q", workload.WorkloadRef)
			}
			persisted := mapFromInterface(normalized["removal_evidence"])
			data, err := json.Marshal(persisted)
			if err != nil {
				return err
			}
			evidence, err := workloadremoval.ParseEvidence(data)
			if err != nil {
				return err
			}
			if !localRemovalEvidenceMatches(authority, workload, evidence) {
				return errors.New("StackKits removal evidence does not match the durable Apply authority")
			}
			evidenceSet = append(evidenceSet, persisted)
		}
	}
	job.mutateResult(func(result map[string]interface{}) {
		result[LocalStackKitRemovalEvidenceResultField] = evidenceSet
	})
	return nil
}

func validatePersistedLocalRemovalEvidence(authority LocalStackKitTeardownAuthority, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("encode durable StackKits removal evidence: %w", err)
	}
	var entries []json.RawMessage
	if err := json.Unmarshal(data, &entries); err != nil {
		return fmt.Errorf("decode durable StackKits removal evidence: %w", err)
	}
	expected := make(map[string]LocalStackKitTeardownWorkloadAuthority)
	for _, server := range authority.Servers {
		for _, workload := range server.Workloads {
			expected[localTeardownWorkloadKey(workload)] = workload
		}
	}
	if len(entries) != len(expected) {
		return errors.New("durable StackKits removal evidence does not cover every workload placement")
	}
	for _, entry := range entries {
		evidence, err := workloadremoval.ParseEvidence(entry)
		if err != nil {
			return fmt.Errorf("validate durable StackKits removal evidence: %w", err)
		}
		key := strings.Join([]string{evidence.Authority.WorkloadRef, evidence.Authority.RequirementID, evidence.Authority.InstanceRef, evidence.Authority.NodeRef}, "\x00")
		workload, exists := expected[key]
		if !exists || !localRemovalEvidenceMatches(authority, workload, evidence) {
			return errors.New("durable StackKits removal evidence does not match the Apply authority")
		}
		delete(expected, key)
	}
	if len(expected) != 0 {
		return errors.New("durable StackKits removal evidence is incomplete")
	}
	return nil
}

func localTeardownWorkloadKey(workload LocalStackKitTeardownWorkloadAuthority) string {
	return strings.Join([]string{workload.WorkloadRef, workload.RequirementID, workload.InstanceRef, workload.NodeRef}, "\x00")
}

func localRemovalEvidenceMatches(authority LocalStackKitTeardownAuthority, workload LocalStackKitTeardownWorkloadAuthority, evidence workloadremoval.Evidence) bool {
	if evidence.Authority.PlanHash != authority.PlanHash || evidence.Authority.AppliedRequestDigest != authority.AppliedRequestDigest ||
		evidence.Authority.WorkloadRef != workload.WorkloadRef || evidence.Authority.RequirementID != workload.RequirementID ||
		evidence.Authority.InstanceRef != workload.InstanceRef || evidence.Authority.RuntimeOwnerRef != workload.RuntimeOwnerRef ||
		evidence.Authority.SiteRef != workload.SiteRef || evidence.Authority.NodeRef != workload.NodeRef ||
		evidence.Authority.ExecutionChannelRef != workload.ExecutionChannelRef {
		return false
	}
	for _, digest := range workload.ArtifactDigests {
		if evidence.Authority.ArtifactDigest == digest {
			return true
		}
	}
	return false
}

func boundLocalStackKitTeardownAuthority(job *Job, snapshot portinventory.TeardownSnapshot) (LocalStackKitTeardownAuthority, error) {
	if job == nil {
		return LocalStackKitTeardownAuthority{}, errors.New("local StackKits teardown job is missing")
	}
	rawAuthority, ok := job.Snapshot().Result[LocalStackKitTeardownAuthorityResultField]
	if !ok {
		return LocalStackKitTeardownAuthority{}, errors.New("durable local StackKits teardown authority is missing")
	}
	authority, err := DecodeLocalStackKitTeardownAuthority(rawAuthority)
	if err != nil {
		return LocalStackKitTeardownAuthority{}, err
	}
	if authority.TechstackID != job.TargetID || authority.TenantID != payloadString(job.Payload, tenantIDField) ||
		authority.OwnerID != payloadString(job.Payload, "owner_id") {
		return LocalStackKitTeardownAuthority{}, errors.New("local StackKits teardown authority does not match the destroy job")
	}
	servers := make(map[string]LocalStackKitTeardownServerAuthority, len(authority.Servers))
	for _, server := range authority.Servers {
		servers[server.ServerID] = server
	}
	for _, generation := range snapshot.Generations {
		server, exists := servers[generation.ServerID]
		if generation.ResolvedPlanHash != authority.PlanHash || !exists {
			return LocalStackKitTeardownAuthority{}, errors.New("local StackKits teardown authority does not cover every port-claim generation")
		}
		for _, nodeRef := range generation.NodeRefs {
			covered := false
			for _, workload := range server.Workloads {
				if workload.NodeRef == nodeRef {
					covered = true
					break
				}
			}
			if !covered {
				return LocalStackKitTeardownAuthority{}, errors.New("local StackKits teardown authority does not cover every port-claim node")
			}
		}
	}
	return authority, nil
}
