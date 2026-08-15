package activities

import (
	"context"
	"testing"

	"github.com/kombifyio/techstack/pkg/ril/workflow"
	"github.com/kombifyio/techstack/pkg/ril/workflows"
)

type statusCall struct{ id, status string }

type fakeRegistry struct {
	statusCalls []statusCall
	created     []string // "source|server|stack"
	createID    string
	deleted     []string
}

func (f *fakeRegistry) CreateTargetService(_ context.Context, src, server, stack string) (string, error) {
	f.created = append(f.created, src+"|"+server+"|"+stack)
	return f.createID, nil
}

func (f *fakeRegistry) SetServiceStatus(_ context.Context, id, status string) error {
	f.statusCalls = append(f.statusCalls, statusCall{id, status})
	return nil
}

func (f *fakeRegistry) DeleteService(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

type agentCall struct{ id, node, idem string }

type fakeAgent struct {
	deploy []agentCall
	remove []agentCall
	drain  []agentCall
}

func (f *fakeAgent) DeployService(_ context.Context, id, node, idem string) error {
	f.deploy = append(f.deploy, agentCall{id, node, idem})
	return nil
}

func (f *fakeAgent) RemoveService(_ context.Context, id, node, idem string) error {
	f.remove = append(f.remove, agentCall{id, node, idem})
	return nil
}

func (f *fakeAgent) DrainService(_ context.Context, id, idem string) error {
	f.drain = append(f.drain, agentCall{id: id, idem: idem})
	return nil
}

type fakeHealth struct {
	healthy bool
	queried []string
}

func (f *fakeHealth) ServiceHealthy(_ context.Context, id string) (bool, error) {
	f.queried = append(f.queried, id)
	return f.healthy, nil
}

type fakeTraffic struct{ points []string }

func (f *fakeTraffic) PointTraffic(_ context.Context, from, to string) error {
	f.points = append(f.points, from+"->"+to)
	return nil
}

func newRunner(deps Deps) *workflow.ActivityRunner {
	r := workflow.NewActivityRunner()
	RegisterServiceMigrationActivities(r, deps)
	return r
}

func run(t *testing.T, r *workflow.ActivityRunner, name string, in map[string]any, idem string) map[string]any {
	t.Helper()
	out, err := r.Run(context.Background(), name, in, idem)
	if err != nil {
		t.Fatalf("activity %s: %v", name, err)
	}
	return out
}

func TestMarkServiceStatusActivity(t *testing.T) {
	reg := &fakeRegistry{}
	r := newRunner(Deps{Registry: reg})
	run(t, r, workflows.ActMarkServiceStatus, map[string]any{"service_id": "svc-1", "status": "migrating"}, "idem-1")
	if len(reg.statusCalls) != 1 || reg.statusCalls[0] != (statusCall{"svc-1", "migrating"}) {
		t.Fatalf("status calls = %+v", reg.statusCalls)
	}
}

func TestCreateTargetServiceActivity_ReturnsID(t *testing.T) {
	reg := &fakeRegistry{createID: "svc-target-1"}
	r := newRunner(Deps{Registry: reg})
	out := run(t, r, workflows.ActCreateTargetService, map[string]any{
		"source_service_id": "svc-src", "target_server_id": "node-2", "stack_id": "stack-1",
	}, "idem")
	if out["target_service_id"] != "svc-target-1" {
		t.Fatalf("output = %+v, want target_service_id svc-target-1", out)
	}
	if len(reg.created) != 1 || reg.created[0] != "svc-src|node-2|stack-1" {
		t.Fatalf("create args = %+v", reg.created)
	}
}

func TestDeleteServiceActivity(t *testing.T) {
	reg := &fakeRegistry{}
	r := newRunner(Deps{Registry: reg})
	run(t, r, workflows.ActDeleteService, map[string]any{"service_id": "svc-old"}, "idem")
	if len(reg.deleted) != 1 || reg.deleted[0] != "svc-old" {
		t.Fatalf("deleted = %+v", reg.deleted)
	}
}

func TestDeployAndRemoveActivities_ThreadIdempotencyKey(t *testing.T) {
	ag := &fakeAgent{}
	r := newRunner(Deps{Agent: ag})
	run(t, r, workflows.ActDeployServiceToNode, map[string]any{"target_service_id": "svc-t", "target_server_id": "node-2"}, "idem-deploy")
	run(t, r, workflows.ActRemoveServiceFromNode, map[string]any{"target_service_id": "svc-t", "target_server_id": "node-2"}, "idem-remove")
	if len(ag.deploy) != 1 || ag.deploy[0] != (agentCall{"svc-t", "node-2", "idem-deploy"}) {
		t.Fatalf("deploy = %+v", ag.deploy)
	}
	if len(ag.remove) != 1 || ag.remove[0] != (agentCall{"svc-t", "node-2", "idem-remove"}) {
		t.Fatalf("remove = %+v", ag.remove)
	}
}

func TestDrainActivity_ThreadsIdempotencyKey(t *testing.T) {
	ag := &fakeAgent{}
	r := newRunner(Deps{Agent: ag})
	run(t, r, workflows.ActDrainServiceOnNode, map[string]any{"service_id": "svc-src"}, "idem-drain")
	if len(ag.drain) != 1 || ag.drain[0].id != "svc-src" || ag.drain[0].idem != "idem-drain" {
		t.Fatalf("drain = %+v", ag.drain)
	}
}

func TestVerifyServiceHealthyActivity(t *testing.T) {
	h := &fakeHealth{healthy: true}
	r := newRunner(Deps{Health: h})
	out := run(t, r, workflows.ActVerifyServiceHealthy, map[string]any{"target_service_id": "svc-t"}, "idem")
	if out["healthy"] != true {
		t.Fatalf("healthy = %v, want true", out["healthy"])
	}
	if len(h.queried) != 1 || h.queried[0] != "svc-t" {
		t.Fatalf("queried = %+v", h.queried)
	}
}

func TestSwitchTrafficActivity(t *testing.T) {
	tr := &fakeTraffic{}
	r := newRunner(Deps{Traffic: tr})
	run(t, r, workflows.ActSwitchServiceTraffic, map[string]any{"from_service_id": "svc-src", "to_service_id": "svc-t"}, "idem")
	if len(tr.points) != 1 || tr.points[0] != "svc-src->svc-t" {
		t.Fatalf("points = %+v", tr.points)
	}
}
