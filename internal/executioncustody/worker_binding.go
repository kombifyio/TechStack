package executioncustody

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/db"
	"github.com/kombifyio/techstack/pkg/guardbinding"
	"google.golang.org/protobuf/encoding/protojson"
)

type workerBindingRequest struct {
	TenantID   string
	WorkerID   string
	Binding    *guardbinding.Binding
	LocalGrant *guardbinding.LocalRepairGrant
	// Predecessor asks for a result-only predecessor recovery statement. The
	// provider recomputes its scope and lifetime; only the request's tenant,
	// worker, server and candidate commands are taken from the caller.
	Predecessor *guardbinding.PredecessorRecovery
}

// WorkerBindingSigner retains public pins and request cancellation only. The
// existing per-worker signer and its derived private key stay in native custody.
type WorkerBindingSigner struct {
	remote         *Remote
	ctx            context.Context
	tenant, worker string
	pins           []guardbinding.PinnedKey
}

func (r *Remote) WorkerBindingSigner(ctx context.Context, tenant, worker string) (*WorkerBindingSigner, error) {
	if tenant != r.metadata.Grant.TenantID || worker == "" {
		return nil, errors.New("worker binding scope denied")
	}
	var pins []guardbinding.PinnedKey
	if err := r.call(ctx, "worker-binding/pins", workerBindingRequest{TenantID: tenant, WorkerID: worker}, &pins); err != nil {
		return nil, err
	}
	if len(pins) != 1 {
		return nil, errors.New("worker binding public pin unavailable")
	}
	public, err := base64.StdEncoding.Strict().DecodeString(pins[0].PublicKey)
	if err != nil || len(public) != 32 || guardbinding.KeyIDOf(public) != pins[0].KeyID {
		return nil, errors.New("worker binding public pin invalid")
	}
	return &WorkerBindingSigner{remote: r, ctx: ctx, tenant: tenant, worker: worker, pins: pins}, nil
}
func (s *WorkerBindingSigner) PublicKeys() []guardbinding.PinnedKey {
	return append([]guardbinding.PinnedKey{}, s.pins...)
}
func (s *WorkerBindingSigner) Sign(binding *guardbinding.Binding) error {
	if binding == nil || binding.TenantID != s.tenant || binding.WorkerID != s.worker {
		return errors.New("worker binding scope denied")
	}
	var result guardbinding.Binding
	if err := s.remote.call(s.ctx, "worker-binding/sign", workerBindingRequest{TenantID: s.tenant, WorkerID: s.worker, Binding: binding}, &result); err != nil {
		return err
	}
	if result.VerifySignature(s.pins) != nil {
		return errors.New("worker binding signature does not match retained pin")
	}
	*binding = result
	return nil
}
func (s *WorkerBindingSigner) SignLocalGrant(grant *guardbinding.LocalRepairGrant) error {
	if grant == nil || grant.TenantID != s.tenant || grant.WorkerID != s.worker {
		return errors.New("local repair scope denied")
	}
	var result guardbinding.LocalRepairGrant
	if err := s.remote.call(s.ctx, "worker-binding/local-grant", workerBindingRequest{TenantID: s.tenant, WorkerID: s.worker, LocalGrant: grant}, &result); err != nil {
		return err
	}
	if result.VerifySignature(s.pins) != nil {
		return errors.New("local repair signature does not match retained pin")
	}
	*grant = result
	return nil
}

func (s *WorkerBindingSigner) SignPredecessorRecovery(recovery *guardbinding.PredecessorRecovery) error {
	if recovery == nil || recovery.TenantID != s.tenant || recovery.WorkerID != s.worker {
		return errors.New("predecessor recovery scope denied")
	}
	var result guardbinding.PredecessorRecovery
	if err := s.remote.call(s.ctx, "worker-binding/predecessor-recovery", workerBindingRequest{TenantID: s.tenant, WorkerID: s.worker, Predecessor: recovery}, &result); err != nil {
		return err
	}
	expect := guardbinding.Expectation{TenantID: s.tenant, WorkerID: s.worker, ServerID: recovery.ServerID}
	if result.Verify(s.pins, expect, time.Now()) != nil {
		return errors.New("predecessor recovery signature does not match retained pin")
	}
	*recovery = result
	return nil
}

