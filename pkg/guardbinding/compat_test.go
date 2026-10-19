package guardbinding

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
)

// contractVectors is the frozen G01 vector file shared by the control-plane
// and Guard sides of the binding contract.
type contractVectors struct {
	CommandRaw  string          `json:"command_raw"`
	BaseBinding json.RawMessage `json:"base_binding"`
	Expectation struct {
		TenantID          string `json:"tenant_id"`
		WorkerID          string `json:"worker_id"`
		ServerID          string `json:"server_id"`
		HighestGeneration int64  `json:"highest_generation"`
	} `json:"guard_expectation"`
	Now       time.Time `json:"now"`
	Admission []struct {
		Name      string         `json:"name"`
		Raw       string         `json:"raw"`
		Operation string         `json:"operation"`
		Binding   map[string]any `json:"binding"`
		Known     bool           `json:"known"`
		Now       *time.Time     `json:"now"`
		Deny      string         `json:"deny"`
	} `json:"admission"`
	MixedVersion []struct {
		Name         string   `json:"name"`
		Capabilities []string `json:"capabilities"`
		Deny         string   `json:"deny"`
	} `json:"mixed_version"`
	Reports []struct {
		Name   string                  `json:"name"`
		Report ResultReport            `json:"report"`
		Result *agentpb.StackKitResult `json:"result"`
		Deny   string                  `json:"deny"`
	} `json:"reports"`
	Revisions []struct {
		Name   string `json:"name"`
		Stored struct {
			State              string    `json:"state"`
			Revision           int64     `json:"revision"`
			Digest             string    `json:"digest"`
			SameRevisionDigest string    `json:"same_revision_digest"`
			DispatchGeneration int64     `json:"dispatch_generation"`
			AuthorityExpiresAt time.Time `json:"authority_expires_at"`
		} `json:"stored"`
		Submission struct {
			Report     ResultReport `json:"report"`
			Generation int64        `json:"generation"`
			StartedAt  time.Time    `json:"started_at"`
		} `json:"submission"`
		Duplicate bool   `json:"duplicate"`
		Deny      string `json:"deny"`
	} `json:"revisions"`
}

func loadVectors(t *testing.T) contractVectors {
	t.Helper()
	data, err := os.ReadFile("testdata/contract-vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors contractVectors
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	return vectors
}

func code(denial *Denial) string {
	if denial == nil {
		return ""
	}
	return denial.Code
}

// A Guard admits exactly the bindings the contract admits: the valid exchange
// and a journaled recovery, and refuses every altered binding, payload,
// identity, generation, authority or contract version before any effect.
func TestGuardAdmitsOnlyContractBindings(t *testing.T) {
	vectors := loadVectors(t)
	for _, vector := range vectors.Admission {
		t.Run(vector.Name, func(t *testing.T) {
			fields := map[string]any{}
			if err := json.Unmarshal(vectors.BaseBinding, &fields); err != nil {
				t.Fatal(err)
			}
			for key, value := range vector.Binding {
				fields[key] = value
			}
			encoded, err := json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			var binding Binding
			if err := json.Unmarshal(encoded, &binding); err != nil {
				t.Fatal(err)
			}
			raw, operation, now := vectors.CommandRaw, binding.Operation, vectors.Now
			if vector.Raw != "" {
				raw = vector.Raw
			}
			if vector.Operation != "" {
				operation = vector.Operation
			}
			if vector.Now != nil {
				now = *vector.Now
			}
			expect := Expectation{
				TenantID: vectors.Expectation.TenantID, WorkerID: vectors.Expectation.WorkerID, ServerID: vectors.Expectation.ServerID,
				HighestGeneration: vectors.Expectation.HighestGeneration, Known: vector.Known,
			}
			if got := code(binding.Validate([]byte(raw), operation, expect, now)); got != vector.Deny {
				t.Fatalf("admission = %q, want %q", got, vector.Deny)
			}
		})
	}
}

// Mixed versions fail closed: only a Guard that speaks this binding contract
// is sent mutating work; any other stays observe-only with upgrade guidance.
func TestMutatingDispatchRequiresTheBindingContract(t *testing.T) {
	for _, vector := range loadVectors(t).MixedVersion {
		t.Run(vector.Name, func(t *testing.T) {
			denial := AdmitMutatingDispatch(vector.Capabilities)
			if got := code(denial); got != vector.Deny {
				t.Fatalf("mutating dispatch = %q, want %q", got, vector.Deny)
			}
			if denial != nil && denial.Guidance == "" {
				t.Fatal("an observe-only refusal carries no guidance")
			}
		})
	}
}

// A report is accepted only when its state, revision bound and resolution are
// known and its frozen digest matches its content.
func TestReportCheckFollowsTheFrozenDigests(t *testing.T) {
	for _, vector := range loadVectors(t).Reports {
		t.Run(vector.Name, func(t *testing.T) {
			report := vector.Report
			if report.Digest == "<computed>" {
				report.Digest, _ = report.ComputeDigest("cmd-vec-1", vector.Result)
			}
			if got := code(report.Check("cmd-vec-1", vector.Result)); got != vector.Deny {
				t.Fatalf("report check = %q, want %q", got, vector.Deny)
			}
		})
	}
}

// Revisions commit in order against the stored outcome: an identical repeat is
// acknowledged again, a conflict or gap is refused, only a resolution that
// names the evidence it supersedes recovers an unknown outcome, and a stale
// credential or an effect after its authority cannot settle a command.
func TestRevisionAcceptanceAcksIdempotentlyAndRefusesConflicts(t *testing.T) {
	for _, vector := range loadVectors(t).Revisions {
		t.Run(vector.Name, func(t *testing.T) {
			stored := Committed{
				State: vector.Stored.State, Revision: vector.Stored.Revision, Digest: vector.Stored.Digest,
				SameRevisionDigest: vector.Stored.SameRevisionDigest, DispatchGeneration: vector.Stored.DispatchGeneration,
				AuthorityExpiresAt: vector.Stored.AuthorityExpiresAt,
			}
			submission := Submission{Report: vector.Submission.Report, Generation: vector.Submission.Generation, StartedAt: vector.Submission.StartedAt}
			duplicate, denial := AcceptRevision(stored, submission)
			if got := code(denial); got != vector.Deny || duplicate != vector.Duplicate {
				t.Fatalf("accept = (duplicate %v, %q), want (duplicate %v, %q)", duplicate, got, vector.Duplicate, vector.Deny)
			}
		})
	}
}
