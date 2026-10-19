package devicereadiness

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/kombifyio/techstack/internal/substrate"
)

// Accelerator passthrough is the hypervisor half of a use case that needs a
// GPU: the node has to hand a whole PCI device to one guest. The guest half,
// its driver and the CUDA proof, belongs to StackKits inside the guest.
//
// Four things decide whether that hand-over can work, and each is its own
// check so a fix binds to exactly the fault it answers. A Thunderbolt or USB4
// enclosure under user security is invisible on the PCI bus until it is
// authorized. The IOMMU has to be on, or nothing can be isolated. The GPU's
// IOMMU group may hold only the GPU's own functions and bridges, because
// everything in a group goes to the guest together. And the host must not keep
// a driver on the GPU, or the guest start races the host for it.

// AcceleratorFacts is what the node exposes about devices it could pass
// through. It is observed only on a hypervisor node.
type AcceleratorFacts struct {
	Observed bool `json:"observed"`
	// IOMMUEnabled is nil when the probe could not tell, never false by
	// default.
	IOMMUEnabled *bool `json:"iommuEnabled,omitempty"`
	IOMMUGroups  int   `json:"iommuGroups,omitempty"`
	// ThunderboltSecurity is the domain security level: none, user, secure,
	// dponly or usbonly. Under user and secure a newly attached device stays
	// unauthorized until someone approves it.
	ThunderboltSecurity string              `json:"thunderboltSecurity,omitempty"`
	Thunderbolt         []ThunderboltDevice `json:"thunderbolt,omitempty"`
	GPUs                []PCIGPU            `json:"gpus,omitempty"`
}

// ThunderboltDevice is one attached Thunderbolt or USB4 device, not a host
// router.
type ThunderboltDevice struct {
	ID string `json:"id"`
	// UniqueID survives reattachment and port changes, which is why a
	// persistent authorization names it rather than the port.
	UniqueID   string `json:"uniqueId"`
	Name       string `json:"name,omitempty"`
	Vendor     string `json:"vendor,omitempty"`
	Authorized bool   `json:"authorized"`
}

