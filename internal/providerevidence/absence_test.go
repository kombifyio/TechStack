package providerevidence

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/providerexecutor/v1beta1"
)

func decommissionCommand() providerexecutor.Command {
	return providerexecutor.Command{
		TenantID:               "auth0|6a4957c8a4a480ce95c4290e",
		OperationID:            "op_60ca93505e4acdbd2b4474e4447e75fb",
		LeaseID:                "lease-53ce833757725c03e4fb93029c3073d4",
		LeaseRevision:          1,
		RuntimeServerID:        "server_395c714cbef340a18c96ca61",
		ResourceGenerationID:   "601dc403-e959-4802-b0b9-b68ea46bf67a",
		ProviderID:             "ionos",
		Operation:              providerexecutor.OperationDecommission,
		CommandDigest:          "sha256:" + strings.Repeat("a", 64),
		CapabilitySnapshotHash: "sha256:" + strings.Repeat("b", 64),
		ConnectionHash:         "sha256:" + strings.Repeat("c", 64),
		ExecutionProfileHash:   "sha256:" + strings.Repeat("d", 64),
		ResourceGraphHash:      "sha256:" + strings.Repeat("e", 64),
		RequestedAt:            time.Date(2026, 7, 27, 13, 9, 40, 0, time.UTC),
	}
}

func datacenterTarget() providerexecutor.ResourceTarget {
	return providerexecutor.ResourceTarget{
		BindingID:     "datacenter",
		Kind:          "ionos.datacenter",
		NativeRef:     "/cloudapi/v6/datacenters/bb797a26-88f8-4f83-b7dc-c18d3be29852",
		OwnershipHash: "sha256:" + strings.Repeat("f", 64),
		Disposition:   providerexecutor.DispositionDelete,
	}
}

func notFoundObservation() Observation {
	return Observation{
		Endpoint:    "https://api.ionos.com/cloudapi/v6/datacenters/bb797a26-88f8-4f83-b7dc-c18d3be29852",
		StatusCode:  404,
		CollectedAt: time.Date(2026, 7, 27, 13, 9, 45, 0, time.UTC),
	}
}

// The envelope must satisfy every field the wire contract binds, or the receipt
// is rejected and the decommission cannot converge. This is the shape the
// adapter shipped empty, which is why 0 of 115 capacity reservations had ever
// been released by 2026-07-27.
func TestAbsenceEvidenceBindsTheCommandAndResource(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()

	evidence, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatalf("buildAbsenceEvidence: %v", err)
	}
	if len(document) == 0 {
		t.Fatal("no absence document was produced")
	}
	for name, check := range map[string]struct{ got, want any }{
		"source":            {evidence.Source, providerexecutor.EvidenceSourceProviderAPI},
		"observation":       {evidence.Observation, providerexecutor.ObservationAbsent},
		"definitive":        {evidence.Definitive, true},
		"operation":         {evidence.OperationID, command.OperationID},
		"lease revision":    {evidence.LeaseRevision, command.LeaseRevision},
		"runtime server":    {evidence.RuntimeServerID, command.RuntimeServerID},
		"provider":          {evidence.ProviderID, command.ProviderID},
		"capability":        {evidence.CapabilitySnapshotHash, command.CapabilitySnapshotHash},
		"binding":           {evidence.BindingID, target.BindingID},
		"native ref":        {evidence.NativeRefHash, providerexecutor.ComputeNativeRefHash(target.NativeRef)},
		"connection":        {evidence.ConnectionHash, command.ConnectionHash},
		"execution profile": {evidence.ExecutionProfileHash, command.ExecutionProfileHash},
		"resource graph":    {evidence.ResourceGraphHash, command.ResourceGraphHash},
		"subject": {evidence.SubjectHash, providerexecutor.ComputeEvidenceSubjectHash(
			command, target, providerexecutor.ObservationAbsent)},
	} {
		if check.got != check.want {
			t.Errorf("%s = %v, want %v", name, check.got, check.want)
		}
	}
}

// Adapter-sourced absence is rejected by the wire contract itself
// (validation.go: "adapter evidence cannot prove resource absent"), so minting
// anything but provider_api evidence would produce a receipt that can never
// seal.
func TestAbsenceEvidenceIsAlwaysProviderAPISourced(t *testing.T) {
	evidence, _, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), notFoundObservation())
	if err != nil {
		t.Fatalf("buildAbsenceEvidence: %v", err)
	}
	if evidence.Source == providerexecutor.EvidenceSourceAdapter {
		t.Fatal("adapter-sourced absence evidence can never be sealed")
	}
}

