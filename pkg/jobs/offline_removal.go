package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/portinventory"
)

const (
	// OfflineRemovalEvidenceResultField holds the admission-time proof that the
	// servers carrying a stack's port claims are gone, with the owner's
	// confirmation. Only the destroy admission writes it.
	OfflineRemovalEvidenceResultField = "offline_removal_evidence"
	// OfflineRemovalRetiredResultField lists the servers whose custody the
	// destroy retired once the evidence was bound.
	OfflineRemovalRetiredResultField = "offline_removal_retired"
	// OfflineRemovalCustodyResultField maps each retired server to what ending
	// its custody did beyond the tombstone (the Wallet withdrawal).
	OfflineRemovalCustodyResultField = "offline_removal_custody"
	// DestroyWorkspaceStateOfflineRemoval records that the control plane held a
	// workspace but ran no StackKits removal, because the servers it targeted
	// were confirmed gone.
	DestroyWorkspaceStateOfflineRemoval = "offline_removal"

	// OfflineRemovalBasisOwnerConfirmation is the proof class for a host
	// nobody but its owner can vouch for: the server has no lease and no
	// provider custody to read back, its Guard has been silent beyond the
	// stale-heartbeat window, and the owner named it as gone.
	OfflineRemovalBasisOwnerConfirmation = "owner_confirmation"

	offlineRemovalEvidenceVersion = "techstack.offline-stack-removal/v1"
)

// OfflineRemovalServerEvidence is what the control plane observed about one
// server when it admitted the removal. It is secret-free.
type OfflineRemovalServerEvidence struct {
	ServerID            string     `json:"server_id"`
	AgentID             string     `json:"agent_id"`
	ConnectionState     string     `json:"connection_state"`
	LastHeartbeatAt     *time.Time `json:"last_heartbeat_at,omitempty"`
	HeartbeatAgeSeconds int64      `json:"heartbeat_age_seconds"`
	ServerRevision      int64      `json:"server_revision"`
}

// OfflineRemovalEvidence authorizes a destroy to proceed without node-side
// execution on the listed servers. Servers of the stack that are not listed
// keep the typed removal on their node.
type OfflineRemovalEvidence struct {
	SchemaVersion string                         `json:"schema_version"`
	TechstackID   string                         `json:"techstack_id"`
	TenantID      string                         `json:"tenant_id"`
	OwnerID       string                         `json:"owner_id"`
	Basis         string                         `json:"basis"`
	ConfirmedAt   time.Time                      `json:"confirmed_at"`
	Servers       []OfflineRemovalServerEvidence `json:"servers"`
}

// DestroyWorkspaceStateRetiresProjection reports whether a destroy that recorded
// this workspace state retired the control-plane stack projection itself, so no
// later snapshot may write the stack again.
func DestroyWorkspaceStateRetiresProjection(state string) bool {
	state = strings.TrimSpace(state)
	return strings.EqualFold(state, DestroyWorkspaceStateAbsent) || strings.EqualFold(state, DestroyWorkspaceStateOfflineRemoval) ||
		strings.EqualFold(state, DestroyWorkspaceStateNoApplyRemoval)
}

// OfflineServerRetireRequest names one server whose custody ends with the
// removal. The retirer is idempotent: a retried destroy repeats it.
type OfflineServerRetireRequest struct {
	TenantID string
	OwnerID  string
	ServerID string
	AgentID  string
	JobID    string
}

// OfflineServerRetireResult is what ending one server's custody did besides the
// tombstone: the owner's Wallet stops delivering to the node and marks the items
// it held for rotation, as it does for a detached server. It is secret-free.
type OfflineServerRetireResult struct {
	WalletState       string `json:"wallet_state"`
	TargetsRemoved    int    `json:"targets_removed"`
	ItemsNeedRotation int    `json:"items_need_rotation"`
}

// OfflineServerRetirer ends the custody of one confirmed-absent server the way
// an owner's detach does: the worker credential, host-key identity and reach are
// revoked, the aggregate is advanced to its terminal tombstone, the agent is
// disconnected and the Wallet withdraws the node. It performs no provider I/O
// and is safe to repeat; a step that cannot finish is an error, never skipped.
type OfflineServerRetirer func(context.Context, OfflineServerRetireRequest) (OfflineServerRetireResult, error)

// NewOfflineRemovalEvidence builds and validates the evidence for one removal.
func NewOfflineRemovalEvidence(techstackID, tenantID, ownerID string, confirmedAt time.Time, servers []OfflineRemovalServerEvidence) (OfflineRemovalEvidence, error) {
	evidence := OfflineRemovalEvidence{
		SchemaVersion: offlineRemovalEvidenceVersion,
		TechstackID:   strings.TrimSpace(techstackID), TenantID: strings.TrimSpace(tenantID), OwnerID: strings.TrimSpace(ownerID),
		Basis: OfflineRemovalBasisOwnerConfirmation, ConfirmedAt: confirmedAt.UTC(),
		Servers: append([]OfflineRemovalServerEvidence(nil), servers...),
	}
	sort.Slice(evidence.Servers, func(i, j int) bool { return evidence.Servers[i].ServerID < evidence.Servers[j].ServerID })
	if err := evidence.Validate(); err != nil {
		return OfflineRemovalEvidence{}, err
	}
	return evidence, nil
}