// PCIFunction is one PCI function as the bus reports it, driver or not.
type PCIFunction struct {
	Slot     string `json:"slot"`
	Class    string `json:"class,omitempty"`
	VendorID string `json:"vendorId,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
	// Driver is empty when nothing is bound.
	Driver string `json:"driver,omitempty"`
}

// PCIGPU is a display controller found on the bus. It is detected from the PCI
// class, so a GPU with no driver at all is still found.
type PCIGPU struct {
	PCIFunction
	Description string `json:"description,omitempty"`
	// BootVGA marks the device the node itself boots its console on. It is
	// never offered for passthrough.
	BootVGA bool `json:"bootVga"`
	// Removable marks a device behind a hot-plug link: Thunderbolt, USB4 or a
	// hot-plug slot. Such a device can appear late or without its resources,
	// which the guest start has to tolerate.
	Removable  bool   `json:"removable"`
	IOMMUGroup string `json:"iommuGroup,omitempty"`
	// GroupMembers are all functions in the GPU's IOMMU group, the GPU
	// included.
	GroupMembers []PCIFunction `json:"groupMembers,omitempty"`
	// Upstream are the bridges between the root complex and the GPU, nearest
	// to the root first. The first entry is the root port.
	Upstream []string `json:"upstream,omitempty"`
}

// SlotBase is the bus:device part of a function address, which is what a
// whole-device passthrough names.
func (f PCIFunction) SlotBase() string {
	if index := strings.LastIndexByte(f.Slot, '.'); index > 0 {
		return f.Slot[:index]
	}
	return f.Slot
}

// IsBridge reports a bridge device (PCI class 0x06): a root port, a
// Thunderbolt switch port, or the dummy host bridge AMD places in the same
// group. vfio accepts them in a group without handing them to the guest.
func (f PCIFunction) IsBridge() bool { return strings.HasPrefix(f.Class, "0x06") }

// PassthroughCandidates are the GPUs this node could give a guest. The console
// GPU is excluded, because taking it away from the node leaves nobody able to
// see what the node is doing.
func (a AcceleratorFacts) PassthroughCandidates() []PCIGPU {
	candidates := make([]PCIGPU, 0, len(a.GPUs))
	for _, gpu := range a.GPUs {
		if !gpu.BootVGA {
			candidates = append(candidates, gpu)
		}
	}
	return candidates
}

// Functions returns every function of the GPU's own device, which is what goes
// to the guest together: the GPU and, usually, its HDMI audio.
func (g PCIGPU) Functions() []PCIFunction {
	base := g.SlotBase()
	functions := []PCIFunction{g.PCIFunction}
	for _, member := range g.GroupMembers {
		if member.Slot != g.Slot && member.SlotBase() == base {
			functions = append(functions, member)
		}
	}
	sort.SliceStable(functions, func(i, j int) bool { return functions[i].Slot < functions[j].Slot })
	return functions
}

// ForeignGroupMembers are devices that share the GPU's IOMMU group without
// belonging to it. Each one would go to the guest with it.
func (g PCIGPU) ForeignGroupMembers() []PCIFunction {
	base := g.SlotBase()
	foreign := make([]PCIFunction, 0)
	for _, member := range g.GroupMembers {
		if member.SlotBase() != base && !member.IsBridge() {
			foreign = append(foreign, member)
		}
	}
	return foreign
}

// Check ids for accelerator passthrough.
const (
	CheckThunderbolt     = "accelerator-thunderbolt"
	CheckIOMMU           = "accelerator-iommu"
	CheckIOMMUGroup      = "accelerator-iommu-group"
	CheckAcceleratorBind = "accelerator-vfio-binding"
)

const vfioDriver = "vfio-pci"

// acceleratorChecks answers the four passthrough questions. They apply only to
// a hypervisor node; an ordinary host gets its GPU through its own driver,
// which is the StackKits side.
func acceleratorChecks(facts Facts) []Check {
	ids := []string{CheckThunderbolt, CheckIOMMU, CheckIOMMUGroup, CheckAcceleratorBind}
	all := func(status Status, summary string) []Check {
		checks := make([]Check, 0, len(ids))
		for _, id := range ids {
			checks = append(checks, Check{ID: id, Status: status, Summary: summary})
		}
		return checks
	}
	switch {
	case !facts.Hypervisor.Observed:
		return all(StatusUnknown, "Could not determine whether this device can pass a GPU to a guest")
	case facts.Hypervisor.Kind == "":
		return all(StatusSkipped, "GPU passthrough applies only to a hypervisor node")
	case !facts.Accelerators.Observed:
		return all(StatusUnknown, "The node's PCI devices could not be observed")
	}

	accel := facts.Accelerators
	candidates := accel.PassthroughCandidates()
	checks := []Check{checkThunderbolt(accel), checkIOMMU(accel, candidates)}
	if len(candidates) == 0 {
		summary := "No GPU on this node is available for passthrough"
		detail := ""
		if len(unauthorizedThunderbolt(accel)) > 0 {
			detail = "A Thunderbolt device is attached but not authorized; a GPU behind it stays invisible until it is."
		}
		checks = append(checks,
			Check{ID: CheckIOMMUGroup, Status: StatusSkipped, Summary: summary, Detail: detail},
			Check{ID: CheckAcceleratorBind, Status: StatusSkipped, Summary: summary, Detail: detail})
		return checks
	}
	return append(checks, checkIOMMUGroup(candidates), checkAcceleratorBinding(candidates))
}

func unauthorizedThunderbolt(accel AcceleratorFacts) []ThunderboltDevice {
	pending := make([]ThunderboltDevice, 0)
	for _, device := range accel.Thunderbolt {
		if !device.Authorized {
			pending = append(pending, device)
		}
	}
	return pending
}

func checkThunderbolt(accel AcceleratorFacts) Check {
	check := Check{ID: CheckThunderbolt}
	pending := unauthorizedThunderbolt(accel)
	switch {
	case len(pending) > 0:
		check.Status = StatusBlocked
		check.Summary = fmt.Sprintf("%d Thunderbolt device(s) are attached but not authorized", len(pending))
		parts := make([]string, 0, len(pending))
		for _, device := range pending {
			label := strings.TrimSpace(device.Vendor + " " + device.Name)
			if label == "" {
				label = device.UniqueID
			}
			parts = append(parts, device.ID+" "+label)
		}
		check.Detail = strings.Join(parts, "; ") + " (security level " + accel.ThunderboltSecurity + ")"
	case len(accel.Thunderbolt) == 0:
		check.Status = StatusSkipped
		check.Summary = "No Thunderbolt device is attached"
	default:
		check.Status = StatusPass
		check.Summary = "Every attached Thunderbolt device is authorized"
	}
	return check
}

func checkIOMMU(accel AcceleratorFacts, candidates []PCIGPU) Check {
	check := Check{ID: CheckIOMMU}
	switch {
	case accel.IOMMUEnabled == nil:
		check.Status = StatusUnknown
		check.Summary = "Whether the IOMMU is enabled could not be observed"
	case *accel.IOMMUEnabled:
		check.Status = StatusPass
		check.Summary = fmt.Sprintf("The IOMMU is enabled with %d groups", accel.IOMMUGroups)
	case len(candidates) == 0:
		// Without a GPU to hand over, a disabled IOMMU costs nothing yet.
		check.Status = StatusWarning
		check.Summary = "The IOMMU is disabled, so no device can be passed to a guest"
	default:
		check.Status = StatusBlocked
		check.Summary = "The IOMMU is disabled, so the GPU cannot be passed to a guest"
	}
	return check
}

func checkIOMMUGroup(candidates []PCIGPU) Check {
	check := Check{ID: CheckIOMMUGroup}
	isolated := make([]string, 0, len(candidates))
	shared := make([]string, 0)
	unknown := 0
	for _, gpu := range candidates {
		if gpu.IOMMUGroup == "" {
			unknown++
			continue
		}
		if foreign := gpu.ForeignGroupMembers(); len(foreign) > 0 {
			slots := make([]string, 0, len(foreign))
			for _, member := range foreign {
				slots = append(slots, member.Slot)
			}
			shared = append(shared, gpu.Slot+" shares group "+gpu.IOMMUGroup+" with "+strings.Join(slots, ", "))
			continue
		}
		isolated = append(isolated, gpu.SlotBase())
	}
	switch {
	case len(isolated) > 0:
		check.Status = StatusPass
		check.Summary = "GPU " + strings.Join(isolated, ", ") + " sits in its own IOMMU group"
		check.Detail = strings.Join(shared, "; ")
	case len(shared) > 0:
		check.Status = StatusBlocked
		check.Summary = "No GPU sits in an IOMMU group of its own"
		check.Detail = strings.Join(shared, "; ")
	default:
		check.Status = StatusUnknown
		check.Summary = fmt.Sprintf("The IOMMU group of %d GPU(s) could not be observed", unknown)
	}
	return check
}

func checkAcceleratorBinding(candidates []PCIGPU) Check {
	check := Check{ID: CheckAcceleratorBind}
	held := make([]string, 0)
	for _, gpu := range candidates {
		for _, function := range gpu.Functions() {
			if function.Driver != vfioDriver {
				driver := function.Driver
				if driver == "" {
					driver = "no driver"
				}
				held = append(held, function.Slot+" ("+driver+")")
			}
		}
	}
	if len(held) == 0 {
		check.Status = StatusPass
		check.Summary = "The GPU is reserved for a guest"
		return check
	}
	// Proxmox can take an unbound function at guest start, so this does not
	// block. A host driver that grabbed it first is what makes that start
	// fail, typically the HDMI audio function under snd_hda_intel.
	check.Status = StatusWarning
	check.Summary = "The GPU is not reserved for a guest yet"
	check.Detail = "Not on " + vfioDriver + ": " + strings.Join(held, ", ")
	return check
}

// Parameter shapes accepted from a report. A value that does not match is
// refused before it reaches a command or a file on the node.
var (
	thunderboltUniqueIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	pciIDPattern               = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{4}$`)
	pciSlotPattern             = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
)