// A provider answer that is not "gone" must not become an absence claim. This
// is the difference between proving a resource was deleted and merely failing
// to see it.
func TestOnlyANotFoundAnswerEstablishesAbsence(t *testing.T) {
	for _, status := range []int{200, 202, 401, 403, 429, 500, 502, 503} {
		observed := notFoundObservation()
		observed.StatusCode = status
		if _, _, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), observed); err == nil {
			t.Fatalf("provider status %d was accepted as proof of absence", status)
		}
	}
	for _, status := range []int{404, 410} {
		observed := notFoundObservation()
		observed.StatusCode = status
		if _, _, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), observed); err != nil {
			t.Fatalf("provider status %d should establish absence: %v", status, err)
		}
	}
}

// The endpoint is the audit trail: without it nobody can repeat the request
// that produced the not-found answer.
func TestAbsenceEvidenceRequiresTheProviderEndpoint(t *testing.T) {
	observed := notFoundObservation()
	observed.Endpoint = "   "
	if _, _, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), observed); err == nil {
		t.Fatal("evidence was minted without naming the provider endpoint that was read")
	}
}

// Two different resources must never share an evidence reference, or one
// observation would silently vouch for another.
func TestEachBindingGetsItsOwnReference(t *testing.T) {
	command := decommissionCommand()
	volume := datacenterTarget()
	volume.BindingID = "boot-volume"
	volume.NativeRef = "https://api.ionos.com/cloudapi/v6/datacenters/bb797a26-88f8-4f83-b7dc-c18d3be29852/servers/50aad2e8-9e6a-4899-816f-b762de8fe070/volumes/ff22228a-e2c8-4bc8-93bf-55bf7a2743ab"

	first, _, err := buildAbsenceEvidence(command, datacenterTarget(), notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := buildAbsenceEvidence(command, volume, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	if first.Ref == second.Ref || first.Digest == second.Digest ||
		first.AttestationRef == second.AttestationRef || first.AttestationDigest == second.AttestationDigest {
		t.Fatal("two bindings share an evidence identity")
	}
}

// The digest must change when the observation changes, otherwise a stored
// document could be swapped for a different one under the same digest.
func TestDigestCoversTheObservation(t *testing.T) {
	base, _, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	moved := notFoundObservation()
	moved.Endpoint = "https://api.ionos.com/cloudapi/v6/datacenters/00000000-0000-0000-0000-000000000000"
	other, _, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), moved)
	if err != nil {
		t.Fatal(err)
	}
	if base.Digest == other.Digest {
		t.Fatal("the digest does not cover the observed endpoint")
	}
}

