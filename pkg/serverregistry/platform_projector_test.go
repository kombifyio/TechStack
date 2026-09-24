package serverregistry

import (
	"context"
	"errors"
	"testing"
)

type fakeSource struct {
	tenants map[string][]string // afterTenantID -> page
	servers map[string][]ProjectedServer
	err     error
}

func (f *fakeSource) ListServerProjectionTenants(_ context.Context, after string, _ int) ([]string, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.tenants[after], nil
}

func (f *fakeSource) ListProjectedServers(_ context.Context, tenantID string) ([]ProjectedServer, error) {
	return f.servers[tenantID], nil
}

type fakePublisher struct {
	batches  [][]ProjectedServer
	tenants  []string
	failFor  string
	attempts int
}

func (f *fakePublisher) PublishServers(_ context.Context, tenantID string, servers []ProjectedServer) error {
	f.attempts++
	if tenantID == f.failFor {
		return errors.New("publish refused")
	}
	f.tenants = append(f.tenants, tenantID)
	f.batches = append(f.batches, servers)
	return nil
}

func projector(t *testing.T, cfg PlatformProjectorConfig) *PlatformProjector {
	t.Helper()
	p, err := NewPlatformProjector(cfg)
	if err != nil {
		t.Fatalf("NewPlatformProjector: %v", err)
	}
	return p
}

func TestProjectOncePublishesEveryTenantsCurrentServers(t *testing.T) {
	source := &fakeSource{
		tenants: map[string][]string{"": {"tenant-a", "tenant-b"}, "tenant-b": nil},
		servers: map[string][]ProjectedServer{
			"tenant-a": {{ServerID: "s1", TenantID: "tenant-a", AggregateRevision: 7}},
			"tenant-b": {
				{ServerID: "s2", TenantID: "tenant-b", AggregateRevision: 3},
				{ServerID: "s3", TenantID: "tenant-b", AggregateRevision: 4},
			},
		},
	}
	publisher := &fakePublisher{}
	result, err := projector(t, PlatformProjectorConfig{
		Source: source, Publisher: publisher, TenantPageSize: 2,
	}).ProjectOnce(context.Background())
	if err != nil {
		t.Fatalf("ProjectOnce: %v", err)
	}
	if result.Tenants != 2 || result.Servers != 3 || result.Failures != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
}

func TestProjectOnceKeepsGoingWhenOneTenantFails(t *testing.T) {
	// A tenant the platform refused must not stop the sweep: the next pass
	// restates it, because every pass publishes full current state.
	source := &fakeSource{
		tenants: map[string][]string{"": {"tenant-a", "tenant-b"}, "tenant-b": nil},
		servers: map[string][]ProjectedServer{
			"tenant-a": {{ServerID: "s1", TenantID: "tenant-a"}},
			"tenant-b": {{ServerID: "s2", TenantID: "tenant-b"}},
		},
	}
	publisher := &fakePublisher{failFor: "tenant-a"}
	var reported []error
	result, err := projector(t, PlatformProjectorConfig{
		Source: source, Publisher: publisher, TenantPageSize: 2,
		OnError: func(err error) { reported = append(reported, err) },
	}).ProjectOnce(context.Background())
	if err != nil {
		t.Fatalf("ProjectOnce: %v", err)
	}
	if result.Failures != 1 || result.Servers != 1 {
		t.Fatalf("expected one failure and one published server: %#v", result)
	}
	if len(reported) != 1 {
		t.Fatalf("the failure was not reported: %#v", reported)
	}
}

