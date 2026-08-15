package unifier

import "strings"

const iacNodeRoleManager, iacNodeRoleWorker, iacNodeRoleStandalone = "manager", "worker", "standalone"
const iacModeSimple, iacModeAdvanced = "simple", "advanced"
const iacDefaultVariant, iacDefaultComputeTier, iacTemplateFuncDefault, iacSwarmModeSingleManager, iacPackageManagerAPT, iacPackageManagerDNF = "default", "standard", "default", "single-manager", "apt", "dnf"
const iacDefaultSSHUser, iacNetworkModePublic, iacNetworkModeHybrid, iacTLSModeACME, iacTLSModeSelfSigned, iacTLSModeNone = "root", "public", "hybrid", "acme", "self-signed", "none"

// IaCConfig holds the configuration for IaC generation.
type IaCConfig struct {
	// Stack identification
	StackName string `json:"stack_name"`
	StackKit  string `json:"stack_kit"`
	Mode      string `json:"mode"` // "simple" or "advanced"
	Variant   string `json:"variant"`

	// Worker configuration (single-node backwards compatible)
	WorkerIP        string `json:"worker_ip"`
	SSHUser         string `json:"ssh_user"`
	SSHPort         int    `json:"ssh_port"`
	SSHKeyPath      string `json:"ssh_key_path"`
	OSType          string `json:"os_type"` // ubuntu-24, debian-12, etc.
	ComputeTier     string `json:"compute_tier"`
	DockerInstalled bool   `json:"docker_installed"`
	DockerHost      string `json:"docker_host,omitempty"`
	// Platform fallback keeps StackKit-owned application deployment on direct
	// Docker/Compose when a local proof target should not depend on a PaaS
	// installer.
	EnablePlatformFallback bool   `json:"enable_platform_fallback,omitempty"`
	PlatformFallbackMode   string `json:"platform_fallback_mode,omitempty"`

	// Multi-node configuration (Sprint 4.5)
	Nodes       []IaCNode    `json:"nodes,omitempty"`
	SwarmConfig *SwarmConfig `json:"swarm,omitempty"`

	// Network configuration
	NetworkMode     string `json:"network_mode"` // "local", "public", "hybrid"
	Domain          string `json:"domain,omitempty"`
	SubdomainPrefix string `json:"subdomain_prefix,omitempty"`
	ACMEEmail       string `json:"acme_email,omitempty"`
	VPNType         string `json:"vpn_type,omitempty"`
	Subnet          string `json:"subnet,omitempty"`

	// Service configuration
	Services []IaCService `json:"services"`
	// BaseKitServiceEnablement carries explicit base-kit enable_* flags from
	// StackKit service maps into terraform.tfvars.json.
	BaseKitServiceEnablement map[string]bool `json:"basekit_service_enablement,omitempty"`

	// Security
	AdminUser              string `json:"admin_user,omitempty"`
	AdminEmail             string `json:"admin_email,omitempty"`
	AdminPasswordPlaintext string `json:"admin_password_plaintext,omitempty"`
	TinyAuthUsers          string `json:"tinyauth_users,omitempty"`
	PocketIDStaticAPIKey   string `json:"pocketid_static_api_key,omitempty"`
	PocketIDEncryptionKey  string `json:"pocketid_encryption_key,omitempty"`
	EnableFirewall         bool   `json:"enable_firewall"`
	AllowedPorts           []int  `json:"allowed_ports,omitempty"`

	// TLS
	TLSMode string `json:"tls_mode"` // "self-signed", "acme", "none"

	// Metadata
	Labels map[string]string `json:"labels,omitempty"`
}

// IaCNode represents a node in multi-node deployments (Sprint 4.5).
type IaCNode struct {
	Name       string            `json:"name"`
	IP         string            `json:"ip"`
	PrivateIP  string            `json:"private_ip,omitempty"`
	PublicIP   string            `json:"public_ip,omitempty"`
	Role       string            `json:"role"` // "manager", "worker", "standalone"
	SSHUser    string            `json:"ssh_user,omitempty"`
	SSHPort    int               `json:"ssh_port,omitempty"`
	SSHKeyPath string            `json:"ssh_key_path,omitempty"`
	OSType     string            `json:"os_type,omitempty"`
	Labels     map[string]string `json:"labels,omitempty"`
}

