// Code generated from kombify-Core/standards/entitlements.v1.json by scripts/entitlements/generate.mjs. DO NOT EDIT.
// Regenerate with `mise run entitlements:generate`; `mise run entitlements:check` fails when this file is stale.

// Package entitlements is the generated Go projection of the kombify
// entitlement vocabulary: plan tiers, roles, knowledge tiers, AI cohorts,
// the entitlement-key registry, documented route requirements and the
// denial reason-code registry.
package entitlements

import "strings"

// VocabularyVersion is the version of kombify-Core/standards/entitlements.v1.json.
const VocabularyVersion = "1"

// PlanTier is a canonical plan tier (ENTITLEMENTS-ARCHITECTURE section 3).
type PlanTier string

// PlanTierAnonymous is the canonical plan tier "anonymous".
const PlanTierAnonymous PlanTier = "anonymous"

// PlanTierFree is the canonical plan tier "free".
const PlanTierFree PlanTier = "free"

// PlanTierStarter is the canonical plan tier "starter".
const PlanTierStarter PlanTier = "starter"

// PlanTierPro is the canonical plan tier "pro".
const PlanTierPro PlanTier = "pro"

// PlanTierAyn is the canonical plan tier "ayn".
const PlanTierAyn PlanTier = "ayn"

// CanonicalPlanTiers lists the canonical tiers in ascending rank.
var CanonicalPlanTiers = []PlanTier{PlanTierAnonymous, PlanTierFree, PlanTierStarter, PlanTierPro, PlanTierAyn}

// PlanTierRank orders canonical tiers; only the order is contractual.
var PlanTierRank = map[PlanTier]int{
	PlanTierAnonymous: -1, PlanTierFree: 0, PlanTierStarter: 1, PlanTierPro: 2, PlanTierAyn: 4,
}

var planTierAliasPairs = [][2]string{
	{"anonymous", "anonymous"},
	{"free", "free"},
	{"starter", "starter"},
	{"pro", "pro"},
	{"ayn", "ayn"},
	{"public", "anonymous"},
	{"premium", "pro"},
	{"pro_plus", "pro"},
	{"proplus", "pro"},
	{"all_you_need", "ayn"},
	{"all_you_need_tier", "ayn"},
	{"business", "ayn"},
	{"enterprise", "ayn"},
	{"team", "ayn"},
	{"staff", "ayn"},
	{"internal", "ayn"},
}

// PlanTierAliases maps every accepted spelling after NormalizeKey, canonical tiers included.
var PlanTierAliases = func() map[string]PlanTier {
	aliases := make(map[string]PlanTier, len(planTierAliasPairs))
	for _, pair := range planTierAliasPairs {
		aliases[pair[0]] = PlanTier(pair[1])
	}
	return aliases
}()

// PlanTierUnknownFallback is the tier of an unknown, non-empty spelling.
const PlanTierUnknownFallback = PlanTierFree

// PlanTierMissingClaimFallback is the tier of a principal without a plan claim.
const PlanTierMissingClaimFallback = PlanTierAnonymous

// TotalAccessEntitlementToken is an ingress alias only, never entitlement authority.
const TotalAccessEntitlementToken = "*"

// NormalizeKey trims, lowercases, collapses every run outside [a-z0-9] to one underscore and strips edge underscores.
func NormalizeKey(value string) string {
	var builder strings.Builder
	pending := false
	for _, r := range strings.ToLower(strings.TrimSpace(value)) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			if pending && builder.Len() > 0 {
				builder.WriteByte('_')
			}
			pending = false
			builder.WriteRune(r)
			continue
		}
		pending = true
	}
	return builder.String()
}

// ParsePlanTier resolves a raw spelling to its canonical tier; ok is false for unknown values.
func ParsePlanTier(value string) (PlanTier, bool) {
	tier, ok := PlanTierAliases[NormalizeKey(value)]
	return tier, ok
}

// NormalizePlanTier is ParsePlanTier with PlanTierUnknownFallback.
func NormalizePlanTier(value string) PlanTier {
	if tier, ok := ParsePlanTier(value); ok {
		return tier
	}
	return PlanTierUnknownFallback
}

// Role is a canonical staff or customer role (ENTITLEMENTS-ARCHITECTURE section 3.1).
type Role string

// RoleGlobalAdmin is the canonical role "global_admin".
const RoleGlobalAdmin Role = "global_admin"

