package devicereadiness

import (
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/agent/httpguard"
)

// Provenance strings that travel with a derived hardware fact. A reader can
// tell a reading ("guard-probe:sysfs") from an estimate ("estimate:dmi-chassis")
// without knowing how either was made.
const (
	HardwareSourceProbe           = "guard-probe:sysfs"
	HardwareSourceChassisEstimate = "estimate:dmi-chassis"
)

// HardwareFacts is what one probe run read about the machine itself: raw
// observations from procfs and sysfs. They are classified by Observation, in
// Go, so the rules are tested in one place and the shell stays a reader.
// A member that is zero-valued here was not observed.
type HardwareFacts struct {
	Observed bool `json:"observed"`
	CPU      struct {
		Arch  string `json:"arch,omitempty"`
		Cores int    `json:"cores,omitempty"`
		// Flags are the /proc/cpuinfo flags of the first CPU, x86 only.
		Flags []string `json:"flags,omitempty"`
	} `json:"cpu"`
	// GPUs are the PCI display controllers, by their PCI vendor id.
	GPUs []HardwareGPU `json:"gpus,omitempty"`
	// NvidiaDriverVersion is /sys/module/nvidia/version, empty without the
	// proprietary module.
	NvidiaDriverVersion string       `json:"nvidiaDriverVersion,omitempty"`
	RenderNodes         []RenderNode `json:"renderNodes,omitempty"`
	// SystemDisk is the disk under the root filesystem.
	SystemDisk *SystemDisk `json:"systemDisk,omitempty"`
	// ChassisType is the SMBIOS chassis type from /sys/class/dmi/id.
	ChassisType *int `json:"chassisType,omitempty"`
}

// HardwareGPU is one display controller found on the PCI bus.
type HardwareGPU struct {
	Slot     string `json:"slot"`
	VendorID string `json:"vendorId,omitempty"`
	// Driver is the kernel driver bound to the function, empty when none is.
	Driver string `json:"driver,omitempty"`
}

// RenderNode is a DRM render node and the driver behind it.
type RenderNode struct {
	Name     string `json:"name"`
	VendorID string `json:"vendorId,omitempty"`
	Driver   string `json:"driver,omitempty"`
}

// SystemDisk is the whole disk (not a partition) the root filesystem lives on.
type SystemDisk struct {
	Device     string `json:"device"`
	Rotational bool   `json:"rotational"`
}

// x86-64 microarchitecture levels as the ABI document defines them, expressed
// as the /proc/cpuinfo flag names. "abm" is how the kernel names LZCNT.
var (
	amd64V2Flags = []string{"cx16", "lahf_lm", "popcnt", "sse4_1", "sse4_2", "ssse3"}
	amd64V3Flags = []string{"avx", "avx2", "bmi1", "bmi2", "f16c", "fma", "movbe", "xsave"}
	amd64V4Flags = []string{"avx512f", "avx512bw", "avx512cd", "avx512dq", "avx512vl"}
)

// AMD64Level returns the x86-64 microarchitecture level (1 to 4) the CPU flags
// establish, or 0 when the machine is not x86-64 or exposed no flags. A flag
// list that does not even show long mode is not a reading of a 64-bit CPU and
// also yields 0.
func AMD64Level(arch string, flags []string) int {
	if arch != "x86_64" || len(flags) == 0 {
		return 0
	}
	have := make(map[string]bool, len(flags))
	for _, flag := range flags {
		have[strings.ToLower(flag)] = true
	}
	if !have["lm"] {
		return 0
	}
	all := func(required []string) bool {
		for _, flag := range required {
			if !have[flag] {
				return false
			}
		}
		return true
	}
	level := 1
	if all(amd64V2Flags) {
		level = 2
		if all(amd64V3Flags) && (have["abm"] || have["lzcnt"]) {
			level = 3
			if all(amd64V4Flags) {
				level = 4
			}
		}
	}
	return level
}

// gpuVendors maps PCI vendor ids to the accelerator vendor names the catalog
// uses. Other display controllers (a server's BMC graphics, a virtual GPU) are
// not accelerators and are not reported as one.
var gpuVendors = map[string]string{"10de": "nvidia", "1002": "amd", "8086": "intel"}

// gpuVendorRank orders vendors by how likely their GPU is the one a workload
// wants: a discrete card before an integrated one.
var gpuVendorRank = map[string]int{"nvidia": 0, "amd": 1, "intel": 2}

// mediaDrivers are the kernel drivers that expose hardware video encode and
// decode on a render node.
var mediaDrivers = map[string]bool{"i915": true, "xe": true, "amdgpu": true, "nvidia": true}

