package httpguard

import (
	"regexp"
	"strings"
	"time"
)

// HostHardware is what a Guard's sysfs and procfs probe established about the
// machine itself: facts the Layout Planner needs and the usage counters cannot
// give. Every member is optional and an absent member means "not observed",
// never "none": a fact the probe could not read is left out rather than guessed.
//
// It travels inside the inventory's host section. Older control planes ignore
// it, and a Guard without the probe simply does not send it.
type HostHardware struct {
	// ObservedAt is when the probe last ran; the Guard repeats the probe only
	// every few minutes, so this is not the inventory's own timestamp.
	ObservedAt time.Time `json:"observed_at"`
	// AMD64Level is the x86-64 microarchitecture level (1 to 4) read from the
	// CPU flags; absent on other architectures and when the flags are unreadable.
	AMD64Level int `json:"amd64_level,omitempty"`
	// GPU is the most capable GPU the bus enumerates (discrete before
	// integrated). Only vendors that can run accelerated workloads are named.
	GPU *HostGPU `json:"gpu,omitempty"`
	// Media is the hardware media capability, for example an integrated GPU
	// with a render node and a media-capable driver.
	Media *HostHardwareFact `json:"media,omitempty"`
	// DiskClass is the class of the disk that holds the root filesystem.
	DiskClass *HostHardwareFact `json:"disk_class,omitempty"`
	// PowerClass is an estimate. Its source says so.
	PowerClass *HostHardwareFact `json:"power_class,omitempty"`
}

// HostGPU is one GPU with the vendor from its PCI vendor id and the kernel
// driver bound to it.
type HostGPU struct {
	// Vendor is "nvidia", "amd" or "intel".
	Vendor string `json:"vendor"`
	// Driver is the kernel driver name bound to the GPU, empty when none is.
	Driver string `json:"driver,omitempty"`
	// DriverMajor is the major version of a driver that publishes one
	// (/sys/module/nvidia/version); absent for drivers that do not, such as
	// i915, xe and amdgpu, whose version is the kernel's.
	DriverMajor int `json:"driver_major,omitempty"`
}

// HostHardwareFact is a classified hardware fact with the provenance it came
// from, for example "guard-probe:sysfs" or "estimate:dmi-chassis".
type HostHardwareFact struct {
	Class  string `json:"class"`
	Source string `json:"source"`
}

var (
	hostHardwareToken  = regexp.MustCompile(`^[a-z0-9][a-z0-9_.-]{0,31}$`)
	hostHardwareSource = regexp.MustCompile(`^[a-z0-9][a-z0-9:._-]{0,63}$`)

	hostHardwareVendors = map[string]bool{"nvidia": true, "amd": true, "intel": true}
	hostMediaClasses    = map[string]bool{"hardware-transcode": true}
	hostDiskClasses     = map[string]bool{"nvme": true, "ssd": true, "hdd": true}
	hostPowerClasses    = map[string]bool{"low": true, "medium": true, "high": true}
)

// Sanitized keeps what a control plane may store from a Guard report: closed
// vocabularies for every class, bounded tokens, and nothing else. It returns
// nil when nothing survives, so an empty report stores no member at all.
func (h *HostHardware) Sanitized() *HostHardware {
	if h == nil {
		return nil
	}
	clean := HostHardware{ObservedAt: h.ObservedAt.UTC()}
	if h.AMD64Level >= 1 && h.AMD64Level <= 4 {
		clean.AMD64Level = h.AMD64Level
	}
	if gpu := h.GPU; gpu != nil && hostHardwareVendors[gpu.Vendor] {
		kept := HostGPU{Vendor: gpu.Vendor}
		if hostHardwareToken.MatchString(gpu.Driver) {
			kept.Driver = gpu.Driver
		}
		if gpu.DriverMajor > 0 && gpu.DriverMajor < 10000 {
			kept.DriverMajor = gpu.DriverMajor
		}
		clean.GPU = &kept
	}
	clean.Media = sanitizedHardwareFact(h.Media, hostMediaClasses)
	clean.DiskClass = sanitizedHardwareFact(h.DiskClass, hostDiskClasses)
	clean.PowerClass = sanitizedHardwareFact(h.PowerClass, hostPowerClasses)
	if clean.AMD64Level == 0 && clean.GPU == nil && clean.Media == nil && clean.DiskClass == nil && clean.PowerClass == nil {
		return nil
	}
	return &clean
}

func sanitizedHardwareFact(fact *HostHardwareFact, classes map[string]bool) *HostHardwareFact {
	if fact == nil || !classes[strings.TrimSpace(fact.Class)] || !hostHardwareSource.MatchString(fact.Source) {
		return nil
	}
	return &HostHardwareFact{Class: strings.TrimSpace(fact.Class), Source: fact.Source}
}
