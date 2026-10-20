package layout

import "strings"

// Household priority bands. Importance is household policy, owned by
// Techstack; StackKits states only technical facts (boundary §15.4).
const (
	BandImportant = "important"
	BandStandard  = "standard"
	BandTolerant  = "tolerant"
)

// DefaultPolicyProfile is the versioned default household policy.
const DefaultPolicyProfile = "techstack.kombify.io/layout-policy/household-default@v1"

// defaultBands is the default importance of each use case: what a household
// misses first when it is down. Passwords, the smart home and the home
// network are important; AI and game servers tolerate being offline.
var defaultBands = map[string]string{
	"vault":       BandImportant,
	"smart-home":  BandImportant,
	"network":     BandImportant,
	"mail-server": BandImportant,
	"ai":          BandTolerant,
	"game":        BandTolerant,
}

// DefaultBand returns the band of a use case under the default policy.
func DefaultBand(goal string) string {
	if band, ok := defaultBands[strings.ToLower(strings.TrimSpace(goal))]; ok {
		return band
	}
	return BandStandard
}

func bandRank(band string) int {
	switch band {
	case BandImportant:
		return 0
	case BandStandard:
		return 1
	default:
		return 2
	}
}

func availabilityRank(class string) int {
	switch class {
	case AvailabilityAlwaysOn:
		return 0
	case AvailabilityPartTime:
		return 2
	default:
		return 1
	}
}