// Replaying a genuine observation onto a different command is the attack the
// subject hash exists to stop.
func TestSubjectHashSeparatesCommands(t *testing.T) {
	first, _, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	other := decommissionCommand()
	other.OperationID = "op_0000000000000000000000000000000f"
	second, _, err := buildAbsenceEvidence(other, datacenterTarget(), notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	if first.SubjectHash == second.SubjectHash {
		t.Fatal("two commands produced the same evidence subject")
	}
}

// The store being unreachable must never read as a proven absence.
func TestNilRecorderAndVerifierRefuseRatherThanPass(t *testing.T) {
	var recorder *Recorder
	if _, err := recorder.Record(t.Context(), decommissionCommand(), datacenterTarget(), notFoundObservation()); !errors.Is(err, ErrAbsenceObservationUnavailable) {
		t.Fatalf("nil recorder error = %v, want ErrAbsenceObservationUnavailable", err)
	}
	var verifier *Verifier
	err := verifier.VerifyEvidence(t.Context(), decommissionCommand(), datacenterTarget(),
		providerexecutor.Evidence{Observation: providerexecutor.ObservationAbsent})
	if !errors.Is(err, ErrAbsenceObservationUnavailable) {
		t.Fatalf("nil verifier error = %v, want ErrAbsenceObservationUnavailable", err)
	}
}

// A stored row must only vouch for the exact envelope it was recorded for.
func TestStoredObservationRejectsAMismatchedEnvelope(t *testing.T) {
	evidence, document, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	stored := storedObservation{
		OperationID:       evidence.OperationID,
		BindingID:         evidence.BindingID,
		EvidenceDigest:    evidence.Digest,
		AttestationRef:    evidence.AttestationRef,
		AttestationDigest: evidence.AttestationDigest,
		CollectedAt:       evidence.CollectedAt,
		Document:          document,
	}
	if err := stored.matches(evidence); err != nil {
		t.Fatalf("a faithful record was rejected: %v", err)
	}
	for name, mutate := range map[string]func(*providerexecutor.Evidence){
		"attestation ref": func(e *providerexecutor.Evidence) { e.AttestationRef = "provider-attestation://elsewhere.example/x/y" },
		"collected at":    func(e *providerexecutor.Evidence) { e.CollectedAt = e.CollectedAt.Add(time.Second) },
	} {
		t.Run(name, func(t *testing.T) {
			forged := evidence
			mutate(&forged)
			if err := stored.matches(forged); !errors.Is(err, ErrAbsenceObservationMismatch) {
				t.Fatalf("a forged %s was accepted: %v", name, err)
			}
		})
	}
}

// A retried teardown re-reads the provider and stamps a new collection time, so
// the freshly built document -- and its digest -- differ from the observation
// already on record. Comparing them failed every retry with "evidence digest",
// which the adapter then reported as a retryable transient teardown with no
// resources attached, and the operation could never converge.
//
// The first observation is the proof: absence was established then, and
// observing it again does not invalidate it.
func TestARetryAdoptsTheFirstRecordedObservation(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	first, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	stored := storedObservation{
		OperationID: first.OperationID, BindingID: first.BindingID,
		EvidenceDigest: first.Digest, AttestationRef: first.AttestationRef,
		AttestationDigest: first.AttestationDigest, SubjectHash: first.SubjectHash,
		NativeRefHash: first.NativeRefHash, CollectedAt: first.CollectedAt,
		Document: document,
	}

	later := notFoundObservation()
	later.CollectedAt = later.CollectedAt.Add(90 * time.Second)
	retry, _, err := buildAbsenceEvidence(command, target, later)
	if err != nil {
		t.Fatal(err)
	}
	if retry.Digest == first.Digest {
		t.Fatal("the retry produced an identical digest; this test no longer exercises the conflict")
	}

	adopted, err := adoptStoredObservation(retry, stored, command, target)
	if err != nil {
		t.Fatalf("the retry was refused instead of adopting the recorded observation: %v", err)
	}
	if adopted.Digest != stored.EvidenceDigest || !adopted.CollectedAt.Equal(stored.CollectedAt) {
		t.Fatalf("adopted = %s@%s, want the stored observation %s@%s",
			adopted.Digest, adopted.CollectedAt, stored.EvidenceDigest, stored.CollectedAt)
	}
	if err := stored.matches(adopted); err != nil {
		t.Fatalf("the adopted envelope no longer matches its own record: %v", err)
	}
}

// Adopting must never widen what the evidence asserts: the fields that bind the
// claim to this command stay derived and are re-checked.
func TestAdoptingKeepsTheCommandBinding(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	candidate, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	stored := storedObservation{
		OperationID: candidate.OperationID, BindingID: candidate.BindingID,
		EvidenceDigest: candidate.Digest, AttestationRef: candidate.AttestationRef,
		AttestationDigest: candidate.AttestationDigest, SubjectHash: candidate.SubjectHash,
		NativeRefHash: candidate.NativeRefHash, CollectedAt: candidate.CollectedAt,
		Document: document,
	}

	adopted, err := adoptStoredObservation(candidate, stored, command, target)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.SubjectHash != candidate.SubjectHash || adopted.NativeRefHash != candidate.NativeRefHash ||
		adopted.OperationID != command.OperationID || adopted.Source != providerexecutor.EvidenceSourceProviderAPI ||
		adopted.Observation != providerexecutor.ObservationAbsent || !adopted.Definitive {
		t.Fatalf("adopting changed a binding field: %+v", adopted)
	}
}

// A record belonging to another command or resource is a genuine conflict, not
// something to adopt.
func TestAdoptingRefusesAForeignRecord(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	candidate, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	base := storedObservation{
		OperationID: candidate.OperationID, BindingID: candidate.BindingID,
		SubjectHash: candidate.SubjectHash, NativeRefHash: candidate.NativeRefHash,
		Document: document,
	}
	for name, mutate := range map[string]func(*storedObservation){
		"operation":  func(s *storedObservation) { s.OperationID = "op_0000000000000000000000000000000f" },
		"binding":    func(s *storedObservation) { s.BindingID = "boot-volume" },
		"subject":    func(s *storedObservation) { s.SubjectHash = "sha256:" + strings.Repeat("0", 64) },
		"native ref": func(s *storedObservation) { s.NativeRefHash = "sha256:" + strings.Repeat("1", 64) },
	} {
		t.Run(name, func(t *testing.T) {
			foreign := base
			mutate(&foreign)
			if _, err := adoptStoredObservation(candidate, foreign, command, target); !errors.Is(err, ErrAbsenceObservationMismatch) {
				t.Fatalf("a foreign %s was adopted: %v", name, err)
			}
		})
	}
}

// The document lives in a jsonb column, and jsonb does not preserve the bytes
// it was given: it reparses and re-serializes. A digest taken over the JSON text
// could therefore never be recomputed from the stored row, and the verifier
// rejected every absence claim it had itself recorded moments earlier -- live on
// 2026-07-27, one rung past the recorder fix.
//
// Re-encoding with different whitespace stands in for that round trip.
func TestDigestSurvivesAJSONBRoundTrip(t *testing.T) {
	evidence, document, err := buildAbsenceEvidence(decommissionCommand(), datacenterTarget(), notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}

	var parsed [][2]string
	if err := json.Unmarshal(document, &parsed); err != nil {
		t.Fatalf("the stored document shape does not survive parsing: %v", err)
	}
	reencoded, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if string(reencoded) == string(document) {
		t.Fatal("re-encoding produced identical bytes; this test no longer exercises the round trip")
	}

	roundTripped, err := decodeDocumentPairs(reencoded)
	if err != nil {
		t.Fatalf("decodeDocumentPairs: %v", err)
	}
	if got := documentDigest(roundTripped); got != evidence.Digest {
		t.Fatalf("digest after round trip = %s, want %s", got, evidence.Digest)
	}
}

// The framing must stay injective: two different pair lists cannot collide by
// concatenating their way to the same payload.
func TestDigestFramingResistsConcatenationCollisions(t *testing.T) {
	first := documentDigest([][2]string{{"a", "bc"}, {"d", "e"}})
	second := documentDigest([][2]string{{"a", "b"}, {"cd", "e"}})
	if first == second {
		t.Fatal("two distinct documents share a digest")
	}
}

// A document that is not the recorded shape must be refused rather than
// silently digesting to something.
func TestUnreadableStoredDocumentIsRefused(t *testing.T) {
	for name, payload := range map[string]string{
		"not json":    `{`,
		"wrong shape": `{"version":"x"}`,
		"empty":       `[]`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := decodeDocumentPairs([]byte(payload)); !errors.Is(err, ErrAbsenceObservationMismatch) {
				t.Fatalf("payload %q was accepted: %v", payload, err)
			}
		})
	}
}