// SwarmConfig holds Docker Swarm cluster configuration (Sprint 4.5).
type SwarmConfig struct {
	Enabled          bool   `json:"enabled"`
	Mode             string `json:"mode"` // "single-manager", "ha" (3+ managers)
	ManagerCount     int    `json:"manager_count"`
	WorkerCount      int    `json:"worker_count"`
	AdvertiseAddr    string `json:"advertise_addr,omitempty"`
	ListenAddr       string `json:"listen_addr,omitempty"`
	DataPathPort     int    `json:"data_path_port,omitempty"`
	EncryptOverlay   bool   `json:"encrypt_overlay"`
	AutoLockManagers bool   `json:"auto_lock_managers"`
}

// IaCService represents a service for IaC generation.
type IaCService struct {
	Name         string            `json:"name"`
	Image        string            `json:"image"`
	Port         int               `json:"port,omitempty"`
	InternalPort int               `json:"internal_port,omitempty"`
	Environment  map[string]string `json:"environment,omitempty"`
	Volumes      []string          `json:"volumes,omitempty"`
	Networks     []string          `json:"networks,omitempty"`
	Labels       map[string]string `json:"labels,omitempty"`
	DependsOn    []string          `json:"depends_on,omitempty"`
	Restart      string            `json:"restart,omitempty"`
	Command      []string          `json:"command,omitempty"`
	Privileged   bool              `json:"privileged,omitempty"`
	Public       bool              `json:"public"` // Exposed via Traefik

	// Swarm-specific fields (Sprint 4.5)
	SwarmMode    bool                 `json:"swarm_mode,omitempty"`  // Deploy as Swarm service
	Replicas     int                  `json:"replicas,omitempty"`    // Number of replicas
	Global       bool                 `json:"global,omitempty"`      // Deploy on all nodes
	Constraints  []string             `json:"constraints,omitempty"` // Placement constraints
	UpdateConfig *ServiceUpdateConfig `json:"update_config,omitempty"`
}

// IaCOutput contains the generated IaC files.
type IaCOutput struct {
	Files      map[string]string `json:"files"`
	TFVarsJSON []byte            `json:"tfvars_json"`
	Mode       string            `json:"mode"`
	OutputDir  string            `json:"output_dir"`
	Errors     []string          `json:"errors,omitempty"`
	Warnings   []string          `json:"warnings,omitempty"`
}

// HasHardErrors returns true if there are non-warning errors in the output.
func (o *IaCOutput) HasHardErrors() bool {
	for _, e := range o.Errors {
		if !strings.HasPrefix(strings.ToLower(e), "warning:") {
			return true
		}
	}
	return false
}

// HardErrors returns only non-warning errors.
func (o *IaCOutput) HardErrors() []string {
	var errs []string
	for _, e := range o.Errors {
		if !strings.HasPrefix(strings.ToLower(e), "warning:") {
			errs = append(errs, e)
		}
	}
	return errs
}

// ServiceUpdateConfig defines how service updates are handled in Swarm.
type ServiceUpdateConfig struct {
	Parallelism   int    `json:"parallelism,omitempty"`    // Number of tasks to update at once
	Delay         string `json:"delay,omitempty"`          // Delay between updates (e.g., "10s")
	FailureAction string `json:"failure_action,omitempty"` // "pause", "continue", "rollback"
	Order         string `json:"order,omitempty"`          // "stop-first", "start-first"
}

// MultiNodeTFVars represents terraform.tfvars for multi-node StackKits.
// Used by modern-kit and ha-kit.
type MultiNodeTFVars struct {
	// Stack identification
	StackName   string `json:"stack_name"`
	StackKit    string `json:"stack_kit"`
	Variant     string `json:"variant"`
	ComputeTier string `json:"compute_tier"`

	// Network configuration
	NetworkMode string `json:"network_mode"`
	Domain      string `json:"domain,omitempty"`
	ACMEEmail   string `json:"acme_email,omitempty"`
	VPNType     string `json:"vpn_type,omitempty"`
	VPNEnabled  bool   `json:"vpn_enabled"`

	// Swarm configuration
	SwarmEnabled      bool   `json:"swarm_enabled"`
	SwarmMode         string `json:"swarm_mode"` // "single-manager", "ha"
	SwarmManagerCount int    `json:"swarm_manager_count"`
	SwarmEncrypted    bool   `json:"swarm_encrypted"`

	// Node definitions
	Nodes []NodeTFVar `json:"nodes"`

	// Service configuration
	EnableHTTPS   bool `json:"enable_https"`
	EnableLetsEnc bool `json:"enable_letsencrypt"`

	// Security
	EnableFirewall bool  `json:"enable_firewall"`
	AllowedPorts   []int `json:"allowed_ports,omitempty"`
}

