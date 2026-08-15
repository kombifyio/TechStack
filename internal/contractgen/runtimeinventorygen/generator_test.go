package runtimeinventorygen

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/internal/selfhostcontracts/runtimeinventory"
)

func TestSelfhostRuntimeInventoryContractIsSecretFree(t *testing.T) {
	now := time.Date(2026, 8, 3, 10, 0, 0, 0, time.UTC)
	payload := runtimeinventory.ServerList{
		ObservedAt: now,
		Freshness: runtimeinventory.Freshness{
			State:             "fresh",
			StaleAfterSeconds: 60,
		},
		InventoryRevision: 7,
		CollectionCursor:  "compat-7",
		PageSize:          1,
		Servers: []runtimeinventory.Server{{
			ID: "server-compat", Name: "Connected Windows Runtime", ObservedAt: &now,
			Platform:   runtimeinventory.Platform{OS: "windows", Arch: "amd64"},
			Connection: runtimeinventory.ObservedState{State: "connected", ObservedAt: &now},
		}},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"observed_at", "freshness", "inventory_revision", "collection_cursor", "page_size", "servers"} {
		if !strings.Contains(string(encoded), "\""+required+"\"") {
			t.Fatalf("self-host inventory projection missing %q: %s", required, encoded)
		}
	}
	for _, forbidden := range []string{"credential", "secret", "token", "provider_payload", "transport_endpoint"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("self-host inventory projection exposes %q: %s", forbidden, encoded)
		}
	}
}

func TestSchemaRejectsSecretAndOpenFields(t *testing.T) {
	t.Parallel()
	for _, invalidType := range []string{"map[string]string", "arbitrary.Package", "[][]string", "func()"} {
		schema := Schema{
			SchemaVersion: SchemaVersion, GeneratorVersion: GeneratorVersion, WireVersion: WireVersion,
			PackageName: "runtimeinventory", SourceRepository: "github.com/kombifyio/TechStack",
			SourcePath: "contracts/runtimeinventory/v1/schema.json", Imports: []string{"time"},
			Types: []TypeSchema{{Name: "Server", Doc: []string{"Server is invalid."}, Fields: []FieldSchema{{Name: "Value", GoType: invalidType, JSON: "value"}}}},
		}
		if err := Validate(schema); err == nil {
			t.Errorf("unsupported Go type %q was accepted", invalidType)
		}
	}
	schema := Schema{
		SchemaVersion: SchemaVersion, GeneratorVersion: GeneratorVersion, WireVersion: WireVersion,
		PackageName: "runtimeinventory", SourceRepository: "github.com/kombifyio/TechStack",
		SourcePath: "contracts/runtimeinventory/v1/schema.json", Imports: []string{"time"},
		Types: []TypeSchema{{Name: "Server", Doc: []string{"Server is invalid."}, Fields: []FieldSchema{{Name: "Token", GoType: "string", JSON: "api_token"}}}},
	}
	if err := Validate(schema); err == nil {
		t.Fatal("secret-bearing schema field was accepted")
	}
}
