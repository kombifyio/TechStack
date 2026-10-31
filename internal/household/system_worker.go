package household

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agentcontrol"
	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/config"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/logger"
	"github.com/kombifyio/techstack/pkg/orchestrator"
	"github.com/kombifyio/techstack/pkg/releasechannel"
)

// SystemWorker composes actual stores and the existing typed dispatcher. The
// private process owns both connections; no listener receives system custody.
type SystemWorker struct {
	Reducer *SystemReducer
	service *Service
	deliver ActivationDelivery
	orch    *orchestrator.Orchestrator
}

func NewSystemWorker(ctx context.Context, cloud, techstack *sql.DB, relay *agentcontrol.HouseholdRelayClient, deliver ActivationDelivery) (*SystemWorker, error) {
	if relay == nil || deliver == nil {
		return nil, errors.New("household relay and dispatch custody required")
	}
	reducer, err := NewSystemReducer(ctx, cloud)
	if err != nil {
		return nil, err
	}
	store, err := controlplane.NewHouseholdSystemStore(ctx, techstack)
	if err != nil {
		return nil, err
	}
	hub := agentcontrol.NewHub(techstack)
	hub.ConfigureHouseholdRelay(relay)
	cfg := orchestrator.DefaultConfig()
	cfg.Workers = 1
	cfg.StackStore = store
	cfg.WorkerStore = store
	cfg.StackKitCommander = &agentcontrol.HouseholdCommandSender{Hub: hub}
	orch := orchestrator.New(cfg, logger.Default())
	service := &Service{Tenants: store, Homelabs: store, Stacks: store, Links: store, Identity: systemIdentity{orch: orch}}
	return &SystemWorker{Reducer: reducer, service: service, deliver: deliver, orch: orch}, nil
}

func (w *SystemWorker) Close() {
	if w != nil && w.orch != nil {
		w.orch.Stop()
	}
}
func (w *SystemWorker) RunOnce(ctx context.Context) (int, error) {
	if config.ReleaseChannel() == "preview" && !releasechannel.StageOwnerActive() {
		// Before an admitted Stage owner, only the Cloud reducer runs: it commits the
		// canonical snapshot and outbox watermark under the household system role, a
		// database-only step. Every node effect stays with the admitted owner.
		if _, err := w.Reducer.Reduce(ctx); err != nil {
			return 0, err
		}
		return 0, releasechannel.ErrStageOwnerNotAdmitted
	}
	return w.Reducer.ProjectPending(ctx, w.service, w.deliver)
}
func (w *SystemWorker) Run(ctx context.Context, onPass func(int, error)) {
	for {
		n, err := w.RunOnce(ctx)
		if onPass != nil {
			onPass(n, err)
		}
		timer := time.NewTimer(15 * time.Second)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

type systemIdentity struct{ orch *orchestrator.Orchestrator }

func (r systemIdentity) ResolveProjectionTarget(ctx context.Context, t Target) (Target, error) {
	if !releasechannel.AdmitsJobExecution(t.TenantID) {
		return Target{}, releasechannel.ErrTenantNotOwned
	}
	b, err := r.orch.ResolveStackKitIdentityTarget(ctx, t.TenantID, t.OwnerID, t.StackID)
	if err != nil {
		return Target{}, err
	}
	t.AgentID = b.AgentID
	t.StackKitInstanceID = b.StackKitInstanceID
	return t, nil
}
func (r systemIdentity) Project(ctx context.Context, t Target, p IdentityProjection) (Invitation, error) {
	if !releasechannel.AdmitsJobExecution(t.TenantID) {
		return Invitation{}, releasechannel.ErrTenantNotOwned
	}
	if config.ReleaseChannel() == "preview" && !releasechannel.StageOwnerActive() {
		return Invitation{}, releasechannel.ErrStageOwnerNotAdmitted
	}
	result, err := r.orch.RunStackKitIdentityCommand(ctx, orchestrator.StackKitIdentityCommandRequest{
		TenantID: t.TenantID, OwnerID: t.OwnerID, DeploymentID: t.StackID, Operation: agentpb.StackKitOperation_STACKKIT_OPERATION_HOUSEHOLD_PROJECT,
		OwnerApproved: true, ExpectedAgentID: t.AgentID, ExpectedStackKitInstanceID: t.StackKitInstanceID,
		HouseholdProjectionKey: p.Key, HouseholdProjectionRevision: p.Revision, HouseholdProjectionAction: p.Action,
		HouseholdUsername: p.Username, HouseholdEmail: p.Email, HouseholdDisplayName: p.DisplayName, HouseholdApps: p.Apps.Apps, HouseholdAppsSet: p.Apps.Set})
	if err != nil {
		return Invitation{}, err
	}
	defer clear(result.SensitiveResultJson)
	if p.Action == ActionRemove {
		return Invitation{Username: p.Username, Status: "removed"}, nil
	}
	var envelope struct {
		Data json.RawMessage `json:"data"`
	}
	var data struct {
		Username  string `json:"username"`
		Status    string `json:"status"`
		SetupURL  string `json:"setupURL"`
		ExpiresAt string `json:"expiresAt"`
	}
	if json.Unmarshal(result.SensitiveResultJson, &envelope) != nil || json.Unmarshal(envelope.Data, &data) != nil {
		return Invitation{}, errors.New("household enrollment response invalid")
	}
	defer clear(envelope.Data)
	invite := Invitation{Username: data.Username, Status: data.Status, ActivationURL: data.SetupURL}
	if expires, err := time.Parse(time.RFC3339, strings.TrimSpace(data.ExpiresAt)); err == nil {
		invite.ExpiresAt = &expires
	}
	return invite, nil
}

// The private worker has no legacy unfenced mutation surface.
func (systemIdentity) Invite(context.Context, Target, string, string, string, AppGrant) (Invitation, error) {
	return Invitation{}, errors.New("revision required")
}
func (systemIdentity) Remove(context.Context, Target, string) error {
	return errors.New("revision required")
}
func (systemIdentity) UpdateApps(context.Context, Target, string, AppGrant) error {
	return errors.New("revision required")
}
