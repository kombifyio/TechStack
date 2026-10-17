package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/homelabdigest"
	"github.com/kombifyio/techstack/internal/household"
	productnotifications "github.com/kombifyio/techstack/internal/notifications"
	"github.com/kombifyio/techstack/internal/routes"
	"github.com/kombifyio/techstack/internal/usernotice"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/grpcserver"
	"github.com/kombifyio/techstack/pkg/guardposture"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	pkgusernotice "github.com/kombifyio/techstack/pkg/usernotice"
)

// The Companion Feed producers of Techstack: the household projection and the
// D10 link (Cloud household), the Homelab digest contribution, and the opt-in
// smart-home notices a node reports. They need the Postgres control plane; an
// edition without it has no household and no notices, and says so (fail closed).

const householdLinkClientIDEnv = "TECHSTACK_HOUSEHOLD_LINK_CLIENT_ID"

type companionFeedBoot struct {
	household   *household.Service
	userNotices *usernotice.Service
	nodes       pkgusernotice.NodeResolver
	digest      *homelabdigest.Job
}

func bootCompanionFeed(v2 *v2Boot, orch *orchestrator.Orchestrator, outbox *productnotifications.Outbox, grpcSrv *grpcserver.Server, posture *guardposture.Service, log *logger.Logger) *companionFeedBoot {
	store := controlPlanePostgresStore(v2)
	if store == nil {
		return nil
	}
	feed := &companionFeedBoot{
		household: &household.Service{
			Tenants:      feedOwnerTenants{authorize: runtimeSummaryAuthorizationContext(store)},
			Homelabs:     store,
			Stacks:       store,
			Links:        store,
			Identity:     feedIdentityRuntime{orch: orch},
			Issuers:      feedIssuers{services: store},
			Verifier:     &household.OIDCVerifier{AllowPrivateNetworks: !v2.saasMode},
			LinkClientID: strings.TrimSpace(os.Getenv(householdLinkClientIDEnv)),
		},
		nodes: feedNodeResolver{workers: store},
	}
	if outbox != nil {
		feed.userNotices = &usernotice.Service{Admission: store, Links: store, Stacks: store, Outbox: outbox}
		if grpcSrv != nil {
			grpcSrv.SetUserNotices(feed.userNotices, feed.nodes)
		}
	} else {
		log.Warn("user_notices_disabled", "reason", "notification outbox unavailable")
	}
	engine := productnotifications.NewEngineFromEnv()
	if engine.Configured() {
		feed.digest = &homelabdigest.Job{
			Source: &feedDigestSource{
				tenants: store, servers: store, jobs: store, maintenance: store, posture: posture,
			},
			Sender: engine,
			Logger: log.Logger,
		}
	} else {
		log.Warn("homelab_digest_disabled", "reason", "notifications engine not configured")
	}
	return feed
}

// feedOwnerTenants resolves the tenant of an owner subject through the same
// verified-membership rule as the Cloud runtime summary.
type feedOwnerTenants struct {
	authorize func(context.Context, string, string) (context.Context, string, error)
}

func (o feedOwnerTenants) ResolveOwnerTenant(ctx context.Context, hint, owner string) (string, error) {
	owner = strings.TrimSpace(owner)
	tenantID := strings.TrimSpace(hint)
	if tenantID == "" {
		tenantID = canonicalOwnerTenantID(owner)
	}
	_, resolved, err := o.authorize(ctx, tenantID, owner)
	if errors.Is(err, routes.ErrInventoryAccessDenied) {
		return "", household.ErrOwnerNotAuthorized
	}
	if err != nil {
		return "", err
	}
	return resolved, nil
}

// feedIdentityRuntime runs the owner-approved StackKit identity operations on
// the node through the orchestrator's typed command channel.
type feedIdentityRuntime struct{ orch *orchestrator.Orchestrator }

func (r feedIdentityRuntime) Invite(ctx context.Context, target household.Target, username, email, displayName string, apps household.AppGrant) (household.Invitation, error) {
	result, err := r.orch.RunStackKitIdentityCommand(ctx, orchestrator.StackKitIdentityCommandRequest{
		TenantID: target.TenantID, OwnerID: target.OwnerID, DeploymentID: target.StackID,
		Operation:     agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_INVITE,
		OwnerApproved: true, HouseholdUsername: username, HouseholdEmail: email, HouseholdDisplayName: displayName,
		HouseholdApps: apps.Apps, HouseholdAppsSet: apps.Set,
	})
	if err != nil {
		return household.Invitation{}, err
	}
	raw := append([]byte(nil), result.SensitiveResultJson...)
	clear(result.SensitiveResultJson)
	result.SensitiveResultJson = nil
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	var data struct {
		Username  string `json:"username"`
		Status    string `json:"status"`
		ExpiresAt string `json:"expiresAt"`
		SetupURL  string `json:"setupURL"`
	}
	if json.Unmarshal(raw, &envelope) != nil || json.Unmarshal(envelope.Data, &data) != nil {
		return household.Invitation{}, errors.New("decode StackKits household invitation")
	}
	invitation := household.Invitation{Username: data.Username, Status: data.Status, ActivationURL: data.SetupURL}
	if expires, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(data.ExpiresAt)); parseErr == nil {
		expires = expires.UTC()
		invitation.ExpiresAt = &expires
	}
	return invitation, nil
}

