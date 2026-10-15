package substrateprovision

import (
	"errors"
	"fmt"
	"strings"

	"github.com/kombifyio/techstack/internal/substrate"
)

var ErrInsufficientResources = errors.New("insufficient hypervisor resources")

type Placement struct {
	ServerID  string `json:"server_id"`
	ProfileID string `json:"profile_id"`
	Storage   string `json:"storage"`
	Bridge    string `json:"bridge"`
	CPU       int    `json:"cpu"`
	MemoryMiB int    `json:"memory_mib"`
	DiskGiB   int    `json:"disk_gib"`
	Isolated  bool   `json:"isolated,omitempty"`
}

// ValidatePlacement checks guest resources against the profile and available
// substrate capacity. Inventory must already account for retained reservations.
// This decision grants no ownership, provider admission or resource reservation.
func ValidatePlacement(p Placement, inventory substrate.Inventory) error {
	var profile *substrate.Profile
	for _, candidate := range substrate.Profiles() {
		if candidate.ID == p.ProfileID {
			copy := candidate
			profile = &copy
			break
		}
	}
	if profile == nil || p.CPU < profile.MinCPU || p.CPU > 256 || p.MemoryMiB < profile.MinMemoryMiB || p.MemoryMiB > 1048576 || p.DiskGiB < profile.MinDiskGiB || p.DiskGiB > 65536 {
		return errors.New("guest resources outside profile limits")
	}
	if inventory.Architecture != profile.Architecture || !inventory.HardwareVirtualization {
		return substrate.ErrCapability
	}
	if p.CPU > inventory.CPU || uint64(p.MemoryMiB)*1024*1024 > inventory.MemoryAvailable {
		return ErrInsufficientResources
	}
	storageOK, bridgeOK := false, false
	for _, s := range inventory.Storage {
		if s.ID == p.Storage && s.Active == 1 && s.Available >= uint64(p.DiskGiB)*1024*1024*1024 {
			content := "," + s.Content + ","
			storageOK = strings.Contains(content, ",images,") && strings.Contains(content, ",import,") && (!profile.CloudInit || strings.Contains(content, ",snippets,")) && (!profile.Appliance || strings.Contains(content, ",vztmpl,"))
		}
	}
	for _, n := range inventory.Networks {
		if n.Name == p.Bridge && n.Type == "bridge" && n.Active == 1 {
			bridgeOK = true
		}
	}
	if !storageOK || !bridgeOK {
		return fmt.Errorf("active LAN bridge and suitable image/import storage required")
	}
	return nil
}