// NodeTFVar represents a single node in terraform.tfvars.
type NodeTFVar struct {
	Name       string `json:"name"`
	IP         string `json:"ip"`
	PrivateIP  string `json:"private_ip,omitempty"`
	PublicIP   string `json:"public_ip,omitempty"`
	Role       string `json:"role"` // "manager", "worker"
	SSHUser    string `json:"ssh_user"`
	SSHPort    int    `json:"ssh_port"`
	SSHKeyPath string `json:"ssh_key_path,omitempty"`
	IsFirst    bool   `json:"is_first,omitempty"` // First manager initializes swarm
}

// BaseKitTFVars represents the terraform.tfvars structure for base-kit.
// This aligns with StackKits/base-kit/templates/simple/main.tf variables.
type BaseKitTFVars struct {
	// Access configuration
	AccessMode      string `json:"access_mode"` // "ports" or "proxy"
	AdvertiseHost   string `json:"advertise_host,omitempty"`
	Domain          string `json:"domain,omitempty"`
	SubdomainPrefix string `json:"subdomain_prefix,omitempty"`
	EnableHTTPS     bool   `json:"enable_https"`
	EnableLetsEnc   bool   `json:"enable_letsencrypt"`
	TLSProvider     string `json:"tls_provider,omitempty"`
	StepCAEnabled   bool   `json:"step_ca_enabled"`
	ACMEEmail       string `json:"acme_email,omitempty"`
	ACMEChallenge   string `json:"acme_challenge,omitempty"`
	AdminEmail      string `json:"admin_email,omitempty"`

	// Variant and compute
	Variant     string `json:"variant"`      // "default", "beszel", "minimal"
	ComputeTier string `json:"compute_tier"` // "high", "standard", "low"

	// BaseKit service toggles
	EnableTraefik      bool   `json:"enable_traefik"`
	EnableTinyAuth     bool   `json:"enable_tinyauth"`
	EnablePocketID     bool   `json:"enable_pocketid"`
	EnableDokploy      bool   `json:"enable_dokploy"`
	EnableDokployApps  bool   `json:"enable_dokploy_apps"`
	EnableKomodo       bool   `json:"enable_komodo"`
	EnableDockge       bool   `json:"enable_dockge"`
	EnableUptimeKuma   bool   `json:"enable_uptime_kuma"`
	EnableWhoami       bool   `json:"enable_whoami"`
	EnableVaultwarden  bool   `json:"enable_vaultwarden"`
	EnableJellyfin     bool   `json:"enable_jellyfin"`
	EnableImmich       bool   `json:"enable_immich"`
	EnableFiles        bool   `json:"enable_files"`
	EnableCloudreve    bool   `json:"enable_cloudreve"`
	EnableNextcloud    bool   `json:"enable_nextcloud"`
	EnableDashboard    bool   `json:"enable_dashboard"`
	EnableHomepage     bool   `json:"enable_homepage"`
	EnableKombifyPoint bool   `json:"enable_kombify_point"`
	EnableCoolify      bool   `json:"enable_coolify"`
	FilesProvider      string `json:"files_provider,omitempty"`
	// StackKits BaseKit fallback mode. When enabled, StackKits deploys apps
	// directly instead of requiring Coolify bootstrap.
	EnablePlatformFallback bool   `json:"enable_platform_fallback"`
	PlatformFallbackMode   string `json:"platform_fallback_mode"`

	// Port mappings (for ports mode)
	TraefikDashboardPort int `json:"traefik_dashboard_port,omitempty"`
	DozzlePort           int `json:"dozzle_port,omitempty"`
	UptimeKumaPort       int `json:"uptime_kuma_port,omitempty"`
	BeszelPort           int `json:"beszel_port,omitempty"`
	DockgePort           int `json:"dockge_port,omitempty"`
	PortainerPort        int `json:"portainer_port,omitempty"`
	NetdataPort          int `json:"netdata_port,omitempty"`
	WhoamiPort           int `json:"whoami_port,omitempty"`

	// Optional settings
	StacksDir                string `json:"stacks_dir,omitempty"`
	TraefikDashboardInsecure bool   `json:"traefik_dashboard_insecure,omitempty"`
	BindAddress              string `json:"bind_address,omitempty"`
	DockerHost               string `json:"docker_host,omitempty"`

	// Generated identity/bootstrap material. These values are written only to
	// generated tfvars output and must not be committed.
	AdminPasswordPlaintext string `json:"admin_password_plaintext,omitempty"`
	TinyAuthUsers          string `json:"tinyauth_users,omitempty"`
	TinyAuthSecureCookie   string `json:"tinyauth_secure_cookie,omitempty"`
	TinyAuthAppURL         string `json:"tinyauth_app_url,omitempty"`
	PocketIDStaticAPIKey   string `json:"pocketid_static_api_key,omitempty"`
	PocketIDEncryptionKey  string `json:"pocketid_encryption_key,omitempty"`
	PocketIDAppURL         string `json:"pocketid_app_url,omitempty"`
}

