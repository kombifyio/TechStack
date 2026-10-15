package managedstackkit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestRolloutInventoryRejectsAnInvalidPlan(t *testing.T) {
	builder, err := NewRolloutInventory("sha256:" + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = builder.Build(context.Background(), RolloutInventoryRequest{
		TenantID: "tenant-a", StackID: "stack-a", ResolvedPlan: []byte(`{"kind":"not-a-resolved-plan"}`),
	}); err == nil {
		t.Fatal("invalid plan was accepted")
	}
}

func TestRolloutInventoryBindsTheExactOperationsProcess(t *testing.T) {
	builder, err := NewRolloutInventory("sha256:" + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	inventory, err := builder.Build(context.Background(), RolloutInventoryRequest{TenantID: "tenant-a", StackID: "stack-a", ResolvedPlan: testResolvedPlan(t)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(inventory), cloudOperationsExecutable) || !strings.Contains(string(inventory), "sha256:"+strings.Repeat("b", 64)) {
		t.Fatalf("Inventory does not bind exact Operations process: %s", inventory)
	}
}

func TestRolloutInventoryBindsExactlyThePlanNodesOfAMultiNodeKit(t *testing.T) {
	builder, err := NewRolloutInventory(testDigest("b"))
	if err != nil {
		t.Fatal(err)
	}
	nodes := []NodeChannel{
		{ChannelRef: "local-home-main", SiteRef: "home", NodeRef: "home-main"},
		{ChannelRef: "local-cloud-edge", SiteRef: "cloud", NodeRef: "cloud-edge"},
	}
	build := func(planNodes string) ([]byte, error) {
		return builder.Build(context.Background(), RolloutInventoryRequest{
			TenantID: "tenant-a", StackID: "stack-a", Nodes: nodes,
			ResolvedPlan: []byte(`{"apiVersion":"stackkit.resolved-plan/v1","backupTargetRequirements":{},"nodes":` + planNodes + `}`),
		})
	}
	inventory, err := build(`[{"id":"cloud-edge","siteRef":"cloud"},{"id":"home-main","siteRef":"home"}]`)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Channels map[string]struct{ SiteRef, NodeRef string } `json:"executionChannels"`
	}
	if err := json.Unmarshal(inventory, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Channels["local-home-main"].NodeRef != "home-main" || decoded.Channels["local-cloud-edge"].SiteRef != "cloud" || len(decoded.Channels) != 2 {
		t.Fatalf("each plan node needs its own channel, got %s", inventory)
	}
	for name, planNodes := range map[string]string{
		"a node the kit does not bind": `[{"id":"cloud-edge","siteRef":"cloud"},{"id":"home-main","siteRef":"home"},{"id":"home-standby","siteRef":"home"}]`,
		"a node left out":              `[{"id":"home-main","siteRef":"home"}]`,
		"a node on another Site":       `[{"id":"cloud-edge","siteRef":"edge"},{"id":"home-main","siteRef":"home"}]`,
	} {
		if _, err := build(planNodes); err == nil {
			t.Fatalf("a plan with %s was bound", name)
		}
	}
}
