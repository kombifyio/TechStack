package guardbinding

import (
	"strings"
	"testing"
	"time"
)

func TestTenantHandoffRebindAdmission(t *testing.T) {
	now := time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)
	confirmation, err := NewHandoffConfirmation("tenant-a", HandoffQuarantine, now)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseHandoffConfirmation(confirmation.Token(), now)
	if err != nil || parsed != confirmation {
		t.Fatalf("confirmation token does not round-trip: %v", err)
	}
	if _, err := ParseHandoffConfirmation(confirmation.Token(), confirmation.ExpiresAt); err == nil {
		t.Fatal("an expired confirmation was accepted")
	}
	if _, err := ParseHandoffConfirmation(strings.TrimSuffix(confirmation.Token(), "Q")+"!", now); err == nil {
		t.Fatal("a malformed confirmation was accepted")
	}
	if !ConfirmationMatches(confirmation.Code, ConfirmationHash(confirmation.Code)) || ConfirmationMatches("other", ConfirmationHash(confirmation.Code)) {
		t.Fatal("confirmation hash comparison is wrong")
	}

	receipt := TenantHandoffReceipt{
		ContractVersion: TenantHandoffContractVersion, PriorTenantID: "tenant-a", HandoffID: confirmation.HandoffID,
		Confirmation: confirmation.Code, Disposition: confirmation.Disposition, Entries: 2,
		CommandsDigest: CommandsDigest([]string{"b", "a"}), ArchiveDigest: CommandsDigest(nil), RemovedAt: now,
	}
	if CommandsDigest([]string{"a", "b"}) != receipt.CommandsDigest {
		t.Fatal("command digest depends on order")
	}
	digest, err := receipt.Digest()
	if err != nil {
		t.Fatal(err)
	}
	other := receipt
	other.Confirmation = "another-code"
	if again, _ := other.Digest(); again != digest {
		t.Fatal("the receipt digest must not include the one-time confirmation")
	}

	cases := []struct {
		name        string
		declaration *PriorCustody
		tenant      string
		custody     []string
		keyed       bool
		consume     int
		code        string
	}{
		{name: "legacy Guard without declaration", declaration: nil, tenant: "tenant-b"},
		{name: "nothing held", declaration: &PriorCustody{}, tenant: "tenant-b"},
		{name: "same tenant keeps predecessor recovery", declaration: &PriorCustody{HeldTenants: []string{"tenant-b"}, HeldEntries: 3}, tenant: "tenant-b"},
		{name: "other tenant held refuses", declaration: &PriorCustody{HeldTenants: []string{"tenant-a"}, HeldEntries: 1}, tenant: "tenant-b", code: CodeTenantHandoffRequired},
		{name: "receipt does not excuse held payloads", declaration: &PriorCustody{HeldTenants: []string{"tenant-a"}, HeldEntries: 1, Receipts: []TenantHandoffReceipt{receipt}}, tenant: "tenant-b", code: CodeTenantHandoffRequired},
		{name: "receipt is consumed on cross-tenant rebind", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{receipt}}, tenant: "tenant-b", consume: 1},
		{name: "receipt of the same tenant is not consumed", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{receipt}}, tenant: "tenant-a"},
		{name: "repeated receipt refuses", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{receipt, receipt}}, tenant: "tenant-b", code: CodeTenantHandoffInvalid},
		{name: "receipt without confirmation refuses", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{func() TenantHandoffReceipt { r := receipt; r.Confirmation = ""; return r }()}}, tenant: "tenant-b", code: CodeTenantHandoffInvalid},
		{name: "control plane custody refuses an omitted declaration", declaration: nil, tenant: "tenant-b", custody: []string{"tenant-a"}, code: CodeTenantHandoffRequired},
		{name: "control plane custody refuses a deleted receipts file", declaration: &PriorCustody{Contract: TenantHandoffContractVersion}, tenant: "tenant-b", custody: []string{"tenant-a"}, code: CodeTenantHandoffRequired},
		{name: "receipt of another tenant does not cover custody", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{receipt}}, tenant: "tenant-b", custody: []string{"tenant-c"}, code: CodeTenantHandoffRequired},
		{name: "receipt covers control plane custody", declaration: &PriorCustody{Contract: TenantHandoffContractVersion, Receipts: []TenantHandoffReceipt{receipt}}, tenant: "tenant-b", custody: []string{"tenant-a"}, consume: 1},
		{name: "own tenant custody needs no receipt", declaration: nil, tenant: "tenant-a", custody: []string{"tenant-a"}},
		{name: "keyed pairing ignores a receipt the key does not need", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{receipt}}, tenant: "tenant-b", keyed: true},
		{name: "keyed pairing consumes the receipt its key needs", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{receipt}}, tenant: "tenant-b", custody: []string{"tenant-a"}, keyed: true, consume: 1},
		{name: "unknown declaration contract refuses", declaration: &PriorCustody{Contract: "guard.tenant-handoff/v0"}, tenant: "tenant-b", code: CodeContractUnsupported},
		{name: "receipt with unknown disposition refuses", declaration: &PriorCustody{Receipts: []TenantHandoffReceipt{func() TenantHandoffReceipt { r := receipt; r.Disposition = "delete"; return r }()}}, tenant: "tenant-b", code: CodeTenantHandoffInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			consume, denial := tc.declaration.AdmitRebind(tc.tenant, tc.custody, tc.keyed)
			switch {
			case tc.code != "" && (denial == nil || denial.Code != tc.code):
				t.Fatalf("denial = %v, want %s", denial, tc.code)
			case tc.code == "" && denial != nil:
				t.Fatalf("unexpected denial %v", denial)
			case len(consume) != tc.consume:
				t.Fatalf("consume %d receipts, want %d", len(consume), tc.consume)
			}
		})
	}
}

