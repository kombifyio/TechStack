package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/jobs"
)

// Reason codes of a refused offline removal. They are the contract callers
// branch on; the destroy route returns them as `reason_code`.
const (
	// ReasonServerAbsenceConfirmationRequired: a server holding the stack's
	// port claims is offline and the request did not say it is gone.
	ReasonServerAbsenceConfirmationRequired = "server_absence_confirmation_required"
	// ReasonOfflineRemovalInvalid: the confirmation is empty, repeats or names
	// a server this stack does not hold port claims on.
	ReasonOfflineRemovalInvalid = "offline_removal_confirmation_invalid"
	// ReasonOfflineRemovalUnconfirmed: an offline server was left out.
	ReasonOfflineRemovalUnconfirmed = "offline_removal_unconfirmed_server"
	// ReasonOfflineRemovalServerOnline: a confirmed server reported recently,
	// so it is not provably gone; the normal removal runs on its node.
	ReasonOfflineRemovalServerOnline = "offline_removal_server_online"
	// ReasonOfflineRemovalProviderOperated: a confirmed server is leased or
	// operated by kombify or a provider; provider read-back proves its absence.
	ReasonOfflineRemovalProviderOperated = "offline_removal_provider_operated"
	// ReasonOfflineRemovalNoCustody: the server has no approved agent of this
	// stack whose custody could end.
	ReasonOfflineRemovalNoCustody = "offline_removal_server_not_bound"
	// ReasonOfflineRemovalUnavailable: the server authority is not configured.
	ReasonOfflineRemovalUnavailable = "offline_removal_unavailable"
)

// OfflineRemovalRequest is the owner's statement, made with a destroy request,
// that the named servers no longer exist. It is evidence, not authority: the
// control plane still proves each server offline and owner-operated.
type OfflineRemovalRequest struct {
	ConfirmServerIDs []string
}

// OfflineServerAuthority reads the canonical server projection and ends the
// custody of a server the owner confirmed gone. *controlplane.PostgresStore
// satisfies it; without it no removal can be classified offline.
type OfflineServerAuthority interface {
	GetServerRuntime(ctx context.Context, tenantID, serverID string) (*controlplane.ServerRuntime, error)
	controlplane.AbsentOwnerServerRetirer
}

// OfflineServerSummary names one offline server in a refusal.
type OfflineServerSummary struct {
	ID                  string     `json:"id"`
	Name                string     `json:"name"`
	LastHeartbeatAt     *time.Time `json:"last_heartbeat_at,omitempty"`
	HeartbeatAgeSeconds int64      `json:"heartbeat_age_seconds"`
}

// OfflineRemovalError is a destroy admission refusal about servers that are,
// or may not be, gone. Servers lists those the code is about.
type OfflineRemovalError struct {
	Code    string
	Servers []OfflineServerSummary
	Message string
}

func (e *OfflineRemovalError) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// offlineRemovalServerIDs returns the distinct servers holding the stack's port
// claims, sorted.
func offlineRemovalServerIDs(snapshot portinventory.TeardownSnapshot) []string {
	seen := make(map[string]struct{}, len(snapshot.Generations))
	ids := make([]string, 0, len(snapshot.Generations))
	for _, generation := range snapshot.Generations {
		if _, exists := seen[generation.ServerID]; exists {
			continue
		}
		seen[generation.ServerID] = struct{}{}
		ids = append(ids, generation.ServerID)
	}
	sort.Strings(ids)
	return ids
}

