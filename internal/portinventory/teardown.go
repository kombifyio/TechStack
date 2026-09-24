package portinventory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
)

func normalizeTeardownRequest(request TeardownSnapshotRequest) (TeardownSnapshotRequest, error) {
	request.TenantID = strings.TrimSpace(request.TenantID)
	request.OwnerSubjectID = strings.TrimSpace(request.OwnerSubjectID)
	request.TechstackID = strings.TrimSpace(request.TechstackID)
	if request.TenantID == "" || request.OwnerSubjectID == "" || request.TechstackID == "" {
		return TeardownSnapshotRequest{}, ErrInvalidRequest
	}
	return request, nil
}

func sealTeardownSnapshot(request TeardownSnapshotRequest, generations []TeardownGeneration) (TeardownSnapshot, error) {
	request, err := normalizeTeardownRequest(request)
	if err != nil {
		return TeardownSnapshot{}, err
	}
	snapshot := TeardownSnapshot{
		APIVersion: TeardownSnapshotAPIVersion, TenantID: request.TenantID,
		OwnerSubjectID: request.OwnerSubjectID, TechstackID: request.TechstackID,
		Generations: cloneTeardownGenerations(generations),
	}
	if snapshot.Generations == nil {
		snapshot.Generations = []TeardownGeneration{}
	}
	if err := normalizeTeardownGenerations(&snapshot); err != nil {
		return TeardownSnapshot{}, err
	}
	snapshot.SnapshotDigest, err = teardownSnapshotDigest(snapshot)
	if err != nil {
		return TeardownSnapshot{}, err
	}
	return snapshot, nil
}

// ValidateTeardownSnapshot rejects caller-modified or cross-authority release
// batches before a database transaction can mutate claim state.
func ValidateTeardownSnapshot(snapshot TeardownSnapshot) error {
	if snapshot.APIVersion != TeardownSnapshotAPIVersion || snapshot.Generations == nil {
		return ErrTeardownSnapshotMismatch
	}
	request, err := normalizeTeardownRequest(TeardownSnapshotRequest{
		TenantID: snapshot.TenantID, OwnerSubjectID: snapshot.OwnerSubjectID, TechstackID: snapshot.TechstackID,
	})
	if err != nil || request.TenantID != snapshot.TenantID || request.OwnerSubjectID != snapshot.OwnerSubjectID || request.TechstackID != snapshot.TechstackID {
		return ErrTeardownSnapshotMismatch
	}
	normalized := snapshot
	normalized.Generations = cloneTeardownGenerations(snapshot.Generations)
	if err := normalizeTeardownGenerations(&normalized); err != nil || !equalTeardownGenerations(normalized.Generations, snapshot.Generations) {
		return ErrTeardownSnapshotMismatch
	}
	digest, err := teardownSnapshotDigest(snapshot)
	if err != nil || digest != snapshot.SnapshotDigest {
		return ErrTeardownSnapshotMismatch
	}
	return nil
}

func cloneTeardownGenerations(generations []TeardownGeneration) []TeardownGeneration {
	cloned := append([]TeardownGeneration(nil), generations...)
	for index := range cloned {
		cloned[index].NodeRefs = append([]string(nil), cloned[index].NodeRefs...)
	}
	return cloned
}

// DecodeTeardownSnapshot accepts the JSON-compatible shape stored in a
// durable job result and restores the typed, validated authority receipt.
func DecodeTeardownSnapshot(value any) (TeardownSnapshot, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return TeardownSnapshot{}, fmt.Errorf("%w: encode durable snapshot: %v", ErrTeardownSnapshotMismatch, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot TeardownSnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return TeardownSnapshot{}, fmt.Errorf("%w: decode durable snapshot: %v", ErrTeardownSnapshotMismatch, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return TeardownSnapshot{}, ErrTeardownSnapshotMismatch
	}
	if err := ValidateTeardownSnapshot(snapshot); err != nil {
		return TeardownSnapshot{}, err
	}
	return snapshot, nil
}

func normalizeTeardownGenerations(snapshot *TeardownSnapshot) error {
	seen := make(map[string]struct{}, len(snapshot.Generations))
	for index := range snapshot.Generations {
		generation := &snapshot.Generations[index]
		if err := normalizeGenerationRef(&generation.GenerationRef); err != nil ||
			generation.TenantID != snapshot.TenantID || generation.StackID != snapshot.TechstackID ||
			!validSHA256Digest(generation.ClaimSetDigest) || len(generation.NodeRefs) == 0 {
			return ErrTeardownSnapshotMismatch
		}
		for nodeIndex := range generation.NodeRefs {
			generation.NodeRefs[nodeIndex] = strings.TrimSpace(generation.NodeRefs[nodeIndex])
			if generation.NodeRefs[nodeIndex] == "" {
				return ErrTeardownSnapshotMismatch
			}
		}
		sort.Strings(generation.NodeRefs)
		for nodeIndex := 1; nodeIndex < len(generation.NodeRefs); nodeIndex++ {
			if generation.NodeRefs[nodeIndex] == generation.NodeRefs[nodeIndex-1] {
				return ErrTeardownSnapshotMismatch
			}
		}
		key := claimGenerationKey(generation.GenerationRef)
		if _, exists := seen[key]; exists {
			return ErrTeardownSnapshotMismatch
		}
		seen[key] = struct{}{}
	}
	sort.Slice(snapshot.Generations, func(i, j int) bool {
		return teardownGenerationKey(snapshot.Generations[i]) < teardownGenerationKey(snapshot.Generations[j])
	})
	return nil
}

func teardownSnapshotDigest(snapshot TeardownSnapshot) (string, error) {
	snapshot.SnapshotDigest = ""
	data, err := json.Marshal(snapshot)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(digest[:]), nil
}

func teardownGenerationKey(generation TeardownGeneration) string {
	return strings.Join([]string{
		generation.TenantID, generation.ServerID, fmt.Sprint(generation.ServerGeneration),
		generation.StackID, generation.ResolvedPlanHash,
	}, "\x00")
}

func equalTeardownGenerations(left, right []TeardownGeneration) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].GenerationRef != right[index].GenerationRef ||
			left[index].ClaimSetDigest != right[index].ClaimSetDigest ||
			!equalStrings(left[index].NodeRefs, right[index].NodeRefs) {
			return false
		}
	}
	return true
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func validSHA256Digest(value string) bool {
	if !strings.HasPrefix(value, "sha256:") || len(value) != len("sha256:")+sha256.Size*2 {
		return false
	}
	payload := strings.TrimPrefix(value, "sha256:")
	if payload != strings.ToLower(payload) {
		return false
	}
	_, err := hex.DecodeString(payload)
	return err == nil
}
