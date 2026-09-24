package routes

import (
	"net/http"

	"github.com/kombifyio/techstack/pkg/httpx"
)

const (
	capabilityNameHealthCheck = "health_check"
	capabilityTagHealth       = "health"
	capabilityTagJobs         = "jobs"
	capabilityTagMCP          = "mcp"
)

// Capability describes a single API action that the system can perform.
type Capability struct {
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Endpoint    string   `json:"endpoint"`
	Method      string   `json:"method"`
	Auth        bool     `json:"requires_auth"`
	Idempotent  bool     `json:"idempotent"`
	Async       bool     `json:"async,omitempty"`
	Tags        []string `json:"tags,omitempty"`
}

// RegisterCapabilitiesRoutes adds GET /api/v1/capabilities for client
// discovery of the kombifyTechstack API surface.
func RegisterCapabilitiesRoutes(r *httpx.Router, version string) {
	r.GET("/api/v1/capabilities", func(e *httpx.Event) error {
		return httpx.Success(e, http.StatusOK, map[string]any{
			"tool":         "kombify-techstack",
			"version":      version,
			"description":  "Hybrid cloud control plane for Homelabs -- validate YAML specs via CUE, generate OpenTofu configurations, and manage Nodes through a gRPC agent protocol.",
			"capabilities": capabilities(),
			"links": map[string]string{
				"openapi":           "/api/v1/openapi.yaml",
				"tool_manifest":     "/api/v1/tool-manifest.json",
				capabilityTagMCP:    "/api/v1/mcp",
				capabilityTagHealth: "/api/v1/health",
				"docs":              "https://docs.kombify.io/stack/api",
			},
		})
	})
}

