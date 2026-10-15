package federationfabric

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/stackkits/pkg/federationbinding"
)

const testRequirement = `{"apiVersion":"stackkit.federation-link-requirement/v1","kind":"FederationLinkRequirement","stackId":"stack-a","capabilityRef":"inter-site-link","contractOwnerRef":"stackkits-federation-link","capabilityContractHash":"sha256:` + "%a" + `","homeSiteRefs":["home"],"cloudSiteRefs":["cloud"],"targetNodes":[{"siteRef":"cloud","nodeRef":"cloud-edge"},{"siteRef":"home","nodeRef":"home-main"}],"bridgeContractHash":"sha256:` + "%b" + `","policy":{"defaultDeny":true},"specHash":"sha256:` + "%c" + `","requirementsHash":"sha256:` + "%d" + `"}`

func requirementJSON() []byte {
	text := testRequirement
	for key, character := range map[string]string{"%a": "a", "%b": "b", "%c": "c", "%d": "d"} {
		text = strings.ReplaceAll(text, key, strings.Repeat(character, 64))
	}
	return []byte(text)
}

// A binding the authority issues must be exactly valid for the requirement the
// node compiled, must stay inside the window StackKits accepts, and must name
// the same fabric when it is re-issued for the same stack.
func TestIssuedBindingMatchesTheRequirementAndKeepsTheFabricStable(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	counter := byte(0)
	issuer := &Issuer{Now: func() time.Time { return now }, Random: func(buffer []byte) error {
		counter++
		for index := range buffer {
			buffer[index] = counter
		}
		return nil
	}}
	request := Request{TenantID: "tenant-a", StackID: "stack-a", Requirement: requirementJSON(), StackKitsVersion: "v0.51.1", CandidateDigest: "sha256:" + strings.Repeat("e", 64)}
	first, err := issuer.Issue(request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := issuer.Issue(request)
	if err != nil {
		t.Fatal(err)
	}
	var requirement, a, b federationbinding.Document
	for target, raw := range map[*federationbinding.Document][]byte{&requirement: requirementJSON(), &a: first, &b: second} {
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	if err := federationbinding.Validate(a, requirement); err != nil {
		t.Fatalf("the issued binding does not match the requirement: %v", err)
	}
	if a["fabricRef"] != b["fabricRef"] || a["bindingRef"] == b["bindingRef"] {
		t.Fatalf("fabric must be stable and the binding fresh: %v %v", a, b)
	}
	if _, err := issuer.Issue(Request{StackID: "stack-a", Requirement: requirementJSON()}); err == nil {
		t.Fatal("a binding without tenant identity was issued")
	}
}