// A digest column is an index, not an authority.
//
// Trusting it made any record written under a superseded digest scheme
// permanently unusable: the adopted envelope carried the stored value while the
// verifier recomputed the current one, so the absence receipt could never be
// appended and the operation never terminalized -- which also meant no retry
// could replace it. Live on 2026-07-27 exactly one such row stranded its lease
// for good.
//
// Deriving the digest from the document makes such a record verifiable again,
// because both sides recompute the same value from the same bytes.
func TestASupersededDigestColumnDoesNotStrandTheRecord(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	candidate, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	legacy := storedObservation{
		OperationID: candidate.OperationID, BindingID: candidate.BindingID,
		// What an older release wrote: a digest over the raw JSON bytes.
		EvidenceDigest:    "sha256:" + strings.Repeat("9", 64),
		AttestationRef:    candidate.AttestationRef,
		AttestationDigest: "sha256:" + strings.Repeat("8", 64),
		SubjectHash:       candidate.SubjectHash,
		NativeRefHash:     candidate.NativeRefHash,
		CollectedAt:       candidate.CollectedAt,
		Document:          document,
	}

	adopted, err := adoptStoredObservation(candidate, legacy, command, target)
	if err != nil {
		t.Fatalf("a record with a superseded digest column was refused: %v", err)
	}
	pairs, err := decodeDocumentPairs(legacy.Document)
	if err != nil {
		t.Fatal(err)
	}
	if adopted.Digest != documentDigest(pairs) {
		t.Fatal("the adopted digest was not derived from the stored document")
	}
	if adopted.Digest == legacy.EvidenceDigest {
		t.Fatal("the stored digest column was trusted")
	}
}