func (p *Provider) serveWorkerBinding(w http.ResponseWriter, r *http.Request, grant CallerGrant, request workerBindingRequest) {
	deny := func() { http.Error(w, "worker binding scope denied", http.StatusForbidden) }
	if request.TenantID != grant.TenantID || request.WorkerID == "" || len(p.BindingBase) == 0 {
		deny()
		return
	}
	worker, err := controlplane.NewPostgresStore(p.AuditDatabase).GetWorker(r.Context(), request.TenantID, request.WorkerID)
	if err != nil || worker == nil || worker.OwnerSubjectID != grant.OwnerID || worker.TenantID != grant.TenantID {
		deny()
		return
	}
	if worker.StackID != "" && p.scopeDeployment(r.Context(), grant, grant.TenantID, worker.StackID, grant.OwnerID) != nil {
		deny()
		return
	}
	signer, err := guardbinding.DeriveWorkerSigner(p.BindingBase, grant.TenantID, worker.ID)
	if err != nil {
		deny()
		return
	}
	if r.URL.Path == remotePath+"worker-binding/pins" {
		_ = json.NewEncoder(w).Encode(signer.PublicKeys())
		return
	}
	credential, err := controlplane.WorkerCredentialStateFromWorker(*worker)
	if err != nil || credential.Generation < 1 {
		deny()
		return
	}
	if binding := request.Binding; binding != nil && r.URL.Path == remotePath+"worker-binding/sign" {
		if binding.TenantID != grant.TenantID || binding.WorkerID != worker.ID || binding.WorkerGeneration != credential.Generation || binding.CurrentGeneration != credential.Generation || binding.ContractVersion != guardbinding.ContractVersion || !binding.AuthorityExpiresAt.After(time.Now()) {
			deny()
			return
		}
		if binding.Operation == "runtime.install" {
			canonical, err := p.candidateInstallBinding(r.Context(), grant, worker, binding, credential.Generation)
			if err != nil || signer.Sign(canonical) != nil {
				deny()
				return
			}
			_ = json.NewEncoder(w).Encode(canonical)
			return
		}
		tx, err := db.BeginTenantTransaction(r.Context(), p.AuditDatabase, grant.TenantID)
		if err != nil {
			deny()
			return
		}
		defer tx.Rollback()
		var raw []byte
		var digest, contract, state string
		var generation int64
		var expires time.Time
		var server string
		var reoffer bool
		err = tx.QueryRowContext(r.Context(), `SELECT c.command_json,c.payload_digest,c.worker_generation,c.authority_expires_at,c.contract_version,c.state,s.id,COALESCE(c.last_offered_at>c.dispatched_at,false) FROM typed_agent_commands c JOIN servers s ON s.tenant_id=c.tenant_id AND s.worker_id=c.agent_id WHERE c.tenant_id=$1 AND c.agent_id=$2 AND c.command_id=$3 AND c.command_family='stackkit' AND s.id=$4 AND s.owner_subject_id=$5`, grant.TenantID, worker.ID, binding.CommandID, binding.ServerID, grant.OwnerID).Scan(&raw, &digest, &generation, &expires, &contract, &state, &server, &reoffer)
		command := &agentpb.StackKitCommand{}
		if err != nil || protojson.Unmarshal(raw, command) != nil || state != "dispatched" || contract != binding.ContractVersion || generation != binding.WorkerGeneration || !expires.Equal(binding.AuthorityExpiresAt) || digest != binding.PayloadDigest || command.Operation.String() != binding.Operation || reoffer != binding.Reoffer {
			deny()
			return
		}
		computed, err := guardbinding.DigestOf(command)
		if err != nil || computed != digest {
			deny()
			return
		}
		canonical := &guardbinding.Binding{CommandID: binding.CommandID, TenantID: grant.TenantID, WorkerID: worker.ID, ServerID: server, Operation: command.Operation.String(), PayloadDigest: digest, WorkerGeneration: generation, CurrentGeneration: credential.Generation, AuthorityExpiresAt: expires.UTC(), ContractVersion: contract, Reoffer: reoffer}
		if err := signer.Sign(canonical); err != nil {
			deny()
			return
		}
		_ = json.NewEncoder(w).Encode(canonical)
		return
	}
	if recovery := request.Predecessor; recovery != nil && r.URL.Path == remotePath+"worker-binding/predecessor-recovery" {
		canonical, ok := p.predecessorRecovery(r, grant, worker, recovery)
		if !ok || signer.SignPredecessorRecovery(canonical) != nil {
			deny()
			return
		}
		_ = json.NewEncoder(w).Encode(canonical)
		return
	}
	if local := request.LocalGrant; local != nil && r.URL.Path == remotePath+"worker-binding/local-grant" {
		if p.LocalGrant == nil || local.TenantID != grant.TenantID || local.WorkerID != worker.ID || local.Generation != credential.Generation {
			deny()
			return
		}

		tx, err := db.BeginTenantTransaction(r.Context(), p.AuditDatabase, grant.TenantID)
		if err != nil {
			deny()
			return
		}
		var ownedServer bool
		err = tx.QueryRowContext(r.Context(), `SELECT EXISTS(SELECT 1 FROM servers WHERE tenant_id=$1 AND worker_id=$2 AND id=$3 AND owner_subject_id=$4)`, grant.TenantID, worker.ID, local.ServerID, grant.OwnerID).Scan(&ownedServer)
		_ = tx.Rollback()
		if err != nil || !ownedServer {
			deny()
			return
		}
		// Recompute the entire current policy/release/history from its existing
		// authority. No caller-supplied repair bound or release is signed.
		canonical, err := p.LocalGrant(r.Context(), grant.TenantID, worker.ID, credential.Generation)
		if err != nil || canonical == nil || canonical.ServerID != local.ServerID || canonical.TenantID != grant.TenantID || canonical.WorkerID != worker.ID || canonical.Generation != credential.Generation {
			deny()
			return
		}
		if err := signer.SignLocalGrant(canonical); err != nil {
			deny()
			return
		}
		_ = json.NewEncoder(w).Encode(canonical)
		return
	}
	deny()
}
