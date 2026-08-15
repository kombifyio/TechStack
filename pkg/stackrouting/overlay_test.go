package stackrouting

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/unifier"
	"gopkg.in/yaml.v3"
)

func route53State() *DesiredState {
	return &DesiredState{
		StackID: "stack-1", ServerID: "server-1", Revision: 2, Mode: ModeCustomDomain, Domain: "kombified.com",
		Provenance: Provenance{Source: "byod", DNSProvider: "route53"},
	}
}

func TestApplyToStackSpecBytesRemovesStaleRoutingInEveryPrecedenceLayer(t *testing.T) {
	input := []byte(`
domain: kombify.me
address_mode: kombify-me
subdomainPrefix: old-prefix
network:
  domain: kombify.me
  address_mode: kombify-me
metadata:
  domain: kombify.me
  address_mode: kombify-me
  routing_revision: "1"
  routing_server_id: old-server
  routing_lease_id: old-lease
  dns_provider: cloudflare
  dns_zone_id: old-zone
  kombify_me_address_status: registered
  subdomain_prefix: old-prefix
user_config:
  domain: kombify.me
  network:
    domain: kombify.me
  metadata:
    domain: kombify.me
    dns_zone_id: nested-old-zone
    subdomainPrefix: nested-prefix
config:
  domain: kombify.me
  address_mode: kombify-me
  network:
    domain: kombify.me
  metadata:
    domain: kombify.me
    routing_source: stale
    dns_zone_id: config-old-zone
`)
	encoded, err := ApplyToStackSpecBytes(input, route53State())
	if err != nil {
		t.Fatalf("ApplyToStackSpecBytes: %v", err)
	}
	if strings.Contains(string(encoded), "kombify.me") || strings.Contains(string(encoded), "old-zone") || strings.Contains(string(encoded), "old-prefix") {
		t.Fatalf("stale routing survived:\n%s", encoded)
	}
	var got map[string]any
	if err := yaml.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	assertRoutingMap(t, got, "root")
	for _, key := range []string{"user_config", "config"} {
		nested, ok := got[key].(map[string]any)
		if !ok {
			t.Fatalf("%s missing: %#v", key, got[key])
		}
		assertRoutingMap(t, nested, key)
	}
}

func assertRoutingMap(t *testing.T, got map[string]any, layer string) {
	t.Helper()
	if got["domain"] != "kombified.com" || got["address_mode"] != ModeCustomDomain {
		t.Fatalf("%s root routing = %#v", layer, got)
	}
	if prefix, exists := got["subdomainPrefix"]; !exists || prefix != "" {
		t.Fatalf("%s root prefix must be explicitly empty: %#v", layer, got)
	}
	network, _ := got["network"].(map[string]any)
	if network["domain"] != "kombified.com" || network["address_mode"] != ModeCustomDomain {
		t.Fatalf("%s network = %#v", layer, network)
	}
	if prefix, exists := network["subdomainPrefix"]; !exists || prefix != "" {
		t.Fatalf("%s network prefix must be explicitly empty: %#v", layer, network)
	}
	metadata, _ := got["metadata"].(map[string]any)
	if metadata["domain"] != "kombified.com" || metadata["dns_provider"] != "route53" || metadata["routing_revision"] != "2" || metadata["routing_server_id"] != "server-1" {
		t.Fatalf("%s metadata = %#v", layer, metadata)
	}
	if _, exists := metadata["dns_zone_id"]; exists {
		t.Fatalf("%s retained stale dns_zone_id: %#v", layer, metadata)
	}
	if _, exists := metadata["routing_lease_id"]; exists {
		t.Fatalf("%s invented/retained routing_lease_id: %#v", layer, metadata)
	}
	if prefix, exists := metadata["subdomainPrefix"]; !exists || prefix != "" {
		t.Fatalf("%s metadata prefix must be explicitly empty: %#v", layer, metadata)
	}
}

func TestApplyToKombinationReplacesProviderOwnedMetadata(t *testing.T) {
	spec := &core.KombinationSpec{Metadata: map[string]string{
		"address_mode": "kombify-me", "routing_revision": "1", "routing_lease_id": "old", "dns_provider": "cloudflare", "dns_zone_id": "old-zone", "subdomainPrefix": "old",
	}}
	spec.Network.Domain = "kombify.me"
	if err := ApplyToKombination(spec, route53State()); err != nil {
		t.Fatal(err)
	}
	if spec.Network.Domain != "kombified.com" || spec.Metadata["address_mode"] != ModeCustomDomain || spec.Metadata["dns_provider"] != "route53" {
		t.Fatalf("spec = %#v", spec)
	}
	for _, key := range []string{"dns_zone_id", "routing_lease_id"} {
		if _, exists := spec.Metadata[key]; exists {
			t.Fatalf("stale %s survived: %#v", key, spec.Metadata)
		}
	}
	if prefix, exists := spec.Metadata["subdomainPrefix"]; !exists || prefix != "" {
		t.Fatalf("custom-domain prefix not explicitly cleared: %#v", spec.Metadata)
	}
}

func TestApplyToPersistedStackSpecKeepsIntentImmutable(t *testing.T) {
	persister, err := unifier.NewSpecPersisterWithPath(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	intent := []byte("name: demo\ndomain: kombify.me\n")
	if _, _, saveErr := persister.SaveIntentBytes(intent); saveErr != nil {
		t.Fatal(saveErr)
	}
	if _, _, saveErr := persister.SaveStackSpecBytes([]byte("name: demo\ndomain: kombify.me\nmetadata:\n  dns_zone_id: old-zone\n")); saveErr != nil {
		t.Fatal(saveErr)
	}
	if _, _, applyErr := ApplyToPersistedStackSpec(persister, route53State()); applyErr != nil {
		t.Fatal(applyErr)
	}
	gotIntent, err := persister.LoadIntentBytes()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotIntent, intent) {
		t.Fatalf("intent changed:\n%s", gotIntent)
	}
	handoff, err := os.ReadFile(persister.GetStackSpecPath())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(handoff), "domain: kombified.com") || !strings.Contains(string(handoff), `subdomainPrefix: ""`) || strings.Contains(string(handoff), "old-zone") || strings.Contains(string(handoff), "kombify.me") {
		t.Fatalf("handoff =\n%s", handoff)
	}
}