// Tamper evidence must survive the change: editing the document still breaks
// verification, because the digest both sides compute changes with it.
func TestAnEditedDocumentStillBreaksTheDigest(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	candidate, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	honest := storedObservation{
		OperationID: candidate.OperationID, BindingID: candidate.BindingID,
		AttestationRef: candidate.AttestationRef, SubjectHash: candidate.SubjectHash,
		NativeRefHash: candidate.NativeRefHash, CollectedAt: candidate.CollectedAt,
		Document: document,
	}
	adopted, err := adoptStoredObservation(candidate, honest, command, target)
	if err != nil {
		t.Fatal(err)
	}

	tampered := honest
	tampered.Document = []byte(strings.Replace(string(document), `"404"`, `"200"`, 1))
	if string(tampered.Document) == string(document) {
		t.Fatal("the document was not actually edited; this test would pass vacuously")
	}
	pairs, err := decodeDocumentPairs(tampered.Document)
	if err != nil {
		t.Fatal(err)
	}
	if documentDigest(pairs) == adopted.Digest {
		t.Fatal("editing the observed status did not change the digest")
	}
}

// Both digests are derived from the stored document, so neither may be compared
// against its column: a record written under a superseded scheme would be
// stranded forever. This was fixed for the evidence digest and missed one field
// over, and the live teardown then failed with "attestation digest" for exactly
// the same reason.
func TestNoDerivedDigestIsComparedAgainstItsColumn(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	candidate, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	legacy := storedObservation{
		OperationID: candidate.OperationID, BindingID: candidate.BindingID,
		EvidenceDigest:    "sha256:" + strings.Repeat("9", 64),
		AttestationDigest: "sha256:" + strings.Repeat("8", 64),
		AttestationRef:    candidate.AttestationRef,
		SubjectHash:       candidate.SubjectHash,
		NativeRefHash:     candidate.NativeRefHash,
		CollectedAt:       candidate.CollectedAt,
		Document:          document,
	}

	adopted, err := adoptStoredObservation(candidate, legacy, command, target)
	if err != nil {
		t.Fatalf("a record with superseded digest columns was refused: %v", err)
	}
	if err := legacy.matches(adopted); err != nil {
		t.Fatalf("the adopted envelope was rejected against its own record: %v", err)
	}
	if adopted.AttestationDigest == legacy.AttestationDigest {
		t.Fatal("the stored attestation digest column was trusted")
	}
}

// Tamper evidence for the attestation: it must still change with the document.
func TestAttestationDigestCoversTheDocument(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	candidate, document, err := buildAbsenceEvidence(command, target, notFoundObservation())
	if err != nil {
		t.Fatal(err)
	}
	pairs, err := decodeDocumentPairs(document)
	if err != nil {
		t.Fatal(err)
	}
	tampered := append([][2]string{{"attestation_ref", candidate.AttestationRef}}, pairs...)
	tampered[len(tampered)-1] = [2]string{tampered[len(tampered)-1][0], "1999-01-01T00:00:00Z"}
	if documentDigest(tampered) == candidate.AttestationDigest {
		t.Fatal("editing the document did not change the attestation digest")
	}
}

// A successful list response proves absence only for its exact audited target.
func TestProxmoxListAbsenceRequiresExactTarget(t *testing.T) {
	command, target := decommissionCommand(), datacenterTarget()
	command.ProviderID = "proxmox"
	observed := notFoundObservation()
	observed.StatusCode = 200
	observed.AuthoritativeListAbsent = target.NativeRef
	if _, _, err := buildAbsenceEvidence(command, target, observed); err != nil {
		t.Fatal(err)
	}
	observed.AuthoritativeListAbsent = "another-node/1100"
	if _, _, err := buildAbsenceEvidence(command, target, observed); err == nil {
		t.Fatal("unrelated list absence was accepted")
	}
}