// offlineRemovalEvidence decides, for one destroy of a local stack, which of the
// servers holding its port claims are provably gone, and records the owner's
// confirmation for them.
//
// A server is a candidate only when the control plane itself can say nobody
// else vouches for it: no lease, not kombify- or provider-operated, and its
// Guard silent beyond the stale-heartbeat window, recomputed here from the last
// heartbeat. A connected node therefore always keeps the normal removal on its
// node, and a leased or provider-operated server never takes the owner's word:
// its absence is proven by provider-control read-back on the managed path. An
// offline candidate without the owner's exact confirmation refuses the destroy
// with the servers to confirm; a confirmation naming any other server refuses
// it too. The returned evidence is nil when no server is offline.
func (o *Orchestrator) offlineRemovalEvidence(ctx context.Context, stack *orchestratorStack, snapshot portinventory.TeardownSnapshot, request *OfflineRemovalRequest) (*jobs.OfflineRemovalEvidence, error) {
	serverIDs := offlineRemovalServerIDs(snapshot)
	if o.offlineServers == nil {
		if request != nil {
			return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalUnavailable,
				Message: "server absence cannot be verified right now; retry shortly"}
		}
		return nil, nil
	}
	now := o.now().UTC()
	approved, err := o.approvedStackKitLifecycleAgentBindings(ctx, stack)
	if err != nil {
		return nil, fmt.Errorf("read the approved agents of the stack: %w", err)
	}

	type classified struct {
		summary  OfflineServerSummary
		evidence jobs.OfflineRemovalServerEvidence
	}
	offline := map[string]classified{}
	providerOperated := map[string]struct{}{}
	online := map[string]struct{}{}
	for _, serverID := range serverIDs {
		server, readErr := o.offlineServers.GetServerRuntime(ctx, stack.tenantID, serverID)
		if readErr != nil {
			return nil, fmt.Errorf("read server %s before destroy: %w", serverID, readErr)
		}
		if strings.TrimSpace(server.OwnerSubjectID) != stack.ownerID {
			return nil, fmt.Errorf("server %s does not belong to the owner of stack %s", serverID, stack.id)
		}
		switch checkErr := controlplane.CheckOwnerServerAbsentEligible(*server, now); {
		case checkErr == nil:
			agentID := strings.TrimSpace(server.WorkerID)
			if !stackOwnsServerAgent(approved, serverID, agentID) {
				return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalNoCustody,
					Servers: []OfflineServerSummary{summarizeOfflineServer(*server, now)},
					Message: "no approved agent of this stack holds the server, so its custody cannot end"}
			}
			item := classified{summary: summarizeOfflineServer(*server, now)}
			item.evidence = jobs.OfflineRemovalServerEvidence{
				ServerID: serverID, AgentID: agentID, ConnectionState: strings.TrimSpace(server.ConnectionState),
				LastHeartbeatAt: server.LastHeartbeatAt, HeartbeatAgeSeconds: item.summary.HeartbeatAgeSeconds,
				ServerRevision: server.Revision,
			}
			offline[serverID] = item
		case errors.Is(checkErr, controlplane.ErrServerAbsenceUnsupported):
			providerOperated[serverID] = struct{}{}
		default:
			online[serverID] = struct{}{}
		}
	}

	if request == nil {
		if len(offline) == 0 {
			return nil, nil
		}
		summaries := make([]OfflineServerSummary, 0, len(offline))
		for _, serverID := range serverIDs {
			if item, exists := offline[serverID]; exists {
				summaries = append(summaries, item.summary)
			}
		}
		return nil, &OfflineRemovalError{Code: ReasonServerAbsenceConfirmationRequired, Servers: summaries,
			Message: "a server of this stack is offline; if it no longer exists, confirm it by its id to remove the stack without it"}
	}

	confirmed := make(map[string]struct{}, len(request.ConfirmServerIDs))
	for _, raw := range request.ConfirmServerIDs {
		serverID := strings.TrimSpace(raw)
		if serverID == "" {
			return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalInvalid, Message: "confirm_server_ids contains an empty server id"}
		}
		if _, repeated := confirmed[serverID]; repeated {
			return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalInvalid, Message: "confirm_server_ids repeats a server id"}
		}
		confirmed[serverID] = struct{}{}
	}
	if len(confirmed) == 0 {
		return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalInvalid, Message: "confirm_server_ids must name every offline server"}
	}
	known := make(map[string]struct{}, len(serverIDs))
	for _, serverID := range serverIDs {
		known[serverID] = struct{}{}
	}
	for serverID := range confirmed {
		if _, held := known[serverID]; !held {
			return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalInvalid, Message: "a confirmed server holds no port claims of this stack"}
		}
		if _, provider := providerOperated[serverID]; provider {
			return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalProviderOperated,
				Message: "a confirmed server is leased or provider-operated; its provider proves its absence"}
		}
		if _, reachable := online[serverID]; reachable {
			return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalServerOnline,
				Message: "a confirmed server reported within the last few minutes or has not reported yet, so it is not provably offline; the stack is removed through its node"}
		}
	}
	missing := make([]OfflineServerSummary, 0)
	servers := make([]jobs.OfflineRemovalServerEvidence, 0, len(offline))
	for _, serverID := range serverIDs {
		item, exists := offline[serverID]
		if !exists {
			continue
		}
		if _, ok := confirmed[serverID]; !ok {
			missing = append(missing, item.summary)
			continue
		}
		servers = append(servers, item.evidence)
	}
	if len(missing) > 0 {
		return nil, &OfflineRemovalError{Code: ReasonOfflineRemovalUnconfirmed, Servers: missing,
			Message: "every offline server of the stack must be confirmed"}
	}
	evidence, evidenceErr := jobs.NewOfflineRemovalEvidence(stack.id, stack.tenantID, stack.ownerID, now, servers)
	if evidenceErr != nil {
		return nil, evidenceErr
	}
	return &evidence, nil
}

