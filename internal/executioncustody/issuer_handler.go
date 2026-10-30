package executioncustody

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/google/uuid"
)

// CallerGrant is deployment authority installed by the privileged Execution
// operator, never supplied by the user-facing process or request body. One
// dedicated key has one caller, tenant and host Owner, with explicit deployments.
type CallerGrant struct {
	Caller              string   `json:"caller"`
	OwnerID             string   `json:"owner_id"`
	DeploymentAdmission string   `json:"deployment_admission,omitempty"`
	PublicKey           string   `json:"public_key"`
	TenantID            string   `json:"tenant_id"`
	OwnerRef            string   `json:"owner_ref"`
	DeploymentIDs       []string `json:"deployment_ids"`
}

// NewRemoteHandler exposes the existing issuer, not a second signing or wallet
// authority. Authentication binds every request to its exact body and route.
func NewRemoteHandler(issuer *Issuer, grants map[string]CallerGrant, provider *Provider) (http.Handler, error) {
	if issuer == nil || len(grants) == 0 || provider == nil || provider.Database == nil || provider.AuditDatabase == nil || provider.Runtime == nil || provider.Actions.LeaseManager == nil || provider.Actions.LeaseDecommissioner == nil || provider.Power == nil || provider.Channel == nil || provider.Envelopes == nil || len(provider.BindingBase) == 0 {
		return nil, errors.New("advanced issuer requires its retained key and explicit caller grants")
	}
	keys := map[string]servicecall.CallerPublicKey{}
	byCaller := map[string]CallerGrant{}
	kidByCaller := map[string]string{}
	var callers []string
	for kid, grant := range grants {
		key, err := decodePublicKey(grant.PublicKey)
		ownedRecords := grant.DeploymentAdmission == "owned-records" && grant.OwnerRef == "" && grant.DeploymentIDs != nil && len(grant.DeploymentIDs) == 0
		fixed := grant.DeploymentAdmission == "" && ownerRefPattern.MatchString(grant.OwnerRef) && len(grant.DeploymentIDs) > 0
		if err != nil || !callerKeyIDPattern.MatchString(kid) || !stackIDPattern.MatchString(grant.Caller) || grant.TenantID == "" || grant.OwnerID == "" || (!ownedRecords && !fixed) {
			return nil, errors.New("advanced issuer caller grant is incomplete")
		}
		if _, exists := byCaller[grant.Caller]; exists {
			return nil, errors.New("advanced issuer caller has ambiguous grants")
		}
		for _, deployment := range grant.DeploymentIDs {
			if !deploymentIDPattern.MatchString(deployment) {
				return nil, errors.New("advanced issuer caller deployment is invalid")
			}
		}
		keys[kid] = servicecall.CallerPublicKey{Service: grant.Caller, Key: key}
		byCaller[grant.Caller] = grant
		kidByCaller[grant.Caller] = kid
		callers = append(callers, grant.Caller)
	}
	core := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json")
		caller := servicecall.FromContext(r.Context())
		if caller == nil {
			http.Error(w, "issuer caller required", http.StatusUnauthorized)
			return
		}
		grant, ok := byCaller[caller.Service]
		id, err := uuid.Parse(caller.RequestID)
		if !ok || err != nil || id == uuid.Nil || id.String() != caller.RequestID {
			http.Error(w, "issuer request denied", http.StatusForbidden)
			return
		}
		admitted := func(tenant, deployment, owner string) bool {
			if tenant != grant.TenantID || owner != "" && grant.OwnerRef != "" && owner != grant.OwnerRef {
				return false
			}
			if grant.DeploymentAdmission == "owned-records" {
				return provider.scopeDeployment(r.Context(), grant, tenant, deployment, grant.OwnerID) == nil
			}
			for _, allowed := range grant.DeploymentIDs {
				if allowed == deployment {
					return true
				}
			}
			return false
		}
		decode := func(value any) bool {
			decoder := json.NewDecoder(r.Body)
			decoder.DisallowUnknownFields()
			if decoder.Decode(value) != nil || decoder.Decode(&struct{}{}) != io.EOF {
				http.Error(w, "issuer request invalid", http.StatusBadRequest)
				return false
			}
			return true
		}
		respond := func(value any, err error) {
			if err != nil {
				status := http.StatusConflict
				if errors.Is(err, ErrTrustBindingMissing) {
					status = http.StatusNotFound
				}
				http.Error(w, "issuer operation denied", status)
				return
			}
			_ = json.NewEncoder(w).Encode(value)
		}
		if r.Method != http.MethodPost {
			http.Error(w, "method denied", http.StatusMethodNotAllowed)
			return
		}
		action := strings.TrimPrefix(r.URL.Path, remotePath)
		if action == "issue" && id.Version() != 7 {
			http.Error(w, "capability request requires UUIDv7", http.StatusBadRequest)
			return
		}
		if mutates(action) {
			if err := provider.claimRequest(r.Context(), grant, caller.RequestID, r.URL.Path); err != nil {
				status := http.StatusServiceUnavailable
				if errors.Is(err, errRequestReplay) {
					status = http.StatusConflict
				}
				http.Error(w, "execution request admission denied", status)
				return
			}
		}
		switch action {
		case "worker-binding/pins", "worker-binding/sign", "worker-binding/local-grant", "worker-binding/predecessor-recovery":
			var request workerBindingRequest
			if !decode(&request) {
				return
			}
			provider.serveWorkerBinding(w, r, grant, request)
		case "metadata":
			if !decode(&struct{}{}) {
				return
			}
			respond(Metadata{IssuerID: issuer.IssuerID(), KeyID: issuer.KeyID(), Bundle: issuer.TrustBundle(), CallerKeyID: kidByCaller[grant.Caller], Grant: grant}, nil)
		case "binding":
			var request TrustBinding
			if !decode(&request) {
				return
			}
			if !admitted(request.TenantID, request.DeploymentID, "") {
				http.Error(w, "issuer scope denied", http.StatusForbidden)
				return
			}
			binding, err := issuer.TrustBindingFor(r.Context(), request.TenantID, request.DeploymentID)
			if err == nil && grant.OwnerRef != "" && binding.OwnerRef != grant.OwnerRef {
				http.Error(w, "issuer owner denied", http.StatusForbidden)
				return
			}
			respond(binding, err)
		case "bind":
			var binding TrustBinding
			if !decode(&binding) {
				return
			}
			if !admitted(binding.TenantID, binding.DeploymentID, binding.OwnerRef) {
				http.Error(w, "issuer scope denied", http.StatusForbidden)
				return
			}
			if err := provider.verifyTrustImport(r.Context(), grant, binding); err != nil {
				http.Error(w, "authenticated host trust-import evidence required", http.StatusForbidden)
				return
			}
			respond(struct{}{}, issuer.RecordTrustBinding(r.Context(), binding))
		case "issue":
			var request Request
			if !decode(&request) {
				return
			}
			if !admitted(request.TenantID, request.DeploymentID, request.OwnerRef) || request.TTL < 0 || request.TTL > DefaultTTL {
				http.Error(w, "issuer scope denied", http.StatusForbidden)
				return
			}
			binding, err := issuer.TrustBindingFor(r.Context(), request.TenantID, request.DeploymentID)
			if err != nil {
				respond(nil, err)
				return
			}
			if binding.OwnerRef != request.OwnerRef || binding.StackID != request.StackID || binding.IssuerID != issuer.IssuerID() || binding.KeyID != issuer.KeyID() || binding.BundleSHA256 != issuer.TrustBundleSHA256() {
				http.Error(w, "issuer trust binding denied", http.StatusForbidden)
				return
			}
			capability, err := issuer.IssueBoundRequest(r.Context(), request, id)
			respond(capability, err)
		default:
			if provider != nil {
				provider.serve(w, r, grant)
			} else {
				http.NotFound(w, r)
			}
		}
	})
	return servicecall.RequireBoundServiceAuth(servicecall.BoundRequestConfig{Config: servicecall.Config{ServiceName: RemoteAudience, CallerPublicKeys: keys, RequireEd25519: true, AllowedCallers: callers, Enabled: true}, RequiredScope: remoteScope, MaxBodyBytes: remoteBodyLimit})(core), nil
}

func decodePublicKey(value string) ([]byte, error) {
	key, err := base64.RawStdEncoding.Strict().DecodeString(value)
	if err != nil || len(key) != 32 {
		return nil, errors.New("advanced issuer caller public key is invalid")
	}
	return key, nil
}