// RoleAdmin is the canonical role "admin".
const RoleAdmin Role = "admin"

// RoleDeveloper is the canonical role "developer".
const RoleDeveloper Role = "developer"

// RoleManager is the canonical role "manager".
const RoleManager Role = "manager"

// RoleUser is the canonical role "user".
const RoleUser Role = "user"

// Roles lists every role in descending privilege order.
var Roles = []Role{RoleGlobalAdmin, RoleAdmin, RoleDeveloper, RoleManager, RoleUser}

// StaffRoles lists the roles that are staff.
var StaffRoles = []Role{RoleGlobalAdmin, RoleAdmin, RoleDeveloper, RoleManager}

// RoleLevel is the privilege level of each role.
var RoleLevel = map[Role]int{
	RoleGlobalAdmin: 100, RoleAdmin: 90, RoleDeveloper: 80, RoleManager: 60, RoleUser: 10,
}

// ParseRole resolves a claim value to its canonical role. Retired aliases and cohort labels never resolve.
func ParseRole(value string) (Role, bool) {
	const namespace = "kombify:"
	trimmed := strings.TrimSpace(value)
	if len(trimmed) >= len(namespace) && strings.EqualFold(trimmed[:len(namespace)], namespace) {
		trimmed = trimmed[len(namespace):]
	}
	role := Role(NormalizeKey(trimmed))
	_, ok := RoleLevel[role]
	return role, ok
}

// IsStaff reports whether the role is a staff role.
func (r Role) IsStaff() bool {
	for _, staff := range StaffRoles {
		if r == staff {
			return true
		}
	}
	return false
}

// KnowledgeTier is a canonical knowledge tier (ENTITLEMENTS-ARCHITECTURE section 3.2).
type KnowledgeTier string

var knowledgeTierAliasPairs = [][2]string{
	{"public", "public"},
	{"customer", "customer"},
	{"developer", "developer"},
	{"internal", "internal"},
	{"owner", "owner"},
	{"free", "public"},
	{"client", "customer"},
	{"dev", "developer"},
	{"staff", "internal"},
	{"admin", "owner"},
}

// KnowledgeTiers lists the canonical knowledge tiers in ascending rank.
var KnowledgeTiers = []KnowledgeTier{"public", "customer", "developer", "internal", "owner"}

// KnowledgeTierAliases maps every accepted spelling after NormalizeKey.
var KnowledgeTierAliases = func() map[string]KnowledgeTier {
	aliases := make(map[string]KnowledgeTier, len(knowledgeTierAliasPairs))
	for _, pair := range knowledgeTierAliasPairs {
		aliases[pair[0]] = KnowledgeTier(pair[1])
	}
	return aliases
}()

// KnowledgeTierRank orders the canonical knowledge tiers.
var KnowledgeTierRank = map[KnowledgeTier]int{
	"public": 0, "customer": 1, "developer": 2, "internal": 3, "owner": 4,
}

// NormalizeKnowledgeTier resolves a spelling, falling back to the unknown fallback tier.
func NormalizeKnowledgeTier(value string) KnowledgeTier {
	if tier, ok := KnowledgeTierAliases[NormalizeKey(value)]; ok {
		return tier
	}
	return "public"
}

// AiCohort is an AI capability cohort; a separate axis, not a plan tier.
type AiCohort string

var aiCohortAliasPairs = [][2]string{
	{"free", "free"},
	{"standard", "standard"},
	{"premium", "premium"},
	{"ultra", "ultra"},
	{"minimal", "free"},
}

// AiCohortAliases maps every accepted cohort spelling after NormalizeKey.
var AiCohortAliases = func() map[string]AiCohort {
	aliases := make(map[string]AiCohort, len(aiCohortAliasPairs))
	for _, pair := range aiCohortAliasPairs {
		aliases[pair[0]] = AiCohort(pair[1])
	}
	return aliases
}()

// RiskClass is the interaction-card risk class of what a key unlocks.
type RiskClass string

// EntitlementKeyDefinition is one registered entitlement key.
type EntitlementKeyDefinition struct {
	Key         string
	Owner       string
	RiskClass   RiskClass
	GrantSource string
	Description string
}

