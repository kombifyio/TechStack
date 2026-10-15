package walletmigration

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// Resolution says how a caller finds a wallet row today.
type Resolution string

const (
	// ResolutionNameLabel: case-insensitive match on metadata name plus kind,
	// first hit in list order. These callers must switch to item ids.
	ResolutionNameLabel Resolution = "name_label"
	// ResolutionItemID: an explicit Techstack item id.
	ResolutionItemID Resolution = "item_id"
	// ResolutionCompositeID: an id composed from the stack and a service id.
	ResolutionCompositeID Resolution = "composite_item_id"
	// ResolutionServiceRef: owner, stack, service_id and source_ref.
	ResolutionServiceRef Resolution = "service_ref"
)

// Caller is a code path that reads or writes Techstack wallet rows and must
// move to the Wallet item id when the Wallet becomes the source.
type Caller struct {
	Location   string     `json:"location"`
	Resolution Resolution `json:"resolution"`
	Kinds      []string   `json:"kinds"`
	Use        string     `json:"use"`
}

// KnownCallers lists the wallet callers at the inspected revision. The design
// document holds the switch step for each.
func KnownCallers() []Caller {
	return []Caller{
		{"internal/routes/stacks/wizard_remote_wallet.go resolveWalletSSHKey", ResolutionNameLabel, []string{kindSSHKey}, "SSH private key for remote enrollment and rollout"},
		{"internal/routes/stacks/wizard_remote_connection.go resolveWalletSSHPassword", ResolutionNameLabel, []string{kindSSHPassword}, "SSH password for remote enrollment and rollout"},
		{"internal/routes/wizard_remote_ssh.go resolveWalletCredentialSecret", ResolutionNameLabel, []string{kindSSHKey}, "SSH private key for the wizard connection test"},
		{"internal/routes/server_terminal.go authorizeUserKey", ResolutionItemID, []string{kindSSHKey}, "SSH public key installed on a managed server"},
		{"internal/routes/stacks/owner_spec.go ownerSpecResponseFromControlPlane", ResolutionCompositeID, []string{kindOther}, "recovery passphrase hash handed to the StackKit owner bootstrap"},
		{"internal/routes/wallet.go", ResolutionItemID, []string{"*"}, "owner list, create, update, delete and reveal API"},
		{"internal/walletsync/stackkit_outputs.go SyncStackKitOutputsToStore", ResolutionServiceRef, []string{kindOther}, "writer: StackKit identity outputs"},
		{"internal/routes/stacks/owner_bootstrap.go upsertWalletStoreEntry", ResolutionCompositeID, []string{kindOther}, "writer: owner access and recovery entries"},
		{"internal/systemwallet/credentials.go UpsertCredential", ResolutionItemID, []string{kindPassword}, "writer: system account recovery passwords"},
		{"internal/routes/stacks/wizard_remote_connection.go upsertRemoteSSHPasswordWalletItem", ResolutionCompositeID, []string{kindSSHPassword}, "writer: remote SSH password"},
	}
}

// Remote authentication methods as the wizard persists them.
const (
	authMethodPassword = "password"
	authMethodSSHKey   = "ssh-key"
)

// StackCredentialRef is a stack's persisted reference to a wallet credential
// by name label (stack config server_remote_credential_ref or
// server_remote_ssh_key_label).
type StackCredentialRef struct {
	TenantID   string
	StackID    string
	OwnerID    string
	AuthMethod string
	Label      string
	// Host, User and Port are the stack's persisted remote target; a node
	// access item is bound to them. Port is zero when the stack names none.
	Host string
	User string
	Port int
	// ItemID is the Wallet item the stack already reads
	// (server_remote_credential_item_id); such a stack needs no switch.
	ItemID string
	// Revision is the stack revision (updated_at) the reference was read at,
	// which the switch compares and swaps against. The reader sets it.
	Revision time.Time
}

// StackCredentialRefFromConfig derives the reference the remote-connection
// path resolves for a stack, mirroring credentialsFromStackConfig. ok is false
// for a stack without a persisted remote SSH target.
func StackCredentialRefFromConfig(tenantID, stackID, ownerID string, config map[string]any) (StackCredentialRef, bool) {
	if configString(config, "server_remote_host") == "" || configString(config, "server_remote_user") == "" {
		return StackCredentialRef{}, false
	}
	method := strings.ToLower(configString(config, "server_remote_auth_method"))
	if method == "" {
		method = authMethodSSHKey
	}
	label := firstNonEmpty(configString(config, "server_remote_credential_ref"), configString(config, "server_remote_ssh_key_label"))
	if method == authMethodPassword && label == "" {
		label = serviceRemoteSSH
		if stack := strings.TrimSpace(stackID); stack != "" {
			label += ":" + stack
		}
	}
	return StackCredentialRef{
		TenantID:   strings.TrimSpace(tenantID),
		StackID:    strings.TrimSpace(stackID),
		OwnerID:    strings.TrimSpace(ownerID),
		AuthMethod: method,
		Label:      label,
		Host:       configString(config, "server_remote_host"),
		User:       configString(config, "server_remote_user"),
		Port:       configPort(config),
		ItemID:     configString(config, StackConfigKeyCredentialItemID),
	}, true
}

// StackConfigKeyCredentialItemID is the stack configuration key that holds the
// kombify Wallet node access item a connect-remote stack logs in with.
const StackConfigKeyCredentialItemID = "server_remote_credential_item_id" // #nosec G101 -- configuration key name, not a credential.

// configPort reads server_remote_port the way the remote-connection path does.
func configPort(config map[string]any) int {
	switch typed := config["server_remote_port"].(type) {
	case int:
		return max(typed, 0)
	case int64:
		return max(int(typed), 0)
	case float64:
		return max(int(typed), 0)
	case string:
		if parsed, err := strconv.Atoi(strings.TrimSpace(typed)); err == nil {
			return max(parsed, 0)
		}
	}
	return 0
}