func (r feedIdentityRuntime) Remove(ctx context.Context, target household.Target, username string) error {
	_, err := r.orch.RunStackKitIdentityCommand(ctx, orchestrator.StackKitIdentityCommandRequest{
		TenantID: target.TenantID, OwnerID: target.OwnerID, DeploymentID: target.StackID,
		Operation:     agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_REMOVE,
		OwnerApproved: true, HouseholdUsername: username,
	})
	return err
}

func (r feedIdentityRuntime) UpdateApps(ctx context.Context, target household.Target, username string, apps household.AppGrant) error {
	_, err := r.orch.RunStackKitIdentityCommand(ctx, orchestrator.StackKitIdentityCommandRequest{
		TenantID: target.TenantID, OwnerID: target.OwnerID, DeploymentID: target.StackID,
		Operation:     agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_UPDATE_APPS,
		OwnerApproved: true, HouseholdUsername: username, HouseholdApps: apps.Apps, HouseholdAppsSet: apps.Set,
	})
	return err
}

// feedIssuers finds the Pocket ID origin of a stack in Techstack's own service
// inventory. Only an https origin is admitted as an OIDC issuer.
type feedIssuers struct {
	services controlplane.ServiceRuntimeStore
}

func (i feedIssuers) PocketIDIssuer(ctx context.Context, tenantID, stackID string) (string, error) {
	services, err := i.services.ListServiceRuntimes(ctx, tenantID, stackID, "")
	if err != nil {
		return "", err
	}
	for _, service := range services {
		switch strings.ToLower(strings.TrimSpace(service.ServiceKey)) {
		case "pocketid", "pocket-id", "pocket_id":
		default:
			continue
		}
		raw, _ := service.Access["url"].(string)
		parsed, parseErr := url.Parse(strings.TrimSpace(raw))
		if parseErr != nil || parsed.Scheme != "https" || parsed.Host == "" {
			continue
		}
		return "https://" + parsed.Host, nil
	}
	return "", fmt.Errorf("stack %s has no https Pocket ID service", stackID)
}

// feedNodeResolver maps an authenticated agent to its node: the stack and the
// owner of the worker record. The notice never names either.
type feedNodeResolver struct{ workers controlplane.WorkerStore }

func (r feedNodeResolver) ResolveNode(ctx context.Context, tenantID, agentID string) (pkgusernotice.Node, error) {
	worker, err := r.workers.GetWorker(ctx, tenantID, agentID)
	if err != nil {
		return pkgusernotice.Node{}, err
	}
	if worker == nil || strings.TrimSpace(worker.StackID) == "" || strings.TrimSpace(worker.OwnerSubjectID) == "" {
		return pkgusernotice.Node{}, errors.New("worker has no stack or owner")
	}
	return pkgusernotice.Node{TenantID: tenantID, StackID: worker.StackID, OwnerSubjectID: worker.OwnerSubjectID, AgentID: agentID}, nil
}

// feedDigestSource reads the Homelab numbers from the canonical stores: server
// health, the day's backup jobs, the newest OS-update plan and the open Guard
// findings.
type feedDigestSource struct {
	tenants interface {
		ListServerProjectionTenants(ctx context.Context, afterTenantID string, limit int) ([]string, error)
	}
	servers     controlplane.ServerRuntimeStore
	jobs        controlplane.JobStore
	maintenance controlplane.LatestServerMaintenanceReader
	posture     *guardposture.Service
}

func (s *feedDigestSource) Snapshot(ctx context.Context, dayStart time.Time) ([]homelabdigest.OwnerFacts, error) {
	byOwner := map[string]*homelabdigest.Facts{}
	var order []string
	after := ""
	for {
		tenants, err := s.tenants.ListServerProjectionTenants(ctx, after, 100)
		if err != nil {
			return nil, err
		}
		for _, tenantID := range tenants {
			if err := s.addTenant(ctx, tenantID, dayStart, byOwner, &order); err != nil {
				return nil, err
			}
		}
		if len(tenants) < 100 {
			break
		}
		after = tenants[len(tenants)-1]
	}
	out := make([]homelabdigest.OwnerFacts, 0, len(order))
	for _, owner := range order {
		out = append(out, homelabdigest.OwnerFacts{OwnerSubject: owner, Facts: *byOwner[owner]})
	}
	return out, nil
}

