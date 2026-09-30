package managedstackkit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/backupstore"
	"github.com/kombifyio/stackkits/pkg/backupbinding"
)

type renewalCustody struct{ *fakeRolloutCustody }

func (c renewalCustody) Evidence(context.Context, string, string) (backupstore.CustodyEvidence, error) {
	return c.evidence, nil
}

func TestRenewalPreservesAppliedCustodyAndRejectsRepositoryReplacement(t *testing.T) {
	for _, changed := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-repository", true: "replacement"}[changed], func(t *testing.T) {
			order := []string{}
			original := backupstore.CustodyEvidence{BindingEvidence: []byte("binding"), TargetEvidence: []byte("target"), AttestationEvidence: []byte("original"), ObservedAt: time.Now().Add(-time.Hour)}
			custody := renewalCustody{&fakeRolloutCustody{evidence: original, order: &order}}
			builder, err := NewRolloutInventory(custody, custody, testDigest("b"))
			if err != nil {
				t.Fatal(err)
			}
			builder.attest = func(_ context.Context, _ backupstore.Credentials, e backupstore.CustodyEvidence) (backupstore.CustodyEvidence, error) {
				e.AttestationEvidence = []byte("fresh")
				e.ObservedAt = time.Now()
				return e, nil
			}
			var plan map[string]any
			if err := json.Unmarshal(testResolvedPlan(t), &plan); err != nil {
				t.Fatal(err)
			}
			binding, _ := backupbinding.OpaqueReference("backup-target-binding", original.BindingEvidence)
			target, _ := backupbinding.OpaqueReference("backup-target", original.TargetEvidence)
			plan["externalBackupTargetBindings"] = map[string]any{"cloud": map[string]any{"offsite-object-backup": map[string]string{"bindingRef": binding, "backupTargetRef": target}}}
			raw, _ := json.Marshal(plan)
			if changed {
				custody.evidence.TargetEvidence = []byte("replacement")
			}
			result, err := builder.AttestBackupRenewal(t.Context(), RolloutInventoryRequest{TenantID: "tenant-a", StackID: "stack-a", ResolvedPlan: raw, StackKitsVersion: "v0.5.2", CandidateDigest: testDigest("a")})
			if changed {
				if err == nil || len(result) != 0 {
					t.Fatal("renewed replacement repository")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if string(custody.evidence.AttestationEvidence) != "original" || !custody.evidence.ObservedAt.Equal(original.ObservedAt) {
				t.Fatal("renewal replaced durable applied custody")
			}
			freshRef, _ := backupbinding.OpaqueReference("backup-custody-attestation", []byte("fresh"))
			if !strings.Contains(string(result), freshRef) || !strings.Contains(string(result), target) {
				t.Fatal("fresh proof lost original repository")
			}
		})
	}
}
