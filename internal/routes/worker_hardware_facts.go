package routes

import (
	"encoding/json"

	"github.com/kombifyio/techstack/pkg/agent/httpguard"
)

// workerHardwareKey is the member of the stored host map that holds the
// Guard's machine-level hardware facts (CPU level, GPU, media, disk and power
// class). The writer below and hostHardwareFromMap are its only two users.
const workerHardwareKey = "hardware"

// inventoryHardwareMap is the stored form of a Guard's hardware report: what
// survives the sanitizer, as a plain map so it round-trips through the
// worker's JSON columns unchanged. It returns nil when the Guard sent nothing
// usable, and the host map then carries no hardware member at all.
func inventoryHardwareMap(reported *httpguard.HostHardware) map[string]any {
	clean := reported.Sanitized()
	if clean == nil {
		return nil
	}
	encoded, err := json.Marshal(clean)
	if err != nil {
		return nil
	}
	var out map[string]any
	if json.Unmarshal(encoded, &out) != nil {
		return nil
	}
	return out
}

// hostHardwareFromMap reads the stored hardware member back, sanitizing again:
// a row written by an older or hand-edited path never reaches the planner with
// a class outside the closed vocabularies.
func hostHardwareFromMap(host map[string]any) *httpguard.HostHardware {
	raw, ok := host[workerHardwareKey]
	if !ok || raw == nil {
		return nil
	}
	encoded, err := json.Marshal(raw)
	if err != nil {
		return nil
	}
	var hardware httpguard.HostHardware
	if json.Unmarshal(encoded, &hardware) != nil {
		return nil
	}
	return hardware.Sanitized()
}