// Observation condenses the raw facts into what the control plane stores with
// the worker. It returns nil when nothing was observed.
func (h HardwareFacts) Observation(observedAt time.Time) *httpguard.HostHardware {
	if !h.Observed {
		return nil
	}
	observation := httpguard.HostHardware{
		ObservedAt: observedAt.UTC(),
		AMD64Level: AMD64Level(h.CPU.Arch, h.CPU.Flags),
		GPU:        h.primaryGPU(),
	}
	if h.hasMediaRenderNode() {
		observation.Media = &httpguard.HostHardwareFact{Class: "hardware-transcode", Source: HardwareSourceProbe}
	}
	if class := h.diskClass(); class != "" {
		observation.DiskClass = &httpguard.HostHardwareFact{Class: class, Source: HardwareSourceProbe}
	}
	if class := h.powerClass(); class != "" {
		observation.PowerClass = &httpguard.HostHardwareFact{Class: class, Source: HardwareSourceChassisEstimate}
	}
	return observation.Sanitized()
}

// primaryGPU picks the most capable named GPU: nvidia, then amd, then intel,
// the first by PCI slot within a vendor. The driver major is only reported for
// the proprietary nvidia module, the one driver that publishes a version of its
// own; i915, xe and amdgpu share the kernel's.
func (h HardwareFacts) primaryGPU() *httpguard.HostGPU {
	candidates := make([]HardwareGPU, 0, len(h.GPUs))
	for _, gpu := range h.GPUs {
		if _, named := gpuVendors[strings.ToLower(gpu.VendorID)]; named {
			candidates = append(candidates, gpu)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.SliceStable(candidates, func(i, j int) bool {
		left := gpuVendorRank[gpuVendors[strings.ToLower(candidates[i].VendorID)]]
		right := gpuVendorRank[gpuVendors[strings.ToLower(candidates[j].VendorID)]]
		if left != right {
			return left < right
		}
		return candidates[i].Slot < candidates[j].Slot
	})
	chosen := candidates[0]
	gpu := httpguard.HostGPU{Vendor: gpuVendors[strings.ToLower(chosen.VendorID)], Driver: strings.ToLower(chosen.Driver)}
	if gpu.Driver == "nvidia" {
		gpu.DriverMajor = leadingNumber(h.NvidiaDriverVersion)
	}
	return &gpu
}

func (h HardwareFacts) hasMediaRenderNode() bool {
	for _, node := range h.RenderNodes {
		if _, named := gpuVendors[strings.ToLower(node.VendorID)]; named && mediaDrivers[strings.ToLower(node.Driver)] {
			return true
		}
	}
	return false
}

// virtualDiskPrefixes are block devices whose rotational flag says nothing
// about the storage behind them: virtual disks report whatever the hypervisor
// does, and flash cards and RAM disks are neither "ssd" nor "hdd".
var virtualDiskPrefixes = []string{"vd", "xvd", "loop", "zram", "nbd", "ram", "mmcblk"}

func (h HardwareFacts) diskClass() string {
	if h.SystemDisk == nil || h.SystemDisk.Device == "" {
		return ""
	}
	device := strings.ToLower(h.SystemDisk.Device)
	if strings.HasPrefix(device, "nvme") {
		return "nvme"
	}
	for _, prefix := range virtualDiskPrefixes {
		if strings.HasPrefix(device, prefix) {
			return ""
		}
	}
	if h.SystemDisk.Rotational {
		return "hdd"
	}
	return "ssd"
}

// SMBIOS chassis types that decide the power estimate. Types the table does not
// list (Other, Unknown, docking stations) estimate nothing.
var (
	smallChassis   = map[int]bool{8: true, 9: true, 10: true, 11: true, 14: true, 30: true, 31: true, 32: true, 33: true, 34: true, 35: true, 36: true}
	desktopChassis = map[int]bool{3: true, 4: true, 6: true, 7: true, 13: true, 15: true, 16: true, 24: true}
	serverChassis  = map[int]bool{5: true, 17: true, 23: true, 25: true, 28: true, 29: true}
)

// powerClass is an estimate of the machine's typical draw from its chassis and
// core count: portable and embedded machines are low, desktops medium, servers
// high, and a core count above eight moves a machine one class up. It is a
// planning hint, labelled as an estimate wherever it travels, and says nothing
// when the chassis is not recorded.
func (h HardwareFacts) powerClass() string {
	if h.ChassisType == nil {
		return ""
	}
	large := h.CPU.Cores > 8
	switch chassis := *h.ChassisType; {
	case smallChassis[chassis]:
		if large {
			return "medium"
		}
		return "low"
	case desktopChassis[chassis]:
		if large {
			return "high"
		}
		return "medium"
	case serverChassis[chassis]:
		return "high"
	default:
		return ""
	}
}

// leadingNumber reads the number before the first dot of a driver version such
// as "550.54.14"; it returns 0 for anything else.
func leadingNumber(version string) int {
	version = strings.TrimSpace(version)
	if dot := strings.IndexByte(version, '.'); dot >= 0 {
		version = version[:dot]
	}
	number, err := strconv.Atoi(version)
	if err != nil || number < 0 {
		return 0
	}
	return number
}