// precheckOfflineRemoval answers, without side effects, the refusals that
// depend only on the state of the stack's servers and on the owner's
// confirmation. It runs before the destroy cancels the stack's pending work, so
// a refused request leaves every job as it was; the same classification runs
// again on the authoritative snapshot once the stack is quiet. A confirmation
// that has no server to apply to is refused too, never silently ignored.
func (o *Orchestrator) precheckOfflineRemoval(ctx context.Context, stack *orchestratorStack, managed bool, request *OfflineRemovalRequest) error {
	nothingToConfirm := func(message string) error {
		return &OfflineRemovalError{Code: ReasonOfflineRemovalInvalid, Message: message}
	}
	if managed || o.cfg.PortInventory == nil {
		if request != nil {
			return nothingToConfirm("this stack has no owner server to confirm; its infrastructure is removed through its provider")
		}
		return nil
	}
	snapshot, err := o.cfg.PortInventory.SnapshotForTeardown(ctx, portinventory.TeardownSnapshotRequest{
		TenantID: stack.tenantID, OwnerSubjectID: stack.ownerID, TechstackID: stack.id,
	})
	if err == nil {
		err = portinventory.ValidateTeardownSnapshot(snapshot)
	}
	if err != nil {
		// The authoritative snapshot below reports it.
		return nil
	}
	if len(snapshot.Generations) == 0 {
		if request != nil {
			return nothingToConfirm("no server of this stack holds port claims, so there is nothing to confirm")
		}
		return nil
	}
	_, classifyErr := o.offlineRemovalEvidence(ctx, stack, snapshot, request)
	var refusal *OfflineRemovalError
	if errors.As(classifyErr, &refusal) {
		return classifyErr
	}
	return nil
}

func stackOwnsServerAgent(approved []stackKitLifecycleAgentBinding, serverID, workerID string) bool {
	if workerID == "" {
		return false
	}
	for _, binding := range approved {
		if binding.ServerID == serverID && binding.WorkerID == workerID {
			return true
		}
	}
	return false
}

func summarizeOfflineServer(server controlplane.ServerRuntime, now time.Time) OfflineServerSummary {
	summary := OfflineServerSummary{
		ID:   server.ID,
		Name: firstNonEmptyString(server.DisplayName, server.Name, server.ID),
	}
	if server.LastHeartbeatAt != nil && !server.LastHeartbeatAt.IsZero() {
		at := server.LastHeartbeatAt.UTC()
		summary.LastHeartbeatAt = &at
		if age := now.Sub(at); age > 0 {
			summary.HeartbeatAgeSeconds = int64(age / time.Second)
		}
	}
	return summary
}

// offlineServerWalletTimeout bounds the Wallet call that follows the tombstone.
const offlineServerWalletTimeout = 10 * time.Second

// ConfigureOfflineServerDisconnect late-binds the gRPC hub's agent removal,
// which an owner's detach also runs once custody has ended. Without it a retired
// server's agent is only refused at its next call.
func (o *Orchestrator) ConfigureOfflineServerDisconnect(disconnect func(agentID string) error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.offlineDisconnect = disconnect
}

// retireOfflineServer ends the custody of one server the destroy job's evidence
// names, as an owner's detach does. The store re-checks, in its own transaction
// and on database time, that the server is still offline; a server that came
// back stops the removal. After the tombstone the agent is disconnected and the
// owner's Wallet withdraws the node, marking the items it held for rotation: a
// Wallet that cannot answer is an error, so the destroy fails with its claims
// retained and a retry repeats the withdrawal.
func (o *Orchestrator) retireOfflineServer(ctx context.Context, request jobs.OfflineServerRetireRequest) (jobs.OfflineServerRetireResult, error) {
	if o == nil || o.offlineServers == nil {
		return jobs.OfflineServerRetireResult{}, errors.New("offline server authority is not configured")
	}
	receipt, err := o.offlineServers.RetireAbsentOwnerServer(ctx, controlplane.AbsentOwnerServerRetireRequest{
		TenantID: request.TenantID, OwnerSubjectID: request.OwnerID,
		ServerID: request.ServerID, AgentID: request.AgentID, EvidenceRef: request.JobID,
	})
	if err != nil {
		return jobs.OfflineServerRetireResult{}, err
	}
	o.mu.RLock()
	disconnect := o.offlineDisconnect
	o.mu.RUnlock()
	if disconnect != nil && receipt != nil {
		// Custody has ended, so a failed disconnect leaves nothing that can act.
		_ = disconnect(receipt.AgentID)
	}
	if !o.kombifyWallet.Enabled() {
		return jobs.OfflineServerRetireResult{WalletState: "not_configured"}, nil
	}
	walletCtx, cancel := context.WithTimeout(ctx, offlineServerWalletTimeout)
	defer cancel()
	withdrawn, err := o.kombifyWallet.HostWithdraw(walletCtx, request.OwnerID, request.AgentID)
	if err != nil {
		return jobs.OfflineServerRetireResult{}, fmt.Errorf("withdraw the server from the owner's Wallet: %w", err)
	}
	return jobs.OfflineServerRetireResult{
		WalletState: "recorded", TargetsRemoved: withdrawn.TargetsRemoved, ItemsNeedRotation: withdrawn.NeedsRotation,
	}, nil
}