// AcceleratorParameters fills the passthrough parameters from what the probe
// measured. They come from the node's own report, never from a request body,
// so a caller cannot point a fix at a device the node did not show.
//
// The first unauthorized Thunderbolt device and the first isolated GPU are
// chosen; a node with several runs the fix again after the next probe.
func AcceleratorParameters(facts Facts) Parameters {
	params := Parameters{}
	if pending := unauthorizedThunderbolt(facts.Accelerators); len(pending) > 0 {
		params.ThunderboltUniqueID = pending[0].UniqueID
	}
	if gpu, found := facts.Accelerators.isolatedCandidate(); found {
		seen := map[string]bool{}
		for _, function := range gpu.Functions() {
			params.PCIFunctions = append(params.PCIFunctions, function.Slot)
			id := strings.ToLower(function.VendorID + ":" + function.DeviceID)
			if !seen[id] {
				seen[id] = true
				params.VFIOIDs = append(params.VFIOIDs, id)
			}
		}
	}
	return params
}

// isolatedCandidate is the first passthrough candidate whose IOMMU group holds
// only its own functions and bridges.
func (a AcceleratorFacts) isolatedCandidate() (PCIGPU, bool) {
	for _, gpu := range a.PassthroughCandidates() {
		if gpu.IOMMUGroup != "" && len(gpu.ForeignGroupMembers()) == 0 {
			return gpu, true
		}
	}
	return PCIGPU{}, false
}

