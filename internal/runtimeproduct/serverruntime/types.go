// Package serverruntime owns Techstack-specific managed-runtime product state.
// These provider, billing, feature and SSH projections are intentionally not
// part of the shared neutral runtime contracts.
package serverruntime

type NodeLifecycle string

const NodeLifecyclePVM NodeLifecycle = "pvm"

type BillingCadence string

const BillingCadenceMonthly BillingCadence = "monthly"

const (
	RuntimeLaneMonthly                    = "monthly-runtime"
	FeatureTechStackMonthlyRuntime        = "techstack_monthly_runtime"
	FeatureTechStackMonthlyRuntimeCentron = "techstack_monthly_runtime_centron"
	FeatureTechStackMonthlyRuntimeIONOS   = "techstack_monthly_runtime_ionos"
	FeatureTechStackMonthlyRuntimeBaseKit = "techstack_monthly_runtime_basekit"
)

type RuntimeOfferingID string

const (
	RuntimeOfferingStandard RuntimeOfferingID = "monthly-runtime-standard"
	RuntimeOfferingPremium  RuntimeOfferingID = "monthly-runtime-premium"
)

type RuntimeAction string

const (
	RuntimeActionStatus       RuntimeAction = "status"
	RuntimeActionStart        RuntimeAction = "start"
	RuntimeActionStop         RuntimeAction = "stop"
	RuntimeActionEnableSSH    RuntimeAction = "enable_ssh"
	RuntimeActionDisableSSH   RuntimeAction = "disable_ssh"
	RuntimeActionSSHInfo      RuntimeAction = "ssh_info"
	RuntimeActionDecommission RuntimeAction = "decommission"
)

type RuntimeProfile struct{ ProfileID, ProviderID string }

func MonthlyRuntimeProfileForProvider(providerID string) (RuntimeProfile, bool) {
	switch providerID {
	case "centron-managed":
		return RuntimeProfile{ProfileID: "centron-managed-pvm-monthly", ProviderID: providerID}, true
	case "ionos-managed":
		return RuntimeProfile{ProfileID: "ionos-managed-pvm-monthly", ProviderID: providerID}, true
	default:
		return RuntimeProfile{}, false
	}
}

type NodeStatus struct {
	ID        string `json:"id"`
	State     string `json:"state"`
	PublicIP  string `json:"public_ip,omitempty"`
	PrivateIP string `json:"private_ip,omitempty"`
	Error     string `json:"error,omitempty"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

type SSHInfo struct {
	Host             string `json:"host,omitempty"`
	Port             int    `json:"port,omitempty"`
	User             string `json:"user,omitempty"`
	Password         string `json:"password,omitempty"`
	KeyPath          string `json:"key_path,omitempty"`
	PrivateKey       string `json:"private_key,omitempty"`
	ClientPrivateKey string `json:"client_private_key,omitempty"`
	PublicKey        string `json:"public_key,omitempty"`
	AuthMethod       string `json:"auth_method,omitempty"`
	Ephemeral        bool   `json:"ephemeral,omitempty"`
	HostKey          string `json:"host_key,omitempty"`
	DisplayHost      string `json:"display_host,omitempty"`
	TunnelOnly       bool   `json:"tunnel_only,omitempty"`
	NodePrivateIP    string `json:"node_private_ip,omitempty"`
	NodePublicIP     string `json:"node_public_ip,omitempty"`
	ProxyJump        string `json:"proxy_jump,omitempty"`
	Command          string `json:"ssh_command,omitempty"`
}

type LeaseRuntimeActionRequest struct {
	TenantID   string            `json:"tenant_id,omitempty"`
	OwnerID    string            `json:"owner_id,omitempty"`
	LeaseID    string            `json:"lease_id"`
	Action     RuntimeAction     `json:"action"`
	OfferingID RuntimeOfferingID `json:"runtime_offering_id,omitempty"`
	Metadata   map[string]string `json:"metadata,omitempty"`
}

type LeaseRuntimeActionResponse struct {
	TenantID      string            `json:"tenant_id,omitempty"`
	LeaseID       string            `json:"lease_id"`
	Action        RuntimeAction     `json:"action"`
	OfferingID    RuntimeOfferingID `json:"runtime_offering_id,omitempty"`
	ProviderID    string            `json:"provider_id,omitempty"`
	ProfileID     string            `json:"profile_id,omitempty"`
	EngineVMID    string            `json:"engine_vm_id,omitempty"`
	DesiredState  string            `json:"desired_state,omitempty"`
	ObservedState string            `json:"observed_state,omitempty"`
	LeaseState    string            `json:"lease_state,omitempty"`
	LeaseReason   string            `json:"lease_reason,omitempty"`
	SSHEnabled    bool              `json:"ssh_enabled,omitempty"`
	Status        *NodeStatus       `json:"status,omitempty"`
	SSH           *SSHInfo          `json:"ssh,omitempty"`
	Metadata      map[string]string `json:"metadata,omitempty"`
}
