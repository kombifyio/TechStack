package layout

import (
	"sort"
	"strconv"
	"strings"
)

// reasonTemplates render trace facts into plain English. The code and its
// params are the contract (clients localize by code); the text is the
// deterministic English rendering of the same fact, never free prose.
var reasonTemplates = map[string]string{
	// Why a workload landed where it did.
	"runs_on_always_on":    "{title} is important, so it runs on {node}: it has been on around the clock.",
	"prefers_always_on":    "{title} runs on {node} because it has been on around the clock.",
	"avoids_part_time":     "{title} avoids {rival}, which is often switched off.",
	"important_tight_fit":  "{title} is important and small, so it shares {node} with your other important apps and leaves {rival} free for heavier ones.",
	"kept_apart":           "{title} runs on {node}, apart from your important apps, so its load cannot slow them down.",
	"tight_fit":            "{node} has room for {title} and keeps more space free on {rival}.",
	"verified_hardware":    "The hardware facts {title} depends on are confirmed for {node}.",
	"equal_choice":         "{node} and {rival} suit {title} equally well; kombify picked {node}.",
	"only_fitting_node":    "{node} is the only server that meets what {title} needs.",
	"profile_not_measured": "kombify has not measured {title} yet, so it stays on your main server {node}.",
	"pinned":               "{title} stays on {node}.",
	"with_use_case":        "{title} adds to the use case that already runs on {node}, so it runs there too.",
	"repacked":             "{title} runs on {node} so that every selected app fits.",
	"uses_accelerator":     "{title} needs a {vendor} graphics card, which {node} has.",
	"measured_reservation": "kombify measured {title}: it reserves {ram} GB of memory.",
	"estimated_profile":    "kombify has not measured {title} yet; its size is an estimate.",
	"evidence_unverified":  "{node} has not reported every hardware fact yet ({missing}).",
	// Notes about the evidence.
	"node_not_owner_controlled":           "{node} is not owner-controlled, so no app is placed on it.",
	"node_not_connected":                  "{node} is not connected, so it is left out.",
	"node_capacity_unknown":               "{node} has not reported its hardware yet, so it is left out.",
	"no_eligible_node":                    "No connected server has reported its hardware yet.",
	"infeasibility_unproven_unknown_node": "A server that has not reported its hardware yet might still fit; kombify cannot say yet.",
	// Conflicts, only for proven-infeasible results.
	"needs_more_ram":             "{title} needs {need} GB of memory; the most any server has free is {largest} GB.",
	"needs_more_cpu":             "{title} needs {need} CPU cores; the largest server has {largest}.",
	"needs_more_storage":         "{title} needs {need} GB of storage; the most any server has free is {largest} GB.",
	"missing_accelerator":        "{title} needs a {vendor} graphics card; no server has one.",
	"pinned_node_lacks_room":     "The server {title} is bound to has no room for it.",
	"no_node_meets_requirements": "No server meets what {title} needs.",
	"selection_exceeds_free_ram": "The selected apps reserve {need} GB of memory; your servers have {free} GB free.",
	"no_combination_fits":        "No way of distributing the selected apps fits all of them.",
}

// ReasonCodes lists every code the planner can emit, for client catalogs.
func ReasonCodes() []string {
	codes := make([]string, 0, len(reasonTemplates))
	for code := range reasonTemplates {
		codes = append(codes, code)
	}
	sort.Strings(codes)
	return codes
}

func reason(code string, params map[string]string) Reason {
	if params != nil && params["title"] == "" && params["workload"] != "" {
		params["title"] = params["workload"]
	}
	return Reason{Code: code, Params: params, Text: render(code, params)}
}

func render(code string, params map[string]string) string {
	text, ok := reasonTemplates[code]
	if !ok {
		return code
	}
	keys := make([]string, 0, len(params))
	for key := range params {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		text = strings.ReplaceAll(text, "{"+key+"}", params[key])
	}
	return text
}

func explainPlacement(placed placement, state *nodeState, states []*nodeState) []Reason {
	demand := placed.demand
	params := map[string]string{"workload": demand.Workload, "title": demandTitle(demand), "node": state.node.Name}
	if len(placed.rivals) > 0 {
		params["rival"] = placed.rivals[0]
	}
	var reasons []Reason
	switch {
	case placed.pinned && demand.pinReason == "with_use_case":
		reasons = append(reasons, reason("with_use_case", params))
	case placed.pinned && demand.Provenance != "measured":
		reasons = append(reasons, reason("profile_not_measured", params))
	case placed.pinned:
		reasons = append(reasons, reason("pinned", params))
	case placed.decisive == "only":
		reasons = append(reasons, reason("only_fitting_node", params))
	case placed.decisive == "repack":
		reasons = append(reasons, reason("repacked", params))
	case placed.decisive == "availability":
		switch {
		case state.node.Availability.Class == AvailabilityAlwaysOn && demand.Band == BandImportant:
			reasons = append(reasons, reason("runs_on_always_on", params))
		case state.node.Availability.Class == AvailabilityAlwaysOn:
			reasons = append(reasons, reason("prefers_always_on", params))
		default:
			reasons = append(reasons, reason("avoids_part_time", params))
		}
	case placed.decisive == "isolation":
		reasons = append(reasons, reason("kept_apart", params))
	case placed.decisive == "verified":
		reasons = append(reasons, reason("verified_hardware", params))
	case placed.decisive == "fit" && demand.Band == BandImportant:
		reasons = append(reasons, reason("important_tight_fit", params))
	case placed.decisive == "fit":
		reasons = append(reasons, reason("tight_fit", params))
	default:
		reasons = append(reasons, reason("equal_choice", params))
	}
	if demand.Accelerator != nil {
		reasons = append(reasons, reason("uses_accelerator", merge(params, map[string]string{"vendor": demand.Accelerator.Vendor})))
	}
	if demand.Provenance != "measured" && (!placed.pinned || demand.pinReason == "with_use_case") {
		reasons = append(reasons, reason("estimated_profile", params))
	}
	if demand.Provenance == "measured" && demand.Reservation.RAMGB > 0 {
		reasons = append(reasons, reason("measured_reservation", merge(params, map[string]string{"ram": formatGB(demand.Reservation.RAMGB)})))
	}
	if len(placed.unknown) > 0 {
		reasons = append(reasons, reason("evidence_unverified", merge(params, map[string]string{"missing": strings.Join(placed.unknown, ", ")})))
	}
	return reasons
}

func demandTitle(demand Demand) string {
	if strings.TrimSpace(demand.Title) != "" {
		return demand.Title
	}
	return demand.Workload
}

func formatGB(value float64) string {
	return strconv.FormatFloat(roundTo(value, 2), 'f', -1, 64)
}

func formatCores(value float64) string {
	return strconv.FormatFloat(roundTo(value, 2), 'f', -1, 64)
}

func roundTo(value float64, places int) float64 {
	scale := 1.0
	for i := 0; i < places; i++ {
		scale *= 10
	}
	if value < 0 {
		return -float64(int64(-value*scale+0.5)) / scale
	}
	return float64(int64(value*scale+0.5)) / scale
}
