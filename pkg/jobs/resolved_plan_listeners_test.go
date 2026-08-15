package jobs

import (
	"strings"
	"testing"
)

func TestParseResolvedPlanListenerSetConsumesOnlyCanonicalRuntimeListeners(t *testing.T) {
	plan := resolvedPlanWithListeners(`[
		{"id":"listener-b","moduleRef":"module","unitRef":"unit","instanceRef":"instance","nodeRef":"node-a","componentRef":"api","transport":"TCP","bindAddress":"0.0.0.0","port":8443,"targetPort":443,"sharing":"exclusive","sourceRouteRefs":["route-b","route-a","route-a"],"exposure":"public"},
		{"id":"listener-a","moduleRef":"module","unitRef":"unit","instanceRef":"instance","nodeRef":"node-a","componentRef":"metrics","transport":"tcp","bindAddress":"127.0.0.1","port":9090,"targetPort":9090,"sharing":"exclusive","sourceRouteRefs":[],"exposure":"local"}
	]`)
	set, err := parseResolvedPlanListenerSet([]byte(plan))
	if err != nil {
		t.Fatalf("parseResolvedPlanListenerSet() error = %v", err)
	}
	if set.StackKitInstanceID != "stack-a" || set.PlanHash != testResolvedPlanHash || set.NodeRef != "node-a" || len(set.Listeners) != 2 {
		t.Fatalf("listener set = %#v", set)
	}
	if set.Listeners[0].ID != "listener-a" || set.Listeners[1].ID != "listener-b" {
		t.Fatalf("listeners are not normalized by id: %#v", set.Listeners)
	}
	if got := strings.Join(set.Listeners[1].SourceRouteRefs, ","); got != "route-a,route-b" {
		t.Fatalf("source route refs = %q", got)
	}
	if set.Listeners[1].Port != 8443 || set.Listeners[1].TargetPort != 443 {
		t.Fatal("host port and targetPort were collapsed")
	}
}

func TestParseResolvedPlanListenerSetAcceptsMandatoryEmptySet(t *testing.T) {
	set, err := parseResolvedPlanListenerSet([]byte(resolvedPlanWithListeners(`[]`)))
	if err != nil {
		t.Fatalf("parseResolvedPlanListenerSet() error = %v", err)
	}
	if set.Listeners == nil || len(set.Listeners) != 0 || set.NodeRef != "" {
		t.Fatalf("empty listener set = %#v", set)
	}
}

func TestParseResolvedPlanListenerSetDoesNotCoupleStackKitIdentityToTechstackIdentity(t *testing.T) {
	plan := strings.Replace(resolvedPlanWithListeners(`[]`), `"stackId":"stack-a"`, `"stackId":"owner-defined-kit-instance"`, 1)
	set, err := parseResolvedPlanListenerSet([]byte(plan))
	if err != nil {
		t.Fatalf("parseResolvedPlanListenerSet() error = %v", err)
	}
	if set.StackKitInstanceID != "owner-defined-kit-instance" {
		t.Fatalf("StackKitInstanceID = %q", set.StackKitInstanceID)
	}
}

func TestParseResolvedPlanListenerSetFailsClosedWithoutAuthorityOrAcrossNodes(t *testing.T) {
	tests := map[string]string{
		"missing network":   `{"apiVersion":"stackkit.resolved-plan/v1","kind":"ResolvedPlan","stackId":"stack-a","planHash":"` + testResolvedPlanHash + `","routes":[{"port":443}]}`,
		"missing listeners": `{"apiVersion":"stackkit.resolved-plan/v1","kind":"ResolvedPlan","stackId":"stack-a","planHash":"` + testResolvedPlanHash + `","network":{"routes":[{"port":443}]}}`,
		"multi node": resolvedPlanWithListeners(`[
			{"id":"a","moduleRef":"m","unitRef":"u","instanceRef":"i","nodeRef":"node-a","componentRef":"api","transport":"tcp","bindAddress":"0.0.0.0","port":443,"targetPort":443,"sharing":"exclusive","sourceRouteRefs":[],"exposure":"public"},
			{"id":"b","moduleRef":"m","unitRef":"u","instanceRef":"i","nodeRef":"node-b","componentRef":"api","transport":"tcp","bindAddress":"0.0.0.0","port":8443,"targetPort":443,"sharing":"exclusive","sourceRouteRefs":[],"exposure":"public"}
		]`),
		"unknown listener field": resolvedPlanWithListeners(`[
			{"id":"a","moduleRef":"m","unitRef":"u","instanceRef":"i","nodeRef":"node-a","componentRef":"api","transport":"tcp","bindAddress":"0.0.0.0","port":443,"targetPort":443,"sharing":"exclusive","sourceRouteRefs":[],"exposure":"public","openPorts":[443]}
		]`),
	}
	for name, plan := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := parseResolvedPlanListenerSet([]byte(plan)); err == nil {
				t.Fatal("parseResolvedPlanListenerSet() succeeded, want fail-closed error")
			}
		})
	}
}

const testResolvedPlanHash = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

func resolvedPlanWithListeners(listeners string) string {
	return `{"apiVersion":"stackkit.resolved-plan/v1","kind":"ResolvedPlan","stackId":"stack-a","planHash":"` + testResolvedPlanHash + `","network":{"runtimeListeners":` + listeners + `},"routes":[{"port":1234}],"openPorts":[1234]}`
}
