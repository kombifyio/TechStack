//go:build integration

package unifier_test

import (
	"os"
	"slices"
	"testing"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/unifier"
)

// TestPublishedStackKitUnifyAndPipeline exercises the public consumer against
// the pinned public release's metadata, preserving Techstack node and service
// intent without loading or interpreting StackKits CUE.
func TestPublishedStackKitUnifyAndPipeline(t *testing.T) {
	root := os.Getenv("TECHSTACK_STACKKITS_DIR")
	if root != "" {
		if _, err := unifier.NewStackKitLoaderWithDir(root); err != nil {
			t.Fatalf("explicit published metadata fixture: %v", err)
		}
	}
	engine, err := unifier.New()
	if err != nil {
		t.Fatal(err)
	}

	name, kit, node, role, provider := "published-consumer", "basement-kit", "main", "main", "local"
	host, user, port := "192.0.2.1", "fixture", 22
	service, serviceType := "demo", "docker"
	spec := &core.KombinationSpec{
		Name: name, Kit: kit,
		Nodes: []core.NodeSpec{{Name: node, Type: role, Provider: provider,
			SSH: &core.SSHConfig{Host: host, User: user, Port: port}}},
		Services: []core.ServiceSpec{{Name: service, Type: serviceType, Node: node}},
	}
	assertIntent := func(plan *core.UnifiedSpec) {
		t.Helper()
		if plan == nil || plan.Name != name || plan.StackKit != kit {
			t.Fatalf("published consumer changed stack identity: %+v", plan)
		}
		if !slices.ContainsFunc(plan.ResolvedNodes, func(n core.ResolvedNode) bool {
			return n.Name == node && n.Provider == provider && n.SSH != nil && n.SSH.Host == host
		}) {
			t.Fatal("published consumer changed target node")
		}
		if !slices.ContainsFunc(plan.ResolvedServices, func(s core.ResolvedService) bool {
			return s.Name == service && s.Type == serviceType && s.Node == node
		}) {
			t.Fatal("published consumer changed service node affinity")
		}
	}

	direct, err := engine.Unify(spec, nil)
	if err != nil {
		t.Fatalf("public Engine.Unify: %v", err)
	}
	assertIntent(direct)
	result := unifier.NewPipeline(engine).ExecuteInput(&core.InputSpec{
		Name: &name, StackKit: &kit,
		Nodes: []core.InputNodeSpec{{Name: &node, Type: &role, Provider: &provider,
			SSH: &core.InputSSHConfig{Host: &host, User: &user, Port: &port}}},
		Services: []core.InputServiceSpec{{Name: &service, Type: &serviceType, Node: &node}},
	})
	if !result.Success {
		t.Fatalf("public Pipeline.ExecuteInput failed at %s: %s", result.FailedStep, result.ErrorMessage)
	}
	assertIntent(result.UnifiedSpec)
}