// ProposedAccelerator is the GPU this node can give a guest, in the shape a
// guest placement names it, so the creation flow can fill it in instead of
// asking the operator for PCI addresses. It is nil when the node exposes no
// isolated GPU. A device behind a hot-plug link also carries its root port and
// the bridge below it, which the guest start removes and rescans when the
// enclosure came up without its memory windows, and the enclosure id when
// exactly one Thunderbolt device is attached.
func (a AcceleratorFacts) ProposedAccelerator() *substrate.PCIPassthrough {
	if !a.Observed {
		return nil
	}
	gpu, found := a.isolatedCandidate()
	if !found {
		return nil
	}
	proposal := &substrate.PCIPassthrough{Address: gpu.SlotBase()}
	if gpu.Removable && len(gpu.Upstream) >= 2 {
		proposal.RootPort, proposal.Subtree = gpu.Upstream[0], gpu.Upstream[1]
		if len(a.Thunderbolt) == 1 {
			proposal.ThunderboltUniqueID = strings.ToLower(a.Thunderbolt[0].UniqueID)
		}
	}
	return proposal
}

// renderAcceleratorPlaceholder resolves one of the passthrough placeholders or
// reports that the argument is not one of them.
func renderAcceleratorPlaceholder(argument string, params Parameters) (string, bool, error) {
	switch argument {
	case "$TB_UNIQUE_ID":
		if !thunderboltUniqueIDPattern.MatchString(params.ThunderboltUniqueID) {
			return "", true, fmt.Errorf("device readiness: this fix needs the unique id of an attached Thunderbolt device")
		}
		return strings.ToLower(params.ThunderboltUniqueID), true, nil
	case "$VFIO_IDS":
		if len(params.VFIOIDs) == 0 {
			return "", true, fmt.Errorf("device readiness: this fix needs the PCI ids of an isolated GPU")
		}
		for _, id := range params.VFIOIDs {
			if !pciIDPattern.MatchString(id) {
				return "", true, fmt.Errorf("device readiness: %q is not a PCI vendor:device id", id)
			}
		}
		return strings.Join(params.VFIOIDs, ","), true, nil
	case "$PCI_FUNCTIONS":
		if len(params.PCIFunctions) == 0 {
			return "", true, fmt.Errorf("device readiness: this fix needs the PCI functions of an isolated GPU")
		}
		for _, slot := range params.PCIFunctions {
			if !pciSlotPattern.MatchString(slot) {
				return "", true, fmt.Errorf("device readiness: %q is not a PCI function address", slot)
			}
		}
		return strings.Join(params.PCIFunctions, ","), true, nil
	}
	return "", false, nil
}