func TestProjectOnceSplitsLargeTenantsIntoBoundedBatches(t *testing.T) {
	many := make([]ProjectedServer, 5)
	for i := range many {
		many[i] = ProjectedServer{ServerID: string(rune('a' + i)), TenantID: "tenant-a"}
	}
	source := &fakeSource{
		tenants: map[string][]string{"": {"tenant-a"}},
		servers: map[string][]ProjectedServer{"tenant-a": many},
	}
	publisher := &fakePublisher{}
	result, err := projector(t, PlatformProjectorConfig{
		Source: source, Publisher: publisher, BatchSize: 2, TenantPageSize: 1,
	}).ProjectOnce(context.Background())
	if err != nil {
		t.Fatalf("ProjectOnce: %v", err)
	}
	if result.Servers != 5 {
		t.Fatalf("expected all five servers published: %#v", result)
	}
	if len(publisher.batches) != 3 {
		t.Fatalf("expected 5 servers in batches of 2 to be three publishes, got %d", len(publisher.batches))
	}
	if len(publisher.batches[0]) != 2 || len(publisher.batches[2]) != 1 {
		t.Fatalf("unexpected batch sizes: %d %d", len(publisher.batches[0]), len(publisher.batches[2]))
	}
}

func TestProjectOnceStopsAtTheTenantBudget(t *testing.T) {
	source := &fakeSource{
		tenants: map[string][]string{"": {"tenant-a", "tenant-b"}},
		servers: map[string][]ProjectedServer{
			"tenant-a": {{ServerID: "s1"}},
			"tenant-b": {{ServerID: "s2"}},
		},
	}
	result, err := projector(t, PlatformProjectorConfig{
		Source: source, Publisher: &fakePublisher{}, TenantPageSize: 2, MaxTenantsPerRun: 1,
	}).ProjectOnce(context.Background())
	if err != nil {
		t.Fatalf("ProjectOnce: %v", err)
	}
	if result.Tenants != 1 {
		t.Fatalf("the per-pass tenant budget was not honoured: %#v", result)
	}
}

func TestNewPlatformProjectorRequiresItsCollaborators(t *testing.T) {
	if _, err := NewPlatformProjector(PlatformProjectorConfig{Publisher: &fakePublisher{}}); err == nil {
		t.Fatal("a projector without a source must not be constructible")
	}
	if _, err := NewPlatformProjector(PlatformProjectorConfig{Source: &fakeSource{}}); err == nil {
		t.Fatal("a projector without a publisher must not be constructible")
	}
}

func TestProjectOnceSpeaksForATenantWithNoServers(t *testing.T) {
	// A tenant the registry knows but holds no servers for must still be
	// published. Otherwise the platform cannot tell an owner with no machines
	// from one the projector has never reached, and the surfaces label every
	// empty account "not projected yet" forever.
	source := &fakeSource{
		tenants: map[string][]string{"": {"tenant-empty"}, "tenant-empty": nil},
		servers: map[string][]ProjectedServer{},
	}
	publisher := &fakePublisher{}
	result, err := projector(t, PlatformProjectorConfig{
		Source: source, Publisher: publisher, TenantPageSize: 2,
	}).ProjectOnce(context.Background())
	if err != nil {
		t.Fatalf("ProjectOnce: %v", err)
	}
	if result.Tenants != 1 || result.Servers != 0 || result.Failures != 0 {
		t.Fatalf("unexpected result: %#v", result)
	}
	if len(publisher.tenants) != 1 || publisher.tenants[0] != "tenant-empty" {
		t.Fatalf("the empty tenant was not published: %#v", publisher.tenants)
	}
	if len(publisher.batches) != 1 || len(publisher.batches[0]) != 0 {
		t.Fatalf("the empty statement carried rows: %#v", publisher.batches)
	}
}

func TestProjectOnceCountsAFailedEmptyStatement(t *testing.T) {
	// The empty statement is a publish like any other: a refusal must be
	// counted and retried, not silently treated as success.
	source := &fakeSource{
		tenants: map[string][]string{"": {"tenant-empty"}, "tenant-empty": nil},
		servers: map[string][]ProjectedServer{},
	}
	result, err := projector(t, PlatformProjectorConfig{
		Source: source, Publisher: &fakePublisher{failFor: "tenant-empty"},
		TenantPageSize: 2, OnError: func(error) {},
	}).ProjectOnce(context.Background())
	if err != nil {
		t.Fatalf("ProjectOnce: %v", err)
	}
	if result.Failures != 1 {
		t.Fatalf("a refused empty statement must count as a failure: %#v", result)
	}
}
