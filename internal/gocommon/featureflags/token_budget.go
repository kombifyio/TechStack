package featureflags

import (
	"encoding/json"
	"strconv"
)

// TokenBudgetPolicy describes the role-based token allowance configured via
// feature flag metadata in the admin backend.
// Tokens are the universal metering unit. Different engines may burn tokens
// at different rates (e.g. Docker=1/min, DigitalOcean=10/min).
type TokenBudgetPolicy struct {
	Enabled   bool   `json:"enabled"`
	Role      string `json:"role,omitempty"`
	Source    string `json:"source,omitempty"` // "platform", "local", or "default"
	Tokens    int    `json:"tokens,omitempty"`
	Unlimited bool   `json:"unlimited,omitempty"`
}

// DefaultPlatformRolePriority defines the standard ordering used when
// resolving which platform role's budget wins. Higher-priority roles
// appear first.
var DefaultPlatformRolePriority = []string{
	"global_admin", "developer", "manager", "user", "viewer",
}

// ParseTokenBudgetPolicy resolves a token budget from a feature flag's metadata.
// It checks platform roles (in priority order), then localRole, then "default".
// Returns a zero-value TokenBudgetPolicy if the flag is nil, disabled, or has
// no metadata.
func ParseTokenBudgetPolicy(flag *AdminFeatureFlag, platformRoles []string, localRole string) TokenBudgetPolicy {
	if flag == nil || !flag.Enabled || flag.Metadata == nil {
		return TokenBudgetPolicy{}
	}

	roleBudgets, ok := readMap(flag.Metadata, "role_budgets", "roleBudgets")
	if !ok || len(roleBudgets) == 0 {
		return TokenBudgetPolicy{Enabled: true}
	}

	for _, role := range OrderedPlatformRoles(platformRoles) {
		if policy, found := parseRoleBudget(roleBudgets, role); found {
			policy.Enabled = true
			policy.Role = role
			policy.Source = "platform"
			return policy
		}
	}

	if localRole != "" {
		if policy, found := parseRoleBudget(roleBudgets, localRole); found {
			policy.Enabled = true
			policy.Role = localRole
			policy.Source = "local"
			return policy
		}
	}

	if policy, found := parseRoleBudget(roleBudgets, "default"); found {
		policy.Enabled = true
		policy.Role = "default"
		policy.Source = "default"
		return policy
	}

	return TokenBudgetPolicy{Enabled: true}
}

// OrderedPlatformRoles returns roles sorted by DefaultPlatformRolePriority,
// with any unlisted roles appended at the end. Duplicates are removed.
func OrderedPlatformRoles(roles []string) []string {
	seen := make(map[string]struct{}, len(roles))
	ordered := make([]string, 0, len(roles))

	for _, candidate := range DefaultPlatformRolePriority {
		for _, role := range roles {
			if role == candidate {
				if _, ok := seen[role]; !ok {
					seen[role] = struct{}{}
					ordered = append(ordered, role)
				}
			}
		}
	}

	for _, role := range roles {
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		ordered = append(ordered, role)
	}
	return ordered
}

func readMap(source map[string]any, keys ...string) (map[string]any, bool) {
	for _, key := range keys {
		value, ok := source[key]
		if !ok {
			continue
		}
		if typed, ok := value.(map[string]any); ok {
			return typed, true
		}
	}
	return nil, false
}

func parseRoleBudget(roleBudgets map[string]any, role string) (TokenBudgetPolicy, bool) {
	value, ok := roleBudgets[role]
	if !ok {
		return TokenBudgetPolicy{}, false
	}
	switch typed := value.(type) {
	case float64:
		return TokenBudgetPolicy{Tokens: int(typed)}, true
	case int:
		return TokenBudgetPolicy{Tokens: typed}, true
	case int64:
		return TokenBudgetPolicy{Tokens: int(typed)}, true
	case json.Number:
		i, err := typed.Int64()
		if err == nil {
			return TokenBudgetPolicy{Tokens: int(i)}, true
		}
	case string:
		if tokens, err := strconv.Atoi(typed); err == nil {
			return TokenBudgetPolicy{Tokens: tokens}, true
		}
	case map[string]any:
		return parseRoleBudgetObject(typed), true
	}
	return TokenBudgetPolicy{}, false
}

func parseRoleBudgetObject(data map[string]any) TokenBudgetPolicy {
	policy := TokenBudgetPolicy{}
	if unlimited, ok := readBool(data, "unlimited"); ok {
		policy.Unlimited = unlimited
	}
	if tokens, ok := readInt(data, "tokens", "minutes", "budget_minutes", "token_limit", "tokenLimit"); ok {
		policy.Tokens = tokens
	}
	return policy
}

func readBool(source map[string]any, keys ...string) (bool, bool) {
	for _, key := range keys {
		value, ok := source[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case bool:
			return typed, true
		case string:
			if typed == "true" {
				return true, true
			}
			if typed == "false" {
				return false, true
			}
		}
	}
	return false, false
}

func readInt(source map[string]any, keys ...string) (int, bool) {
	for _, key := range keys {
		value, ok := source[key]
		if !ok {
			continue
		}
		switch typed := value.(type) {
		case int:
			return typed, true
		case int64:
			return int(typed), true
		case float64:
			return int(typed), true
		case json.Number:
			i, err := typed.Int64()
			if err == nil {
				return int(i), true
			}
		case string:
			i, err := strconv.Atoi(typed)
			if err == nil {
				return i, true
			}
		}
	}
	return 0, false
}