// configString reads a string from the stack config, its "metadata" or its
// "options" map, in that order.
func configString(config map[string]any, key string) string {
	if value := metadataString(config, key); value != "" {
		return value
	}
	for _, parent := range []string{"metadata", "options"} {
		if nested, ok := config[parent].(map[string]any); ok {
			if value := metadataString(nested, key); value != "" {
				return value
			}
		}
	}
	return ""
}

// LabelOutcome is the result of resolving one stack reference.
type LabelOutcome string

const (
	// LabelResolved: exactly one eligible row matches; the switch writes its id.
	LabelResolved LabelOutcome = "resolved"
	// LabelAmbiguous: several rows match. Today the newest wins silently; the
	// switch pins that row and the owner reviews the others.
	LabelAmbiguous LabelOutcome = "ambiguous"
	// LabelUnresolved: no row matches, so the stack's remote access is broken
	// today and stays so.
	LabelUnresolved LabelOutcome = "unresolved"
	// LabelTargetQuarantined: the only match is quarantined.
	LabelTargetQuarantined LabelOutcome = "target_quarantined"
)

// LabelResolution maps one stack's name-label reference to Techstack item ids.
type LabelResolution struct {
	TenantID string       `json:"tenant_id"`
	StackID  string       `json:"stack_id"`
	Kind     string       `json:"kind"`
	Outcome  LabelOutcome `json:"outcome"`
	// ItemIDs are the matches, newest first: the first is the row the caller
	// resolves today.
	ItemIDs []string `json:"item_ids,omitempty"`
}

// LabelCollision is a group of rows one owner can reach by the same label.
type LabelCollision struct {
	TenantID string   `json:"tenant_id"`
	Kind     string   `json:"kind"`
	ItemIDs  []string `json:"item_ids"`
}

// ResolveStackLabels resolves each stack reference the way the label callers
// do: same owner, same kind, case-insensitive name. Only rows with a resolved
// owner can match, as today.
func ResolveStackLabels(refs []StackCredentialRef, plans []ItemPlan) []LabelResolution {
	out := make([]LabelResolution, 0, len(refs))
	for _, ref := range refs {
		kind := kindSSHKey
		if ref.AuthMethod == authMethodPassword {
			kind = kindSSHPassword
		}
		matches := labelMatches(ref, kind, plans)
		resolution := LabelResolution{TenantID: ref.TenantID, StackID: ref.StackID, Kind: kind, Outcome: labelOutcome(matches)}
		for _, match := range matches {
			resolution.ItemIDs = append(resolution.ItemIDs, match.Source.ItemID)
		}
		out = append(out, resolution)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		return out[i].StackID < out[j].StackID
	})
	return out
}

// labelMatches returns the rows a label caller could pick, newest first.
func labelMatches(ref StackCredentialRef, kind string, plans []ItemPlan) []ItemPlan {
	label := foldLabel(ref.Label)
	if label == "" || ref.OwnerID == "" {
		return nil
	}
	var matches []ItemPlan
	for _, plan := range plans {
		if plan.Source.TenantID == ref.TenantID && plan.Owner.Outcome == OwnerResolved &&
			plan.Owner.OwnerID == ref.OwnerID && plan.Source.Kind == kind && plan.Source.NameLabel == label {
			matches = append(matches, plan)
		}
	}
	sortNewestFirst(matches)
	return matches
}

func labelOutcome(matches []ItemPlan) LabelOutcome {
	switch {
	case len(matches) > 1:
		return LabelAmbiguous
	case len(matches) == 1 && matches[0].Eligible():
		return LabelResolved
	case len(matches) == 1:
		return LabelTargetQuarantined
	default:
		return LabelUnresolved
	}
}

// LabelCollisions finds rows of a label-resolved kind that share tenant,
// owner, kind and folded name. The wizard test resolves request labels too,
// so collisions matter even without a stack reference.
func LabelCollisions(plans []ItemPlan) []LabelCollision {
	type key struct{ tenant, owner, kind, label string }
	groups := map[key][]ItemPlan{}
	for _, plan := range plans {
		if !labelResolvedKinds[plan.Source.Kind] || plan.Owner.Outcome != OwnerResolved || plan.Source.NameLabel == "" {
			continue
		}
		k := key{plan.Source.TenantID, plan.Owner.OwnerID, plan.Source.Kind, plan.Source.NameLabel}
		groups[k] = append(groups[k], plan)
	}
	var out []LabelCollision
	for k, members := range groups {
		if len(members) < 2 {
			continue
		}
		sortNewestFirst(members)
		collision := LabelCollision{TenantID: k.tenant, Kind: k.kind}
		for _, member := range members {
			collision.ItemIDs = append(collision.ItemIDs, member.Source.ItemID)
		}
		out = append(out, collision)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].TenantID != out[j].TenantID {
			return out[i].TenantID < out[j].TenantID
		}
		if out[i].Kind != out[j].Kind {
			return out[i].Kind < out[j].Kind
		}
		return out[i].ItemIDs[0] < out[j].ItemIDs[0]
	})
	return out
}

// sortNewestFirst matches the store's list order (created_at descending).
func sortNewestFirst(plans []ItemPlan) {
	sort.SliceStable(plans, func(i, j int) bool {
		if !plans[i].Source.CreatedAt.Equal(plans[j].Source.CreatedAt) {
			return plans[i].Source.CreatedAt.After(plans[j].Source.CreatedAt)
		}
		return plans[i].Source.ItemID < plans[j].Source.ItemID
	})
}