func TestTenantHandoffConsumptionIsBoundToKeyPairingAndExpiry(t *testing.T) {
	now := time.Date(2026, 10, 26, 9, 0, 0, 0, time.UTC)
	record := HandoffConfirmationRecord{
		TenantID: "tenant-a", ConfirmationSHA256: ConfirmationHash("code"), Disposition: HandoffExport,
		KeyThumbprint: "key-x", ExpiresAt: now.Add(time.Hour),
	}
	first := HandoffPresentation{
		ConfirmationSHA256: ConfirmationHash("code"), Disposition: HandoffExport, ReceiptDigest: "digest",
		ConsumerTenantID: "tenant-b", ConsumerWorkerID: "worker-b", KeyThumbprint: "key-x", Now: now,
	}
	if repeat, ok := DecideHandoffConsumption(record, first); !ok || repeat {
		t.Fatalf("first consumption = repeat %t ok %t", repeat, ok)
	}
	refused := map[string]func(HandoffPresentation) HandoffPresentation{
		"wrong code": func(p HandoffPresentation) HandoffPresentation {
			p.ConfirmationSHA256 = ConfirmationHash("other")
			return p
		},
		"other disposition":           func(p HandoffPresentation) HandoffPresentation { p.Disposition = HandoffQuarantine; return p },
		"another server key":          func(p HandoffPresentation) HandoffPresentation { p.KeyThumbprint = "key-y"; return p },
		"no key proof":                func(p HandoffPresentation) HandoffPresentation { p.KeyThumbprint = ""; return p },
		"expired":                     func(p HandoffPresentation) HandoffPresentation { p.Now = record.ExpiresAt; return p },
		"rebind into the same tenant": func(p HandoffPresentation) HandoffPresentation { p.ConsumerTenantID = "tenant-a"; return p },
	}
	for name, change := range refused {
		if _, ok := DecideHandoffConsumption(record, change(first)); ok {
			t.Fatalf("%s was accepted", name)
		}
	}
	unbound := record
	unbound.KeyThumbprint = ""
	if _, ok := DecideHandoffConsumption(unbound, first); ok {
		t.Fatal("a confirmation bound to no key was accepted as a wildcard")
	}
	consumedAt := now
	record.ReceiptDigest, record.ReceiptedAt = "digest", &consumedAt
	record.ConsumerTenantID, record.ConsumerWorkerID, record.ConsumerKey = "tenant-b", "worker-b", "key-x"
	retry := first
	retry.Now = now.Add(HandoffRepeatWindow - time.Second)
	if repeat, ok := DecideHandoffConsumption(record, retry); !ok || !repeat {
		t.Fatalf("same pairing retry = repeat %t ok %t", repeat, ok)
	}
	replays := map[string]func(HandoffPresentation) HandoffPresentation{
		"replay by another pairing":  func(p HandoffPresentation) HandoffPresentation { p.ConsumerWorkerID = "worker-c"; return p },
		"replay into another tenant": func(p HandoffPresentation) HandoffPresentation { p.ConsumerTenantID = "tenant-c"; return p },
		"another receipt":            func(p HandoffPresentation) HandoffPresentation { p.ReceiptDigest = "other"; return p },
		"after the repeat window":    func(p HandoffPresentation) HandoffPresentation { p.Now = now.Add(HandoffRepeatWindow); return p },
	}
	for name, change := range replays {
		if _, ok := DecideHandoffConsumption(record, change(retry)); ok {
			t.Fatalf("%s was accepted", name)
		}
	}
	record.ExpiresAt = now.Add(time.Minute)
	if _, ok := DecideHandoffConsumption(record, retry); ok {
		t.Fatal("a repeat after expiry was accepted")
	}
}
