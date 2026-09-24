package homeassistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"reflect"

	"github.com/kombifyio/techstack/internal/gocommon/runtimeexecutor"
	"github.com/kombifyio/techstack/internal/managedstackkit"
)

// OwnerBinding is retained during authenticated enrollment of the exact
// CLI-validated target. Hashes are not learned from an incoming operation.
type OwnerBinding struct {
	OwnerID, RecoveryBindingRef       string
	TenantID, StackID, RuntimeAgentID string
	Target                            runtimeexecutor.RuntimeTarget
	Health                            []runtimeexecutor.HealthTarget
	Instance                          *InstanceBinding
}

type OwnerOperations struct {
	Enrollments *EnrollmentStore
	Bindings    []OwnerBinding
	Fallback    interface {
		Execute(context.Context, managedstackkit.OperationsRequest) (runtimeexecutor.ExecutionOutcome, error)
	}
}

func (o *OwnerOperations) Execute(ctx context.Context, input managedstackkit.OperationsRequest) (runtimeexecutor.ExecutionOutcome, error) {
	empty := runtimeexecutor.ExecutionOutcome{}
	r := input.Request
	if err := r.Validate(); err != nil {
		return empty, err
	}
	if len(r.RuntimeTargets) != 1 {
		return empty, ErrUnauthorized
	}
	t := r.RuntimeTargets[0]
	if t.ProviderRef != "stackkits-home-assistant-appliance" {
		if o.Fallback != nil {
			return o.Fallback.Execute(ctx, input)
		}
		return empty, ErrUnauthorized
	}
	var binding *OwnerBinding
	for i := range o.Bindings {
		b := &o.Bindings[i]
		if b.TenantID == input.TenantID && b.StackID == input.StackID && b.RuntimeAgentID == input.RuntimeAgentID && reflect.DeepEqual(b.Target, t) && reflect.DeepEqual(b.Health, r.HealthTargets) {
			if binding != nil {
				return empty, ErrUnauthorized
			}
			binding = b
		}
	}
	if len(r.Artifacts) != 1 || len(t.ArtifactRefs) != 1 || len(r.AccessBindings) != 0 || len(r.BackupTargetBindings) != 0 || t.RuntimeKind != "external" || t.RuntimeDelivery != "external-control-plane" || t.RuntimeEngine != "api" || t.OwnerKind != "module" || t.OwnerRef != t.ModuleRef || t.UnitRef != "instance" || t.WorkloadRef != "smart-home" || len(t.SiteRefs) != 1 || len(t.NodeRefs) != 1 {
		return empty, ErrUnauthorized
	}
	a := r.Artifacts[0]
	if a.ID != t.ArtifactRefs[0] || a.OwnerKind != "render-instance" || a.OwnerRef != t.InstanceRef || a.InstanceRef != t.InstanceRef || a.OwnerContractHash != t.UnitContractHash || a.UnitContractHash != t.UnitContractHash || a.ModuleContractHash != t.ModuleContractHash || a.ProviderContractHash != t.ProviderContractHash || a.ModuleRef != t.ModuleRef || a.ProviderRef != t.ProviderRef || a.UnitRef != t.UnitRef || !reflect.DeepEqual(a.SiteRefs, t.SiteRefs) || !reflect.DeepEqual(a.NodeRefs, t.NodeRefs) || a.Format != "json" || a.Kind != "native-config" {
		return empty, ErrUnauthorized
	}
	// CUE and the pinned CLI own semantic validation. This consumer only
	// decodes their closed transport and checks local binding substitution.
	var artifact struct {
		APIVersion string `json:"apiVersion"`
		ModuleRef  string `json:"moduleRef"`
		SiteRef    string `json:"siteRef"`
		NodeRef    string `json:"nodeRef"`
		Instance   struct {
			Platform            string `json:"platform"`
			InstallationMethod  string `json:"installationMethod"`
			InstanceOrigin      string `json:"instanceOrigin"`
			ManagementScope     string `json:"managementScope"`
			ConfigurationPolicy string `json:"configurationPolicy"`
			DataCustody         string `json:"dataCustody"`
			BaselinePolicy      string `json:"baselinePolicy"`
			BaselineVersion     string `json:"baselineVersion,omitempty"`
		} `json:"instance"`
	}
	decoder := json.NewDecoder(bytes.NewReader(a.Content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return empty, err
	}
	var trailing any
	if !errors.Is(decoder.Decode(&trailing), io.EOF) {
		return empty, ErrUnauthorized
	}
	i := artifact.Instance
	if artifact.APIVersion != "stackkit.home-assistant-instance/v1" || artifact.ModuleRef != t.ModuleRef || artifact.SiteRef != t.SiteRefs[0] || artifact.NodeRef != t.NodeRefs[0] || i.Platform != "home-assistant" || i.ConfigurationPolicy != "preserve-user-changes" || i.DataCustody != "external-instance-owner" {
		return empty, ErrUnauthorized
	}
	if i.InstanceOrigin == "new" && (i.ManagementScope != "managed" || i.BaselinePolicy != "fresh-only" || i.BaselineVersion != "1" || i.InstallationMethod != "haos") {
		return empty, ErrUnauthorized
	}
	if i.InstanceOrigin != "new" && (i.BaselinePolicy != "preserve" || i.BaselineVersion != "") {
		return empty, ErrUnauthorized
	}
	for _, health := range r.HealthTargets {
		if health.RuntimeRequirementID != t.RequirementID || health.TargetRef != t.InstanceRef || health.RouteRef != "" || health.Probe != nil {
			return empty, ErrUnauthorized
		}
	}
	if binding == nil && o.Enrollments != nil {
		var err error
		binding, err = o.Enrollments.Resolve(ctx, input, i.InstanceOrigin, i.ManagementScope)
		if err != nil {
			return empty, err
		}
		defer binding.Instance.Core.Close()
		if binding.Instance.Supervisor != nil {
			defer binding.Instance.Supervisor.Close()
		}
	}
	if binding == nil || binding.Instance == nil || i.InstanceOrigin != binding.Instance.Origin || i.ManagementScope != binding.Instance.ManagementScope {
		return empty, ErrUnauthorized
	}
	observation, err := binding.Instance.ReconcileBaseline(ctx)
	if err != nil {
		return empty, err
	}
	for _, gap := range observation.Gaps {
		if gap != "companion_connector_authorization" {
			return empty, errors.New("Home Assistant baseline requires authenticated TLS access and verified off-instance recovery")
		}
	}
	raw, _ := json.Marshal(observation)
	sum := sha256.Sum256(raw)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	ref := "home-assistant-observation:" + r.RequestDigest
	out := runtimeexecutor.ExecutionOutcome{Runtime: []runtimeexecutor.RuntimeOutcome{{RequirementID: t.RequirementID, InstanceRef: t.InstanceRef, Status: runtimeexecutor.RuntimeStatusApplied, ObservationRef: ref, ObservationDigest: digest}}}
	for _, health := range r.HealthTargets {
		// Only the native application read was actually probed. A routed HTTP
		// health requirement needs its own executor and cannot inherit this PASS.
		if health.RuntimeRequirementID != t.RequirementID || health.TargetRef != t.InstanceRef || health.RouteRef != "" || health.Probe != nil {
			return empty, ErrUnauthorized
		}
		out.Health = append(out.Health, runtimeexecutor.HealthOutcome{RequirementID: health.RequirementID, TargetRef: health.TargetRef, Status: runtimeexecutor.HealthStatusHealthy, ObservationRef: ref, ObservationDigest: digest})
	}
	return out, nil
}