func capabilities() []Capability {
	return []Capability{
		// Health & Info
		{Name: capabilityNameHealthCheck, Description: "Main health check for the service", Endpoint: "/api/v1/health", Method: http.MethodGet, Auth: false, Idempotent: true, Tags: []string{capabilityTagHealth}},
		{Name: "liveness_probe", Description: "Kubernetes liveness probe", Endpoint: "/api/v1/health/live", Method: http.MethodGet, Auth: false, Idempotent: true, Tags: []string{capabilityTagHealth}},
		{Name: "readiness_probe", Description: "Readiness probe (checks the database and configured critical listeners)", Endpoint: "/api/v1/health/ready", Method: http.MethodGet, Auth: false, Idempotent: true, Tags: []string{capabilityTagHealth}},
		{Name: "service_info", Description: "Service version, runtime stats, and uptime", Endpoint: "/api/v1/info", Method: http.MethodGet, Auth: false, Idempotent: true, Tags: []string{capabilityTagHealth}},
		{Name: "instance_info", Description: "Stable local instance identity (id, created_at, hostname, version)", Endpoint: "/api/v1/instance", Method: "GET", Auth: false, Idempotent: true, Tags: []string{"instance"}},
		{Name: "enrollment_status", Description: "Current enrollment record (mode, cloud tenant, linked_at)", Endpoint: "/api/v1/enrollment", Method: "GET", Auth: false, Idempotent: true, Tags: []string{"instance", "enrollment"}},
		{Name: "client_bootstrap", Description: "Public runtime config for the static SPA (edition, deployment mode, telemetry toggles)", Endpoint: "/api/v1/client/bootstrap", Method: "GET", Auth: false, Idempotent: true, Tags: []string{"instance"}},

		// Stacks - Lifecycle
		{Name: "get_homelab", Description: "Resolve the caller's Homelab umbrella with its StackKit deployments", Endpoint: "/api/v1/homelab", Method: "GET", Auth: true, Idempotent: true, Tags: []string{registryCollectionStacks, "homelab"}},
		{Name: "rename_homelab", Description: "Rename the caller's Homelab umbrella; the generated starting name is a placeholder", Endpoint: "/api/v1/homelab", Method: "PATCH", Auth: true, Idempotent: true, Tags: []string{"homelab"}},
		{Name: "list_kit_deployments", Description: "List the caller's StackKit deployments with canonical Homelab, StackKit, and deployment identities", Endpoint: "/api/v1/stacks", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "homelab"}},
		{Name: "create_stack", Description: "Create a StackKit deployment under the caller's Homelab and start provisioning", Endpoint: "/api/v1/stacks", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"stacks"}},
		{Name: "list_jobs", Description: "List owned jobs with optional StackKit deployment, type, state, and search filters", Endpoint: "/api/v1/jobs", Method: "GET", Auth: true, Idempotent: true, Tags: []string{capabilityTagJobs}},
		{Name: "provision_stack", Description: "Start provisioning an existing StackKit deployment", Endpoint: "/api/v1/stacks/{id}/provision", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"stacks"}},
		{Name: "deploy_stack", Description: "Run the full StackKit deployment rollout: unify + IaC + tofu apply", Endpoint: "/api/v1/stacks/{id}/deploy", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"stacks"}},
		{Name: "stackkit_lifecycle", Description: "Dispatch a closed typed StackKits lifecycle operation to an enrolled mTLS Node agent", Endpoint: "/api/v1/stacks/{id}/stackkit/operations", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"stacks", "operations", "ril"}},
		{Name: "resume_stack_enrollment", Description: "Idempotently resume an overdue provider, Guard, or enrollment wait on the exact existing managed lease without creating a provider VM", Endpoint: "/api/v1/stacks/{id}/resume-enrollment", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"stacks", capabilityTagJobs, registryResponseServersKey}},
		{Name: "resume_remote_enrollment", Description: "Idempotently retry connect-remote Guard enrollment on the persisted SSH connection without rerunning StackKit rollout", Endpoint: "/api/v1/stacks/{id}/resume-remote-enrollment", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"stacks", capabilityTagJobs, registryResponseServersKey}},
		{Name: "retry_stack_rollout", Description: "Idempotently retry one failed rollout on its exact active managed lease without creating a provider VM", Endpoint: "/api/v1/stacks/{id}/retry-rollout", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"stacks", capabilityTagJobs, registryResponseServersKey}},
		{Name: "abandon_stale_stack_job", Description: "Retire one owned rollout job, or a provision job durably waiting after lease creation, when it is still marked running but has stopped reporting", Endpoint: "/api/v1/stacks/{id}/jobs/{jobId}/abandon", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"stacks", capabilityTagJobs}},
		{Name: "get_stack_routing", Description: "Read exact desired, observed, and rollout routing state for a StackKit deployment", Endpoint: "/api/v1/stacks/{id}/routing", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "routing"}},
		{Name: "list_stack_routing_targets", Description: "List owner-visible primary managed Node lease targets without selecting or mutating one", Endpoint: "/api/v1/stacks/{id}/routing/targets", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "routing", "servers"}},
		{Name: "ensure_stack_routing", Description: "Idempotently persist desired custom-domain routing for one exact StackKit deployment and Node target", Endpoint: "/api/v1/stacks/{id}/routing", Method: "PUT", Auth: true, Idempotent: true, Async: true, Tags: []string{"stacks", "routing"}},
		{Name: "destroy_stack", Description: "Decommission all infrastructure for one StackKit deployment", Endpoint: "/api/v1/stacks/{id}/destroy", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"stacks"}},
		{Name: "prune_owned_stacks", Description: "Review and digest-apply only owner-scoped E2E/runtime-cloud control-plane residue. Never decommissions leases, stops Nodes, calls providers, or enqueues lifecycle work.", Endpoint: "/api/v1/stacks/prune-orphans", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"stacks"}},
		{Name: "stack_operations", Description: "Post-configuration operations dashboard payload", Endpoint: "/api/v1/stacks/{id}/operations", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "operations"}},
		{Name: "stack_server_details", Description: "Node metadata, health, services, checks, and logs for a StackKit deployment", Endpoint: "/api/v1/stacks/{id}/servers/{serverId}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "operations"}},
		{Name: "add_managed_runtime_server", Description: "Request or safely resume an additional kombify Cloud managed Node for an existing StackKit deployment", Endpoint: "/api/v1/stacks/{id}/managed-runtimes", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"stacks", "operations", "monthly-runtime"}},
		{Name: "assign_stack_worker", Description: "Assign an approved unscoped Node agent to a StackKit deployment before rollout", Endpoint: "/api/v1/stacks/{id}/workers/{workerId}/assign", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"stacks", "operations", "workers"}},
		{Name: "list_registry_services", Description: "List service catalog, managed services, and observed unmanaged services", Endpoint: "/api/v1/registry/services", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"registry", "services"}},
		{Name: "attach_registry_service", Description: "Attach a catalog service to a selected Node", Endpoint: "/api/v1/registry/services/attach", Method: "POST", Auth: true, Idempotent: false, Tags: []string{"registry", "services"}},
		{Name: "import_observed_service", Description: "Import an unmanaged Node service as observed inventory", Endpoint: "/api/v1/registry/services/import", Method: "POST", Auth: true, Idempotent: false, Tags: []string{"registry", "services"}},
		{Name: "import_home_assistant", Description: "Read and link an existing Home Assistant instance as observed inventory without changing its configuration", Endpoint: "/api/v1/registry/services/home-assistant/import", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"registry", "smart-home"}},
		{Name: "list_home_assistant_owners", Description: "List exact StackKits runtime owner enrollments awaiting an authorized Home Assistant connection", Endpoint: "/api/v1/home-assistant/owners", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"smart-home", "connection"}},
		{Name: "attach_home_assistant_owner", Description: "Authorize native API access for a previously enrolled Home Assistant runtime target; cannot replace its origin or configuration ownership", Endpoint: "/api/v1/home-assistant/owners/attach", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"smart-home", "connection"}},
		{Name: "backup_home_assistant", Description: "Create an encrypted immutable off-instance native backup using an explicitly authorized binding", Endpoint: "/api/v1/home-assistant/instances/{binding}/backup", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"smart-home", "backup"}},
		{Name: "restore_home_assistant", Description: "Restore an exact retained backup only to a separately authorized isolated target", Endpoint: "/api/v1/home-assistant/instances/{binding}/restore", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"smart-home", "backup"}},
		{Name: "update_home_assistant", Description: "Apply a controlled native update only after backup and exact isolated recovery evidence", Endpoint: "/api/v1/home-assistant/instances/{binding}/update", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"smart-home", "maintenance"}},
		{Name: "list_substrates", Description: "List owned enrolled hypervisors and their current management availability", Endpoint: "/api/v1/substrates", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"substrate"}},
		{Name: "create_substrate_appliance", Description: "Admit a distinct HAOS guest through customer substrate custody with retained idempotency", Endpoint: "/api/v1/substrates/{id}/appliances", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"substrate", "smart-home"}},
		{Name: "get_substrate_appliance", Description: "Read retained native guest operation status; application authentication is verified separately", Endpoint: "/api/v1/substrates/{id}/appliances/{lease}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"substrate", "smart-home"}},
		{Name: "start_home_assistant_migration", Description: "Start or resume the exact locally authorized native backup and isolated HAOS migration", Endpoint: "/api/v1/home-assistant/migrations", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"smart-home", "migration"}},
		{Name: "get_home_assistant_migration", Description: "Read owner-scoped Home Assistant migration steps and retained evidence", Endpoint: "/api/v1/home-assistant/migrations/{id}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"smart-home", "migration"}},
		{Name: "confirm_home_assistant_cutover", Description: "Explicitly confirm a waiting migration or return after its native preconditions", Endpoint: "/api/v1/home-assistant/migrations/{id}/confirm", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"smart-home", "migration"}},
		{Name: "return_home_assistant_migration", Description: "Back up the new state and return to the retained original without merging configurations", Endpoint: "/api/v1/home-assistant/migrations/{id}/rollback", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"smart-home", "migration"}},

		// Stacks - Specs
		{Name: "get_stack_intent", Description: "Get persisted kombination.yaml (byte-exact, base64)", Endpoint: "/api/v1/stacks/{id}/intent", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "specs"}},
		{Name: "get_stack_requirements", Description: "Get requirements spec (Phase 1 analysis output)", Endpoint: "/api/v1/stacks/{id}/requirements", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "specs"}},
		{Name: "get_stack_unified", Description: "Get unified spec (Phase 2 unification output)", Endpoint: "/api/v1/stacks/{id}/unified", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "specs"}},
		{Name: "verify_spec_chain", Description: "Verify spec hash chain integrity across phases", Endpoint: "/api/v1/stacks/{id}/verify-chain", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "specs"}},
		{Name: "pipeline_status", Description: "Get complete pipeline status for all spec phases", Endpoint: "/api/v1/stacks/{id}/pipeline-status", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "specs"}},

		// Stacks - Import/Export
		{Name: "export_stack", Description: "Export a StackKit deployment as kombination.yaml (YAML or JSON)", Endpoint: "/api/v1/stacks/{id}/export", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "import_export"}},
		{Name: "import_stack", Description: "Import a StackKit spec as a deployment under the caller's Homelab", Endpoint: "/api/v1/stacks/import", Method: "POST", Auth: true, Idempotent: false, Tags: []string{"stacks", "import_export"}},
		{Name: "validate_import", Description: "Validate a spec without importing it", Endpoint: "/api/v1/stacks/import/validate", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"stacks", "import_export"}},

		// Stacks - Drift
		{Name: "check_drift", Description: "Trigger infrastructure drift check (rate-limited to 5 min)", Endpoint: "/api/v1/stacks/{id}/drift/check", Method: "POST", Auth: true, Idempotent: true, Async: true, Tags: []string{"stacks", "drift"}},
		{Name: "get_drift_status", Description: "Get current drift status for a StackKit deployment", Endpoint: "/api/v1/stacks/{id}/drift/status", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "drift"}},
		{Name: "get_drift_details", Description: "Get detailed drift info with affected resources", Endpoint: "/api/v1/stacks/{id}/drift/details", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "drift"}},
		{Name: "resolve_drift", Description: "Trigger drift resolution by re-applying infrastructure", Endpoint: "/api/v1/stacks/{id}/drift/resolve", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"stacks", "drift"}},
		{Name: "list_drift_results", Description: "List all drift check results (paginated)", Endpoint: "/api/v1/drift/results", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stacks", "drift"}},

		// Wizard (native v2, beta)
		{Name: "preview_wizard_projection", Description: "Project a Wizard intent onto the pinned StackKit seed and validate the Architecture v2 result (beta, flag native_v2_wizard)", Endpoint: "/api/v1/wizard/preview", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"wizard", "stackkits"}},
		{Name: "run_wizard", Description: "Execute a Wizard run: found the caller's Homelab with its first StackKit deployment, or expand it with a new StackKit deployment or a Node joining an existing one; validate via pinned StackKits, persist, and dispatch provisioning (beta, flag native_v2_wizard; idempotent with X-Idempotency-Key)", Endpoint: "/api/v1/wizard/runs", Method: "POST", Auth: true, Idempotent: false, Tags: []string{"wizard", "stackkits"}},
		{Name: "get_active_wizard_run", Description: "Read the caller's latest Wizard run with its live provisioning-job snapshot (resume/banner source; beta, flag native_v2_wizard)", Endpoint: "/api/v1/wizard/runs/active", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"wizard", "stackkits"}},

		// Unifier Pipeline
		{Name: "validate_spec", Description: "Validate a kombination spec against CUE schemas (safe, idempotent)", Endpoint: "/api/v1/unifier/validate", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"unifier"}},
		{Name: "analyze_spec", Description: "Analyze spec to get hardware requirements, add-ons, credentials (safe, idempotent)", Endpoint: "/api/v1/unifier/analyze", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"unifier"}},
		{Name: "unify_spec", Description: "Unify specifications into a single resolved spec (Phase 2)", Endpoint: "/api/v1/unifier/unify", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"unifier"}},
		{Name: "run_pipeline", Description: "Run full unifier pipeline: validate + analyze + unify (side-effecting)", Endpoint: "/api/v1/unifier/pipeline", Method: "POST", Auth: true, Idempotent: false, Tags: []string{"unifier"}},
		{Name: "validate_pipeline", Description: "Validate pipeline inputs without executing", Endpoint: "/api/v1/unifier/pipeline/validate", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"unifier"}},
		{Name: "preview_pipeline", Description: "Preview pipeline results without side effects", Endpoint: "/api/v1/unifier/pipeline/preview", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"unifier"}},
		{Name: "recommend_stackkit", Description: "Evaluate Wizard intent through the authenticated Techstack recommendation authority without side effects", Endpoint: "/api/v1/unifier/recommendations", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"unifier", "wizard", "stackkits"}},

		// StackKits
		{Name: "list_stackkits", Description: "List available reusable StackKit definitions", Endpoint: "/api/v1/stackkits", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stackkits"}},
		{Name: "get_stackkit", Description: "Get specific StackKit details and schema", Endpoint: "/api/v1/stackkits/{name}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"stackkits"}},

		// Add-ons
		{Name: "list_addons", Description: "List available add-ons", Endpoint: "/api/v1/addons", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"addons"}},
		{Name: "detect_addons", Description: "Auto-detect applicable add-ons for a spec", Endpoint: "/api/v1/addons/detect", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"addons"}},

		// Workers
		{Name: "list_workers", Description: "List registered Node agents and managed runtime projections (filtered by owner)", Endpoint: "/api/v1/workers", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"workers"}},
		{Name: "list_servers", Description: "List canonical Node runtime state with Node and StackKit deployment identities", Endpoint: "/api/v1/servers", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"servers", "operations"}},
		{Name: "get_server", Description: "Get canonical Node lifecycle, connection, health, channels, and inventory revision", Endpoint: "/api/v1/servers/{serverId}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"servers", "operations"}},
		{Name: "get_server_ports", Description: "Get owner-scoped desired, reserved, observed, exposed, and drift-qualified ports for one canonical Node", Endpoint: "/api/v1/servers/{serverId}/ports", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"servers", "inventory"}},
		{Name: "detach_self_owned_server", Description: "Revoke and tombstone one exact customer-operated BYO Node without provider mutation", Endpoint: "/api/v1/servers/{serverId}/detach", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"servers", "operations"}},
		{Name: "list_inventory_servers", Description: "List owner-scoped sanitized Node inventory for UI and agents", Endpoint: "/api/v1/inventory/servers", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"servers", "inventory", capabilityTagMCP}},
		{Name: "get_inventory_server_health", Description: "Get freshness-qualified health for one owned Node", Endpoint: "/api/v1/inventory/servers/{serverId}/health", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"servers", "inventory", capabilityTagMCP}},
		{Name: "get_inventory_server_ports", Description: "Get desired, reserved, observed, exposed, and drift-qualified ports for one owned Node", Endpoint: "/api/v1/inventory/servers/{serverId}/ports", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"servers", "inventory", capabilityTagMCP}},
		{Name: "list_inventory_services", Description: "List owner-scoped sanitized service health and links", Endpoint: "/api/v1/inventory/services", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"services", "inventory", capabilityTagMCP}},
		{Name: "get_inventory_server_access_context", Description: "Get sanitized Node addresses, channel state, and service links without credentials or SSH material", Endpoint: "/api/v1/inventory/servers/{serverId}/access-context", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"servers", "inventory", capabilityTagMCP}},
		{Name: "register_worker", Description: "Register a worker via pairing token", Endpoint: "/api/v1/workers/register", Method: "POST", Auth: false, Idempotent: false, Tags: []string{"workers"}},
		{Name: "approve_worker", Description: "Approve a pending worker registration", Endpoint: "/api/v1/workers/{id}/approve", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"workers"}},

		// Agents (gRPC)
		{Name: "list_agents", Description: "List connected gRPC agents", Endpoint: "/api/v1/agents", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"agents"}},
		{Name: "get_agent", Description: "Get agent by ID with resource details", Endpoint: "/api/v1/agents/{id}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"agents"}},
		{Name: "send_agent_command", Description: "Send a command to a connected agent", Endpoint: "/api/v1/agents/{id}/command", Method: "POST", Auth: true, Idempotent: false, Tags: []string{"agents"}},
		{Name: "stream_agent_logs", Description: "Stream live agent logs via SSE", Endpoint: "/api/v1/agents/{id}/logs/stream", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"agents", "streaming"}},

		// Jobs
		{Name: "get_job", Description: "Get current job state for UI polling", Endpoint: "/api/v1/jobs/{id}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{capabilityTagJobs}},
		{Name: "stream_job", Description: "Stream job progress via Server-Sent Events", Endpoint: "/api/v1/jobs/{id}/stream", Method: "GET", Auth: true, Idempotent: true, Tags: []string{capabilityTagJobs, "streaming"}},
		{Name: "get_job_logs", Description: "Get logs for a completed or running job", Endpoint: "/api/v1/jobs/{id}/logs", Method: "GET", Auth: true, Idempotent: true, Tags: []string{capabilityTagJobs}},
		{Name: "get_job_stats", Description: "Get job queue statistics", Endpoint: "/api/v1/jobs/stats", Method: "GET", Auth: true, Idempotent: true, Tags: []string{capabilityTagJobs}},

		// Discovery
		{Name: "get_networks", Description: "Get local network interfaces for scanning", Endpoint: "/api/v1/discovery/networks", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"discovery"}},
		{Name: "start_scan", Description: "Start async network scan for devices", Endpoint: "/api/v1/discovery/scan", Method: "POST", Auth: true, Idempotent: false, Async: true, Tags: []string{"discovery"}},
		{Name: "get_scan", Description: "Get scan status and results", Endpoint: "/api/v1/discovery/scan/{id}", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"discovery"}},
		{Name: "list_devices", Description: "Get all discovered network devices", Endpoint: "/api/v1/discovery/devices", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"discovery"}},
		{Name: "test_ssh", Description: "Test SSH connectivity to a device", Endpoint: "/api/v1/discovery/test-ssh", Method: "POST", Auth: true, Idempotent: true, Tags: []string{"discovery"}},

		// Trust
		{Name: "list_pairing_tokens", Description: "List pairing tokens for worker registration", Endpoint: "/api/v1/trust/pairing-tokens", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"trust"}},
		{Name: "create_pairing_token", Description: "Create a pairing token for worker registration", Endpoint: "/api/v1/trust/pairing-tokens", Method: "POST", Auth: true, Idempotent: false, Tags: []string{"trust"}},

		// Tunnel
		{Name: "tunnel_status", Description: "Get Cloudflare tunnel connection status", Endpoint: "/api/v1/tunnel/status", Method: "GET", Auth: false, Idempotent: true, Tags: []string{"tunnel"}},
		{Name: "start_tunnel", Description: "Start Cloudflare tunnel for NAT traversal", Endpoint: "/api/v1/tunnel/start", Method: "POST", Auth: false, Idempotent: false, Tags: []string{"tunnel"}},
		{Name: "stop_tunnel", Description: "Stop Cloudflare tunnel", Endpoint: "/api/v1/tunnel/stop", Method: "POST", Auth: false, Idempotent: false, Tags: []string{"tunnel"}},

		// Features
		{Name: "list_features", Description: "List all feature flags grouped by category", Endpoint: "/api/v1/features", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"features"}},

		// Auth
		{Name: "get_auth_mode", Description: "Get current auth mode (local or cloud)", Endpoint: "/api/v1/auth/mode", Method: "GET", Auth: false, Idempotent: true, Tags: []string{"auth"}},
		{Name: "verify_portal_sso", Description: "Verify SSO token from kombify portal", Endpoint: "/api/v1/auth/portal-verify", Method: "POST", Auth: false, Idempotent: true, Tags: []string{"auth"}},

		// Metrics
		{Name: "prometheus_metrics", Description: "Prometheus-formatted instance metrics (admin/infra only)", Endpoint: "/metrics", Method: "GET", Auth: true, Idempotent: true, Tags: []string{"metrics"}},
	}
}