func (s *feedDigestSource) addTenant(ctx context.Context, tenantID string, dayStart time.Time, byOwner map[string]*homelabdigest.Facts, order *[]string) error {
	servers, err := s.servers.ListServerRuntimesByTenant(ctx, tenantID, "")
	if err != nil {
		return err
	}
	stacksByOwner := map[string]map[string]struct{}{}
	for _, server := range servers {
		owner := strings.TrimSpace(server.OwnerSubjectID)
		if owner == "" || server.DecommissionedAt != nil {
			continue
		}
		facts := byOwner[owner]
		if facts == nil {
			facts = &homelabdigest.Facts{}
			byOwner[owner] = facts
			*order = append(*order, owner)
		}
		facts.ServersTotal++
		if server.ConnectionState == "connected" && server.HealthState == "healthy" {
			facts.ServersOK++
		}
		if stack := strings.TrimSpace(server.StackID); stack != "" {
			if stacksByOwner[owner] == nil {
				stacksByOwner[owner] = map[string]struct{}{}
			}
			stacksByOwner[owner][stack] = struct{}{}
		}
		facts.UpdatesReady += s.pendingUpdates(ctx, tenantID, server.ID)
		open, critical := s.openFindings(ctx, tenantID, server.WorkerID)
		facts.OpenFindings += open
		facts.CriticalFindings += critical
	}
	dayEnd := dayStart.Add(24 * time.Hour)
	for owner, stacks := range stacksByOwner {
		for stackID := range stacks {
			jobs, err := s.jobs.ListJobsByStack(ctx, tenantID, stackID, 100)
			if err != nil {
				return err
			}
			for _, job := range jobs {
				if job.Type != "backup" || job.CompletedAt == nil || job.CompletedAt.Before(dayStart) || !job.CompletedAt.Before(dayEnd) {
					continue
				}
				switch job.State {
				case "completed":
					byOwner[owner].BackupsOK++
				case "failed":
					byOwner[owner].BackupsFailed++
				}
			}
		}
	}
	return nil
}

// pendingUpdates is the pending package count of the server's newest completed
// OS-update plan; an OS update completed after the plan leaves nothing pending.
func (s *feedDigestSource) pendingUpdates(ctx context.Context, tenantID, serverID string) int {
	if s.maintenance == nil || serverID == "" {
		return 0
	}
	job, err := s.maintenance.LatestCompletedServerMaintenance(ctx, tenantID, serverID)
	if err != nil || job == nil || job.Action != controlplane.ServerMaintenanceActionPlan {
		return 0
	}
	switch count := job.Result["pending_count"].(type) {
	case float64:
		return int(count)
	case int:
		return count
	case int64:
		return int(count)
	}
	return 0
}

func (s *feedDigestSource) openFindings(ctx context.Context, tenantID, workerID string) (open, critical int) {
	if s.posture == nil || workerID == "" {
		return 0, 0
	}
	findings, err := s.posture.Findings(ctx, tenantID, workerID, 200)
	if err != nil {
		return 0, 0
	}
	for _, finding := range findings {
		if finding.ResolvedAt != nil {
			continue
		}
		open++
		if finding.Severity == "critical" {
			critical++
		}
	}
	return open, critical
}

// userNoticeSink is the smart-home notice sink of the HTTPS worker route. A nil
// interface, not a typed nil, so the route answers "unavailable" itself.
func userNoticeSink(deps routeDeps) pkgusernotice.Sink {
	if deps.feed == nil || deps.feed.userNotices == nil {
		return nil
	}
	return deps.feed.userNotices
}

// registerHouseholdRoutes mounts the private Cloud household routes. Without
// the Postgres control plane or the shared service secret they answer 503.
func registerHouseholdRoutes(router *httpx.Router, deps routeDeps) {
	cfg := routes.HouseholdRouteConfig{
		ServiceAuthSecret: os.Getenv("SERVICE_AUTH_SECRET"),
		ServiceAuthNext:   os.Getenv("SERVICE_AUTH_SECRET_NEXT"),
	}
	if deps.feed != nil {
		cfg.Service = deps.feed.household
	}
	routes.RegisterHouseholdInternalRoutes(router, cfg)
}