// StackKitTemplateData adapts IaCConfig to the data model expected by StackKit .tpl templates.
// This bridges the Unifier's IaCConfig struct and the template variables
// documented in base-kit/templates/advanced/README.md.
type StackKitTemplateData struct {
	StackName   string
	StackID     string
	Environment string
	Network     struct {
		VPNEnabled bool
		Subnet     string
		Domain     string
	}
	Nodes []struct {
		Name     string
		IP       string
		Role     string
		Provider string
	}
	Services []struct {
		Name  string
		Type  string
		Node  string
		Port  int
		Image string
	}
	Catalog []StackKitCatalogEntry
	Domains []StackKitCatalogEntry
}

// StackKitCatalogEntry mirrors the subset of StackKits servicecatalog.Service
// used by the external Base Kit templates.
type StackKitCatalogEntry struct {
	Key                     string
	Name                    string
	DisplayName             string
	Description             string
	ToolName                string
	ModuleSlug              string
	LocalSlug               string
	PublicSlug              string
	IdentityPolicy          string
	OwnerProvisioningPolicy string
	Icon                    string
	LogoURL                 string
	Badge                   string
	Layer                   string
	Section                 string
	Order                   int
	EnableVar               string
	GuideURL                string
	SetupPolicy             string
	SetupActionLabel        string
	Default                 bool
	Nested                  string
	Flat                    string
}

// IsMultiNode returns true if the config represents a multi-node deployment.
func (c *IaCConfig) IsMultiNode() bool {
	return len(c.Nodes) > 1 || (c.SwarmConfig != nil && c.SwarmConfig.Enabled)
}

// IsSwarmEnabled returns true if Docker Swarm is enabled.
func (c *IaCConfig) IsSwarmEnabled() bool {
	return c.SwarmConfig != nil && c.SwarmConfig.Enabled
}

// GetManagerNodes returns all nodes with manager role.
func (c *IaCConfig) GetManagerNodes() []IaCNode {
	var managers []IaCNode
	for _, node := range c.Nodes {
		if node.Role == iacNodeRoleManager {
			managers = append(managers, node)
		}
	}
	return managers
}

// GetWorkerNodes returns all nodes with worker role.
func (c *IaCConfig) GetWorkerNodes() []IaCNode {
	var workers []IaCNode
	for _, node := range c.Nodes {
		if node.Role == iacNodeRoleWorker {
			workers = append(workers, node)
		}
	}
	return workers
}

// GetPrimaryManager returns the first manager node (Swarm initializer).
func (c *IaCConfig) GetPrimaryManager() *IaCNode {
	for i, node := range c.Nodes {
		if node.Role == iacNodeRoleManager {
			return &c.Nodes[i]
		}
	}
	return nil
}