// renderContent fills the passthrough placeholders inside a file. Only the
// closed set is substituted, and each value has passed its pattern, so a file
// cannot be turned into anything the catalog did not write.
func renderContent(content string, params Parameters) (string, error) {
	for _, placeholder := range []string{"$TB_UNIQUE_ID", "$VFIO_IDS", "$PCI_FUNCTIONS"} {
		if !strings.Contains(content, placeholder) {
			continue
		}
		value, _, err := renderAcceleratorPlaceholder(placeholder, params)
		if err != nil {
			return "", err
		}
		content = strings.ReplaceAll(content, placeholder, value)
	}
	return content, nil
}

// thunderboltRule authorizes exactly one enclosure by its unique id. The node
// runs no bolt daemon, so udev is the one place a persistent approval can live.
const thunderboltRule = `ACTION=="add", SUBSYSTEM=="thunderbolt", ATTR{unique_id}=="$TB_UNIQUE_ID", ATTR{authorized}=="0", ATTR{authorized}="1" # kombify readiness
`

// vfioModprobe reserves the GPU for vfio-pci before any host driver can take
// it. The soft dependencies are what keep nouveau and the HDMI audio driver
// from winning the race at boot.
const vfioModprobe = `# Written by Kombify Techstack device readiness.
# Remove this file, run "update-initramfs -u -k all" and restart to undo.
options vfio-pci ids=$VFIO_IDS disable_vga=1
softdep nouveau pre: vfio-pci
softdep nvidia pre: vfio-pci
softdep snd_hda_intel pre: vfio-pci
blacklist nouveau
`

const vfioModulesLoad = `# Written by Kombify Techstack device readiness.
vfio
vfio_iommu_type1
vfio_pci
`

// vfioRebindPath is the helper that moves the GPU to vfio-pci now, so the
// first guest start does not have to wait for a restart.
const vfioRebindPath = "/usr/local/libexec/kombify-vfio-rebind"

// vfioRebind takes a comma-separated list of PCI function addresses. Every
// address is checked again here, because the helper outlives this fix and can
// be run by hand.
const vfioRebind = `#!/bin/sh
# Written by Kombify Techstack device readiness: move PCI functions to vfio-pci.
set -eu
modprobe vfio-pci
IFS=,
for slot in $1; do
  case "$slot" in
    [0-9a-f][0-9a-f][0-9a-f][0-9a-f]:[0-9a-f][0-9a-f]:[0-9a-f][0-9a-f].[0-7]) ;;
    *) echo "not a PCI function: $slot" >&2; exit 2 ;;
  esac
  dev=/sys/bus/pci/devices/$slot
  [ -e "$dev" ] || { echo "absent: $slot" >&2; exit 3; }
  drv=$(basename "$(readlink "$dev/driver" 2>/dev/null)" 2>/dev/null || true)
  [ "$drv" = vfio-pci ] && continue
  [ -n "$drv" ] && [ "$drv" != . ] && echo "$slot" > "$dev/driver/unbind"
  echo vfio-pci > "$dev/driver_override"
  echo "$slot" > /sys/bus/pci/drivers/vfio-pci/bind
done
# A nouveau that loaded before the reservation keeps hooks into the device even
# unbound; it is unloaded once nothing uses it any more.
if [ -d /sys/module/nouveau ] && [ "$(cat /sys/module/nouveau/refcnt 2>/dev/null || echo 1)" = 0 ]; then
  modprobe -r nouveau || true
fi
`

