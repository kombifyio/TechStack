package featureflags

// FlagshipTokenBudget is the typed form of an "ai.tokens"-style JSON flag
// variation delivered by Cloudflare Flagship (ENTITLEMENTS-ARCHITECTURE §6).
// The operator maintains the variation per tier in the control plane; values are
// plan-targeted. Self-host sets Unlimited=true (no gating).
type FlagshipTokenBudget struct {
	Monthly   int64 `json:"monthly"`
	RPM       int64 `json:"rpm"`
	Burst     int64 `json:"burst"`
	Unlimited bool  `json:"unlimited,omitempty"`
}

// ParseFlagshipTokenBudget parses a JSON-variation object (as decoded into a
// map[string]any) into a typed budget. Reuses the tolerant readInt/readBool
// helpers so string/float/int encodings are all accepted. Missing keys default
// to zero; an explicit "unlimited":true wins.
func ParseFlagshipTokenBudget(raw map[string]any) FlagshipTokenBudget {
	b := FlagshipTokenBudget{}
	if raw == nil {
		return b
	}
	if v, ok := readInt(raw, "monthly", "monthly_tokens"); ok {
		b.Monthly = int64(v)
	}
	if v, ok := readInt(raw, "rpm", "requests_per_minute"); ok {
		b.RPM = int64(v)
	}
	if v, ok := readInt(raw, "burst"); ok {
		b.Burst = int64(v)
	}
	if v, ok := readBool(raw, "unlimited"); ok {
		b.Unlimited = v
	}
	return b
}