// EntitlementKeys is the registry, sorted by key.
var EntitlementKeys = []EntitlementKeyDefinition{
	{Key: "agent.ril.manager", Owner: "kombify-AI-Platform", RiskClass: "destructive", GrantSource: "plan", Description: "RIL manager agent"},
	{Key: "agent.ril.server", Owner: "kombify-AI-Platform", RiskClass: "destructive", GrantSource: "plan", Description: "RIL server agent"},
	{Key: "agent.run", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Agent runtime access"},
	{Key: "agent.tier.premium", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Agent premium tier"},
	{Key: "agent.tier.standard", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Agent standard tier"},
	{Key: "agent.tier.ultra", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Agent ultra tier"},
	{Key: "ai.memory.read", Owner: "kombify-AI-Platform", RiskClass: "none", GrantSource: "plan", Description: "Agent recall of the account's own AI memory"},
	{Key: "ai.models", Owner: "kombify-AI-Platform", RiskClass: "none", GrantSource: "policy", Description: "AI model catalog route"},
	{Key: "ai.tier.premium", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "AI premium tier"},
	{Key: "ai.tier.standard", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "AI standard tier"},
	{Key: "ai.tier.ultra", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "AI ultra tier"},
	{Key: "ai.tokens.ayn", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "AI token budget All-You-Need"},
	{Key: "ai.tokens.pro", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "AI token budget Pro"},
	{Key: "ai.tokens.starter", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "AI token budget Starter"},
	{Key: "automations.platform", Owner: "kombify-Automations", RiskClass: "external_write", GrantSource: "plan", Description: "Workpaths authoring through the platform automation engine"},
	{Key: "automations.schedule", Owner: "kombify-Automations", RiskClass: "external_write", GrantSource: "plan", Description: "Hosted schedule registration and background dispatch for automations"},
	{Key: "cloud.chat.history.export", Owner: "kombify-Cloud", RiskClass: "disclosure", GrantSource: "plan", Description: "Chat history export"},
	{Key: "cloud.runtime.credits.ayn", Owner: "kombify-Cloud", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Cloud runtime credits AYN"},
	{Key: "cloud.runtime.credits.pro", Owner: "kombify-Cloud", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Cloud runtime credits Pro"},
	{Key: "cloud.runtime.credits.starter", Owner: "kombify-Cloud", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Cloud runtime credits Starter"},
	{Key: "cloud.sla.automated", Owner: "kombify-Cloud", RiskClass: "none", GrantSource: "plan", Description: "Automated availability SLA"},
	{Key: "connect.environments.link", Owner: "kombify-Gateway", RiskClass: "local_reversible", GrantSource: "account_baseline", Description: "Link a Workbench environment and hold a grant on it"},
	{Key: "connect.installations.manage", Owner: "kombify-Gateway", RiskClass: "local_reversible", GrantSource: "account_baseline", Description: "Register, confirm and revoke device installations"},
	{Key: "mcp.customer.access", Owner: "kombify-Gateway", RiskClass: "none", GrantSource: "plan", Description: "Customer MCP connector access"},
	{Key: "mcp.github.read", Owner: "kombify-Gateway", RiskClass: "none", GrantSource: "plan", Description: "User-delegated GitHub repository read"},
	{Key: "mcp.owner.full_access", Owner: "kombify-Gateway", RiskClass: "destructive", GrantSource: "policy", Description: "Owner-only MCP tool subset (feature flag, not a staff role)"},
	{Key: "paperwork.agent.identity.provision", Owner: "kombify-Paperwork", RiskClass: "external_write", GrantSource: "plan", Description: "Provision an agent mail identity (signed approval per call)"},
	{Key: "paperwork.agent.identity.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "List and inspect the agent mail identities of the account"},
	{Key: "paperwork.ai.hosted", Owner: "kombify-Paperwork", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Paperwork hosted AI assistance"},
	{Key: "paperwork.document.destinations.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "List document stores, folders and file sources an agent may write to"},
	{Key: "paperwork.documents.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "account_baseline", Description: "Read the account's own Paperwork documents"},
	{Key: "paperwork.documents.write", Owner: "kombify-Paperwork", RiskClass: "local_reversible", GrantSource: "plan", Description: "Create document spaces, folders and documents in Paperwork"},
	{Key: "paperwork.drive.write", Owner: "kombify-Paperwork", RiskClass: "external_write", GrantSource: "plan", Description: "Create Drive folders and upload files to the connected Drive"},
	{Key: "paperwork.integrations.configure", Owner: "kombify-Paperwork", RiskClass: "local_reversible", GrantSource: "plan", Description: "Select the resources a Paperwork integration may use"},
	{Key: "paperwork.integrations.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "List Paperwork integration providers, connections and resources"},
	{Key: "paperwork.lists.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "account_baseline", Description: "Read the account's own Paperwork lists"},
	{Key: "paperwork.mail.address.create", Owner: "kombify-Paperwork", RiskClass: "external_write", GrantSource: "plan", Description: "Create a private mail address on a Paperwork domain"},
	{Key: "paperwork.mail.address.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "List the mail domains and private mail addresses of the account"},
	{Key: "paperwork.mail.draft", Owner: "kombify-Paperwork", RiskClass: "local_reversible", GrantSource: "plan", Description: "Create mail drafts in Paperwork; sending is separate"},
	{Key: "paperwork.mail.provider.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "Discover agent mail domains and sender identities"},
	{Key: "paperwork.mail.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "account_baseline", Description: "Read the account's own Paperwork mail"},
	{Key: "paperwork.mail.send", Owner: "kombify-Paperwork", RiskClass: "external_write", GrantSource: "plan", Description: "Send a reviewed Paperwork draft as mail"},
	{Key: "paperwork.meetings.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "Read Paperwork calendars, events and free slots"},
	{Key: "paperwork.meetings.write", Owner: "kombify-Paperwork", RiskClass: "external_write", GrantSource: "plan", Description: "Create, move and answer Paperwork calendar events"},
	{Key: "paperwork.onboarding.configure", Owner: "kombify-Paperwork", RiskClass: "local_reversible", GrantSource: "plan", Description: "Advance, skip or complete Paperwork onboarding and choose its modules"},
	{Key: "paperwork.onboarding.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "Read the Paperwork onboarding state and modules"},
	{Key: "paperwork.plus", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "Paperwork Plus"},
	{Key: "paperwork.preferences.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "account_baseline", Description: "Read the account's own Paperwork preferences"},
	{Key: "paperwork.preferences.write", Owner: "kombify-Paperwork", RiskClass: "local_reversible", GrantSource: "account_baseline", Description: "Edit the account's own Paperwork preferences"},
	{Key: "paperwork.search.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "Unified search across Paperwork"},
	{Key: "paperwork.tags.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "Read Paperwork tags and search by tag"},
	{Key: "paperwork.tags.write", Owner: "kombify-Paperwork", RiskClass: "local_reversible", GrantSource: "plan", Description: "Create, assign, unassign and move Paperwork tags"},
	{Key: "paperwork.tasks.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "Read Paperwork tasks"},
	{Key: "paperwork.tasks.write", Owner: "kombify-Paperwork", RiskClass: "local_reversible", GrantSource: "account_baseline", Description: "Edit the account's own Paperwork tasks"},
	{Key: "paperwork.workspace.alias.provision", Owner: "kombify-Paperwork", RiskClass: "external_write", GrantSource: "plan", Description: "Provision a workspace mail alias in the Paperwork directory"},
	{Key: "paperwork.workspace.alias.read", Owner: "kombify-Paperwork", RiskClass: "none", GrantSource: "plan", Description: "List the workspace mail aliases of the account"},
	{Key: "sites.ai.builder", Owner: "kombify-Cubi", RiskClass: "cost_bearing", GrantSource: "plan", Description: "kombify Sites AI builder budget"},
	{Key: "sites.hosting.pro", Owner: "kombify-Cubi", RiskClass: "cost_bearing", GrantSource: "plan", Description: "kombify Sites Pro hosting tier"},
	{Key: "sites.hosting.starter", Owner: "kombify-Cubi", RiskClass: "cost_bearing", GrantSource: "plan", Description: "kombify Sites Starter hosting tier"},
	{Key: "speechkit.device.fleet", Owner: "kombify-SpeechKit", RiskClass: "local_reversible", GrantSource: "plan", Description: "SpeechKit device fleet"},
	{Key: "speechkit.homeassistant.bridge", Owner: "kombify-SpeechKit", RiskClass: "external_write", GrantSource: "plan", Description: "SpeechKit Home Assistant bridge"},
	{Key: "speechkit.provider.managed.stt", Owner: "kombify-SpeechKit", RiskClass: "cost_bearing", GrantSource: "plan", Description: "SpeechKit managed STT provider"},
	{Key: "speechkit.provider.managed.tts", Owner: "kombify-SpeechKit", RiskClass: "cost_bearing", GrantSource: "plan", Description: "SpeechKit managed TTS provider"},
	{Key: "speechkit.provider.managed.voiceagent", Owner: "kombify-SpeechKit", RiskClass: "cost_bearing", GrantSource: "plan", Description: "SpeechKit managed voice-agent provider"},
	{Key: "speechkit.server.hosted", Owner: "kombify-SpeechKit", RiskClass: "cost_bearing", GrantSource: "plan", Description: "SpeechKit hosted server"},
	{Key: "speechkit.voiceagent.live", Owner: "kombify-SpeechKit", RiskClass: "cost_bearing", GrantSource: "plan", Description: "SpeechKit live voice agent"},
	{Key: "supplydock.access", Owner: "kombify-Gateway", RiskClass: "none", GrantSource: "policy", Description: "Supply Dock capability activation and eligibility routes"},
	{Key: "techstack.backup.config", Owner: "kombify-Techstack", RiskClass: "local_reversible", GrantSource: "plan", Description: "Backup configuration class"},
	{Key: "techstack.backup.content", Owner: "kombify-Techstack", RiskClass: "local_reversible", GrantSource: "plan", Description: "Backup content class"},
	{Key: "techstack.backup.storage.ayn", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Backup storage budget All-You-Need"},
	{Key: "techstack.backup.storage.pro", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Backup storage budget Pro"},
	{Key: "techstack.backup.storage.starter", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Backup storage budget Starter"},
	{Key: "techstack.inventory.operate", Owner: "kombify-Techstack", RiskClass: "destructive", GrantSource: "plan", Description: "Operate stacks and servers through the Customer MCP (rollouts, routing, start and stop); approval receipt per destructive call"},
	{Key: "techstack.inventory.provision", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Provision stacks and managed servers through the Customer MCP; approval receipt per cost-bearing call"},
	{Key: "techstack.inventory.read", Owner: "kombify-Techstack", RiskClass: "none", GrantSource: "policy", Description: "Read-only Techstack inventory projection (shared public demo)"},
	{Key: "techstack.inventory.write", Owner: "kombify-Techstack", RiskClass: "local_reversible", GrantSource: "plan", Description: "Techstack inventory write access"},
	{Key: "techstack.managed.runtime", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Managed runtime"},
	{Key: "techstack.managed.runtime.centron", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Managed runtime Centron"},
	{Key: "techstack.managed.runtime.cloudkit", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Managed runtime Cloud Kit"},
	{Key: "techstack.managed.runtime.ionos", Owner: "kombify-Techstack", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Managed runtime IONOS"},
	{Key: "techstack.ril.actions", Owner: "kombify-Techstack", RiskClass: "destructive", GrantSource: "plan", Description: "RIL actions"},
	{Key: "techstack.ril.read", Owner: "kombify-Techstack", RiskClass: "none", GrantSource: "plan", Description: "RIL read access"},
	{Key: "techstack.servers.maintenance", Owner: "kombify-Techstack", RiskClass: "destructive", GrantSource: "plan", Description: "Server maintenance (reboot and OS updates)"},
	{Key: "web.browser.login", Owner: "kombify-AI-Platform", RiskClass: "external_write", GrantSource: "plan", Description: "Agent browser sign-in with stored logins"},
	{Key: "web.browser.read", Owner: "kombify-AI-Platform", RiskClass: "none", GrantSource: "plan", Description: "Agent browser access"},
	{Key: "web.browser.session", Owner: "kombify-AI-Platform", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Agent browser sessions"},
	{Key: "web.browser.submit", Owner: "kombify-AI-Platform", RiskClass: "external_write", GrantSource: "plan", Description: "Agent browser form submissions"},
	{Key: "web.search.read", Owner: "kombify-Gateway", RiskClass: "cost_bearing", GrantSource: "plan", Description: "Agent web search access"},
}

// IsEntitlementKey reports whether key is registered.
func IsEntitlementKey(key string) bool {
	for _, entry := range EntitlementKeys {
		if entry.Key == key {
			return true
		}
	}
	return false
}

// RouteRequirement is a documented exact-key requirement of one route.
type RouteRequirement struct {
	Route       string
	Surface     string
	RequiresAll []string
	EnforcedBy  string
}

// RouteRequirements lists the documented route requirements.
var RouteRequirements = []RouteRequirement{
	{Route: "/v1/agents/*", Surface: "http", RequiresAll: []string{"agent.run"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/v1/execution/*", Surface: "http", RequiresAll: []string{"agent.run"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/v1/ai/models", Surface: "http", RequiresAll: []string{"ai.models"}, EnforcedBy: "kombify-Gateway"},
	{Route: "GET /v1/ai/agent-instances", Surface: "http", RequiresAll: []string{"agent.run"}, EnforcedBy: "kombify-Gateway"},
	{Route: "POST /v1/techstack/servers/{id}/actions", Surface: "http", RequiresAll: []string{"techstack.servers.maintenance"}, EnforcedBy: "kombify-Techstack"},
	{Route: "/v1/supply-dock/* (activation and eligibility)", Surface: "http", RequiresAll: []string{"supplydock.access"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/v1/mcp/public/workpaths", Surface: "mcp", RequiresAll: []string{"automations.platform"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/mcp tool web.search", Surface: "mcp", RequiresAll: []string{"web.search.read"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/mcp tool agent-browser.session", Surface: "mcp", RequiresAll: []string{"web.browser.session"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/mcp tool agent-browser.sign_in", Surface: "mcp", RequiresAll: []string{"web.browser.login"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/mcp tool agent-browser.fill_secret", Surface: "mcp", RequiresAll: []string{"web.browser.login"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/mcp tool agent-browser.submit", Surface: "mcp", RequiresAll: []string{"web.browser.submit"}, EnforcedBy: "kombify-Gateway"},
	{Route: "/v1/mcp/opt (owner MCP subset)", Surface: "mcp", RequiresAll: []string{"mcp.owner.full_access"}, EnforcedBy: "kombify-Gateway"},
}

// ReasonCodeDefinition describes one denial reason code or code family.
type ReasonCodeDefinition struct {
	Code      string
	Category  string
	Retryable bool
	Owner     string
}

// ReasonCodes is the exact reason-code registry.
var ReasonCodes = []ReasonCodeDefinition{
	{Code: "missing_authorization", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "token_expired", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "token_not_yet_valid", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "malformed_token", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "unsupported_algorithm", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "unknown_kid", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "signature_invalid", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "issuer_mismatch", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "audience_mismatch", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "invalid_token", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "invalid_api_key", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "missing_auth0_configuration", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "missing_edge_auth_secret", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "jwks_unreachable", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "internal_error", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "agent_auth_context_secret_missing", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "staff_role_required", Category: "authorization", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "entitlement_denied", Category: "entitlement", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "agent_entitlement_denied", Category: "entitlement", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "agent_fga_error", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "execution_provider_credentials_forbidden", Category: "policy", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "automations_engine_not_activated", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "origin_not_configured", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "upstream_unavailable", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "me_ingress_plan_required", Category: "entitlement", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "me_ingress_entitlement_context_unavailable", Category: "availability", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "rate_limited", Category: "rate_limit", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "limiter_missing", Category: "availability", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "limiter_error", Category: "availability", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "server_maintenance_step_up_required", Category: "assurance", Retryable: true, Owner: "kombify-Gateway"},
	{Code: "agent_tier_denied", Category: "entitlement", Retryable: false, Owner: "kombify-AI-Platform"},
}

// ReasonCodePrefixes registers code families by prefix; Code holds the prefix.
var ReasonCodePrefixes = []ReasonCodeDefinition{
	{Code: "service_auth_", Category: "authentication", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "execution_egress_", Category: "policy", Retryable: false, Owner: "kombify-Gateway"},
	{Code: "agent_budget_", Category: "quota", Retryable: false, Owner: "kombify-Gateway"},
}

// DelegatedReasonPrefixes are code families owned by their own registry.
var DelegatedReasonPrefixes = []string{"connect_"}

// LookupReasonCode returns the registered definition of code; delegated prefixes resolve in their own registry.
func LookupReasonCode(code string) (ReasonCodeDefinition, bool) {
	for _, definition := range ReasonCodes {
		if definition.Code == code {
			return definition, true
		}
	}
	for _, definition := range ReasonCodePrefixes {
		if strings.HasPrefix(code, definition.Code) {
			return definition, true
		}
	}
	return ReasonCodeDefinition{}, false
}
