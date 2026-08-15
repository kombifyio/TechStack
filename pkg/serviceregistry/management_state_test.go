package serviceregistry

import "testing"

// legacyRunningStatus is the legacy services.status value the pre-074 read-time
// derivations inspected alongside the provenance.
const legacyRunningStatus = "running"

func TestManagementStateForSourceIsTheSingleOwnershipRule(t *testing.T) {
	for _, test := range []struct {
		source string
		want   ManagementState
	}{
		{source: SourceObserved, want: ManagementObserved},
		{source: "  OBSERVED  ", want: ManagementObserved},
		{source: SourceStackKitsInventory, want: ManagementManaged},
		{source: SourceStackKitOutputs, want: ManagementManaged},
		{source: SourceLegacyRegistryBackfill, want: ManagementManaged},
	} {
		if got := ManagementStateForSource(test.source); got != test.want {
			t.Fatalf("ManagementStateForSource(%q) = %q, want %q", test.source, got, test.want)
		}
	}
}

// The legacy rule must reproduce what the three deleted read-time derivations
// answered, or the 074 backfill would change what a stored row means.
func TestManagementStateForLegacyRecordReproducesTheHistoricalDerivation(t *testing.T) {
	for _, test := range []struct {
		name, source, status, serviceType string
		want                              ManagementState
	}{
		{name: "observed source", source: SourceObserved, status: legacyRunningStatus, want: ManagementObserved},
		{name: "legacy observed status", source: SourceStackKitOutputs, status: "observed", want: ManagementObserved},
		{name: "hand imported custom", source: SourceStackKitOutputs, status: legacyRunningStatus, serviceType: "custom", want: ManagementObserved},
		{name: "stackkits rollout", source: SourceStackKitsInventory, status: legacyRunningStatus, want: ManagementManaged},
		{name: "unknown status", source: SourceStackKitsInventory, status: "unknown", want: ManagementManaged},
		{name: "pocketbase bridge without a source", source: "", status: legacyRunningStatus, want: ManagementManaged},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ManagementStateForLegacyRecord(test.source, test.status, test.serviceType)
			if got != test.want {
				t.Fatalf("ManagementStateForLegacyRecord = %q, want %q", got, test.want)
			}
		})
	}
}

// Anything unreadable must fail closed: claiming a contract we cannot prove
// would let drift comparison run against fiction.
func TestCanonicalManagementStateFailsClosed(t *testing.T) {
	for _, value := range []string{"", "adopted", "unknown", "Managed "} {
		got := CanonicalManagementState(value)
		if value == "Managed " {
			if got != ManagementManaged {
				t.Fatalf("CanonicalManagementState(%q) = %q, want managed", value, got)
			}
			continue
		}
		if got != ManagementObserved {
			t.Fatalf("CanonicalManagementState(%q) = %q, want a fail-closed observed", value, got)
		}
	}
	if err := ValidateManagementState("adopted"); err == nil {
		t.Fatal("an out-of-vocabulary management state was accepted")
	}
}

func TestDesiredStateIsOnlyDefinedForManagedServices(t *testing.T) {
	if !DesiredStateApplicable(ManagementManaged) {
		t.Fatal("a managed service must have a declared desired state")
	}
	if DesiredStateApplicable(ManagementObserved) {
		t.Fatal("an observed service has no declared contract to compare against")
	}
}

// `source` is provenance and stays a separate axis from both ownership and the
// StackKits evidence-provenance vocabulary.
func TestServiceSourceVocabularyIsClosedAndSeparateFromEvidenceProvenance(t *testing.T) {
	for _, source := range ServiceSources {
		if err := ValidateSource(source); err != nil {
			t.Fatalf("ValidateSource(%q): %v", source, err)
		}
	}
	for _, evidence := range []string{"local-runtime", "standard-process", "verified-apply-evidence"} {
		if err := ValidateSource(evidence); err == nil {
			t.Fatalf("StackKits evidence provenance %q was accepted as an inventory source", evidence)
		}
		if got := CanonicalSource(evidence); got != SourceObserved {
			t.Fatalf("CanonicalSource(%q) = %q, want a fail-closed observed", evidence, got)
		}
	}
	if got := CanonicalSource("  StackKits-Inventory "); got != SourceStackKitsInventory {
		t.Fatalf("CanonicalSource folded case/padding to %q", got)
	}
}