func acceleratorResolutions() []Resolution {
	return []Resolution{
		{
			ID: ResolutionThunderboltAuthorize, Title: "Authorize the Thunderbolt enclosure",
			AppliesTo: CheckThunderbolt, Mode: ModeApply,
			Summary: "The enclosure is attached but the node's Thunderbolt security keeps it off the PCI bus until it is approved.",
			Files: []FileChange{{
				Path: "/etc/udev/rules.d/90-kombify-thunderbolt.rules", Mode: 0o644,
				Append: thunderboltRule, AppendUnlessPresent: "$TB_UNIQUE_ID", Backup: true,
			}},
			Commands: [][]string{
				{"udevadm", "control", "--reload"},
				{"udevadm", "trigger", "--subsystem-match=thunderbolt", "--action=add"},
			},
			Guidance: []string{
				"Only this enclosure is approved, by its unique id; another device on the same port stays blocked.",
				"The approval survives a restart and a re-plug. A GPU behind the enclosure appears on the bus shortly after.",
				"Undo by restoring the rules file from its kept copy; an already authorized device stays attached until it is unplugged.",
			},
			RequiresRoot: true, Reversible: true,
		},
		{
			ID: ResolutionIOMMUEnable, Title: "Enable the IOMMU",
			AppliesTo: CheckIOMMU, Mode: ModeHint,
			Summary: "Without an IOMMU the node cannot isolate a device for a guest.",
			Guidance: []string{
				"Enable SVM/VT-d and IOMMU in the firmware setup first; on AMD the kernel then turns it on by itself.",
				"On Intel, add intel_iommu=on to the kernel command line and refresh the boot loader with proxmox-boot-tool.",
				"Readiness does not edit the boot loader: a wrong command line can leave the node unbootable, which only someone at the machine can recover.",
			},
			RequiresRoot: true, RequiresReboot: true, Reversible: true,
		},
		{
			ID: ResolutionIOMMUGroup, Title: "Give the GPU an IOMMU group of its own",
			AppliesTo: CheckIOMMUGroup, Mode: ModeHint,
			Summary: "Everything in the GPU's group would go to the guest with it, including devices the node needs.",
			Guidance: []string{
				"Move the GPU to a slot wired to the CPU, or connect it through Thunderbolt/USB4, which usually gets a group of its own.",
				"A firmware update or an ACS option in the firmware setup can split groups on some boards.",
				"Readiness will not apply the ACS override patch: it pretends devices are isolated that are not, which is a security decision for the owner.",
			},
		},
		{
			ID: ResolutionVFIOBind, Title: "Reserve the GPU for guests",
			AppliesTo: CheckAcceleratorBind, Mode: ModeApply,
			Summary: "A host driver holds the GPU or its audio function, so a guest start races the node for it.",
			Files: []FileChange{
				{Path: "/etc/modprobe.d/kombify-vfio.conf", Mode: 0o644, Content: vfioModprobe, Backup: true},
				{Path: "/etc/modules-load.d/kombify-vfio.conf", Mode: 0o644, Content: vfioModulesLoad, Backup: true},
				{Path: vfioRebindPath, Mode: 0o755, Content: vfioRebind, Backup: true},
			},
			Commands: [][]string{
				{"update-initramfs", "-u", "-k", "all"},
				{vfioRebindPath, "$PCI_FUNCTIONS"},
			},
			Guidance: []string{
				"The node can no longer use this GPU itself; its console stays on the boot GPU.",
				"The GPU is moved to vfio-pci now, and the reservation holds from the next restart on.",
				"Undo by removing the three files, running update-initramfs -u -k all and restarting the node.",
			},
			RequiresRoot: true, RequiresReboot: true, Reversible: true,
		},
	}
}