func DecodeOfflineRemovalEvidence(value any) (OfflineRemovalEvidence, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return OfflineRemovalEvidence{}, fmt.Errorf("encode offline removal evidence: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var evidence OfflineRemovalEvidence
	if err := decoder.Decode(&evidence); err != nil {
		return OfflineRemovalEvidence{}, fmt.Errorf("decode offline removal evidence: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OfflineRemovalEvidence{}, errors.New("offline removal evidence contains trailing data")
	}
	if err := evidence.Validate(); err != nil {
		return OfflineRemovalEvidence{}, err
	}
	return evidence, nil
}

func (evidence OfflineRemovalEvidence) Validate() error {
	for _, value := range []string{evidence.TechstackID, evidence.TenantID, evidence.OwnerID} {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.New("offline removal evidence identity is incomplete")
		}
	}
	if evidence.SchemaVersion != offlineRemovalEvidenceVersion || evidence.Basis != OfflineRemovalBasisOwnerConfirmation ||
		evidence.ConfirmedAt.IsZero() || len(evidence.Servers) == 0 {
		return errors.New("offline removal evidence is invalid")
	}
	previous := ""
	for _, server := range evidence.Servers {
		if server.ServerID == "" || server.AgentID == "" || server.ServerID != strings.TrimSpace(server.ServerID) ||
			server.AgentID != strings.TrimSpace(server.AgentID) || server.ServerID <= previous || server.HeartbeatAgeSeconds < 0 {
			return errors.New("offline removal evidence servers are not canonical")
		}
		previous = server.ServerID
	}
	return nil
}

// Covers reports whether the evidence names this server.
func (evidence OfflineRemovalEvidence) Covers(serverID string) bool {
	for _, server := range evidence.Servers {
		if server.ServerID == serverID {
			return true
		}
	}
	return false
}

// CoversEveryGeneration reports whether no port-claim generation of the
// snapshot belongs to a server that still needs node-side removal.
func (evidence OfflineRemovalEvidence) CoversEveryGeneration(snapshot portinventory.TeardownSnapshot) bool {
	for _, generation := range snapshot.Generations {
		if !evidence.Covers(generation.ServerID) {
			return false
		}
	}
	return true
}

// boundOfflineRemovalEvidence returns the destroy job's offline-removal
// evidence when it carries any. The evidence must name this stack, tenant and
// owner and only servers that hold port claims in the job's own snapshot, so
// it cannot widen what the snapshot admitted.
func boundOfflineRemovalEvidence(job *Job, snapshot portinventory.TeardownSnapshot) (OfflineRemovalEvidence, bool, error) {
	if job == nil {
		return OfflineRemovalEvidence{}, false, errors.New("offline removal job is missing")
	}
	raw, exists := job.Snapshot().Result[OfflineRemovalEvidenceResultField]
	if !exists {
		return OfflineRemovalEvidence{}, false, nil
	}
	evidence, err := DecodeOfflineRemovalEvidence(raw)
	if err != nil {
		return OfflineRemovalEvidence{}, false, err
	}
	if evidence.TechstackID != job.TargetID || evidence.TenantID != payloadString(job.Payload, tenantIDField) ||
		evidence.OwnerID != payloadString(job.Payload, "owner_id") {
		return OfflineRemovalEvidence{}, false, errors.New("offline removal evidence does not match the destroy job")
	}
	inSnapshot := make(map[string]struct{}, len(snapshot.Generations))
	for _, generation := range snapshot.Generations {
		inSnapshot[generation.ServerID] = struct{}{}
	}
	for _, server := range evidence.Servers {
		if _, held := inSnapshot[server.ServerID]; !held {
			return OfflineRemovalEvidence{}, false, errors.New("offline removal evidence names a server without port claims in this destroy")
		}
	}
	return evidence, true, nil
}

// offlineRemovalServerIDs returns the servers whose node-side removal the
// destroy job's evidence replaces; nil when it carries none or an invalid one.
func offlineRemovalServerIDs(job *Job) map[string]struct{} {
	if job == nil {
		return nil
	}
	raw, exists := job.Snapshot().Result[OfflineRemovalEvidenceResultField]
	if !exists {
		return nil
	}
	evidence, err := DecodeOfflineRemovalEvidence(raw)
	if err != nil {
		return nil
	}
	ids := make(map[string]struct{}, len(evidence.Servers))
	for _, server := range evidence.Servers {
		ids[server.ServerID] = struct{}{}
	}
	return ids
}

// retireOfflineRemovalServers ends the custody of every server the evidence
// names. It runs before the port claims are released, and is safe to repeat.
func retireOfflineRemovalServers(ctx context.Context, cfg *ProvisionConfig, job *Job, evidence OfflineRemovalEvidence) error {
	if cfg == nil || cfg.OfflineServerRetirer == nil {
		return errors.New("offline server retirement is not configured")
	}
	retired := make([]interface{}, 0, len(evidence.Servers))
	custody := make(map[string]interface{}, len(evidence.Servers))
	for _, server := range evidence.Servers {
		outcome, err := cfg.OfflineServerRetirer(ctx, OfflineServerRetireRequest{
			TenantID: evidence.TenantID, OwnerID: evidence.OwnerID,
			ServerID: server.ServerID, AgentID: server.AgentID, JobID: job.ID,
		})
		if err != nil {
			return fmt.Errorf("retire offline server %s: %w", server.ServerID, err)
		}
		retired = append(retired, server.ServerID)
		custody[server.ServerID] = outcome
	}
	job.mutateResult(func(result map[string]interface{}) {
		result[OfflineRemovalRetiredResultField] = retired
		result[OfflineRemovalCustodyResultField] = custody
	})
	return nil
}
