package devicereadiness_test

import (
	"context"
	"slices"
	"testing"

	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/pkg/devicereadiness"
)

// GPU passthrough hands a whole PCI device to one guest. What is protected
// here is that a node is never told its GPU is ready when it is not, that each
// fault brings forward the fix for exactly that fault, and that the fixes can
// only ever name devices the node's own report showed.

func proxmoxNode(accel devicereadiness.AcceleratorFacts) devicereadiness.Facts {
	facts := healthyFacts()
	facts.Hypervisor = devicereadiness.HypervisorFacts{Observed: true, Kind: "proxmox", Version: "9.2.2", Bridges: []string{"vmbr0"}}
	accel.Observed = true
	facts.Accelerators = accel
	return facts
}

func fn(slot, class, vendor, device, driver string) devicereadiness.PCIFunction {
	return devicereadiness.PCIFunction{Slot: slot, Class: class, VendorID: vendor, DeviceID: device, Driver: driver}
}

// thunderboltGPU mirrors the reference node: an RTX 3060 behind a Thunderbolt
// enclosure, sharing its group only with the root port, the enclosure's
// switch ports and AMD's dummy host bridge, and with its HDMI audio taken by
// the host's sound driver.
func thunderboltGPU() devicereadiness.PCIGPU {
	return devicereadiness.PCIGPU{
		PCIFunction: fn("0000:08:00.0", "0x030000", "10de", "2503", ""),
		Removable:   true,
		IOMMUGroup:  "7",
		GroupMembers: []devicereadiness.PCIFunction{
			fn("0000:00:03.0", "0x060000", "1022", "14ea", ""),
			fn("0000:00:03.1", "0x060400", "1022", "14ef", "pcieport"),
			fn("0000:06:00.0", "0x060400", "8086", "15da", "pcieport"),
			fn("0000:07:01.0", "0x060400", "8086", "15da", "pcieport"),
			fn("0000:08:00.0", "0x030000", "10de", "2503", ""),
			fn("0000:08:00.1", "0x040300", "10de", "228e", "snd_hda_intel"),
		},
		Upstream: []string{"0000:00:03.1", "0000:06:00.0", "0000:07:01.0"},
	}
}

func consoleGPU() devicereadiness.PCIGPU {
	return devicereadiness.PCIGPU{
		PCIFunction: fn("0000:66:00.0", "0x030000", "1002", "15bf", "amdgpu"), BootVGA: true, IOMMUGroup: "20",
		GroupMembers: []devicereadiness.PCIFunction{fn("0000:66:00.0", "0x030000", "1002", "15bf", "amdgpu")},
	}
}

func offers(report devicereadiness.Report, id string) bool {
	return slices.ContainsFunc(report.Resolutions, func(r devicereadiness.Resolution) bool { return r.ID == id })
}

func TestEnclosureAwaitingApprovalBlocksAndOffersItsApproval(t *testing.T) {
	yes := true
	facts := proxmoxNode(devicereadiness.AcceleratorFacts{
		IOMMUEnabled: &yes, IOMMUGroups: 32, ThunderboltSecurity: "user",
		Thunderbolt: []devicereadiness.ThunderboltDevice{{ID: "0-2", UniqueID: "be030000-0080-7d08-2326-1917aa211122", Vendor: "XYJ-LINK"}},
		GPUs:        []devicereadiness.PCIGPU{consoleGPU()},
	})

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckThunderbolt); got != devicereadiness.StatusBlocked {
		t.Fatalf("unauthorized enclosure: thunderbolt check = %q, want blocked", got)
	}
	if got := checkStatus(t, report, devicereadiness.CheckAcceleratorBind); got == devicereadiness.StatusPass {
		t.Fatal("the console GPU was offered as a passthrough GPU")
	}
	if !offers(report, devicereadiness.ResolutionThunderboltAuthorize) {
		t.Fatal("the approval fix was not offered for an enclosure awaiting approval")
	}
	if params := devicereadiness.AcceleratorParameters(facts); params.ThunderboltUniqueID != "be030000-0080-7d08-2326-1917aa211122" {
		t.Fatalf("approval would name %q, not the enclosure the node reported", params.ThunderboltUniqueID)
	}
}

func TestIsolatedGPUHeldByTheHostIsOfferedForReservation(t *testing.T) {
	yes := true
	facts := proxmoxNode(devicereadiness.AcceleratorFacts{
		IOMMUEnabled: &yes, IOMMUGroups: 32, ThunderboltSecurity: "user",
		Thunderbolt: []devicereadiness.ThunderboltDevice{{ID: "0-2", UniqueID: "be030000-0080-7d08-2326-1917aa211122", Authorized: true}},
		GPUs:        []devicereadiness.PCIGPU{consoleGPU(), thunderboltGPU()},
	})

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckIOMMUGroup); got != devicereadiness.StatusPass {
		t.Fatalf("a group of bridges and the GPU's own functions: group check = %q, want pass", got)
	}
	if got := checkStatus(t, report, devicereadiness.CheckAcceleratorBind); got != devicereadiness.StatusWarning {
		t.Fatalf("audio function on the host driver: binding check = %q, want warning", got)
	}
	if !offers(report, devicereadiness.ResolutionVFIOBind) {
		t.Fatal("the reservation fix was not offered for a GPU the host still holds")
	}
	params := devicereadiness.AcceleratorParameters(facts)
	if !slices.Equal(params.VFIOIDs, []string{"10de:2503", "10de:228e"}) {
		t.Fatalf("reservation ids = %v, want exactly the GPU and its audio", params.VFIOIDs)
	}
	if !slices.Equal(params.PCIFunctions, []string{"0000:08:00.0", "0000:08:00.1"}) {
		t.Fatalf("reservation functions = %v, want exactly the GPU's own device", params.PCIFunctions)
	}
	// The guest placement the creation flow proposes names the whole device,
	// the root port and the enclosure's bridge below it, and the enclosure.
	want := substrate.PCIPassthrough{Address: "0000:08:00", RootPort: "0000:00:03.1", Subtree: "0000:06:00.0", ThunderboltUniqueID: "be030000-0080-7d08-2326-1917aa211122"}
	if report.Accelerator == nil || *report.Accelerator != want {
		t.Fatalf("proposed accelerator = %+v, want %+v", report.Accelerator, want)
	}
}

func TestGPUSharingItsGroupIsNeitherReadyNorReserved(t *testing.T) {
	// Everything in an IOMMU group goes to the guest together. A GPU sharing
	// its group with the node's network card must never be reserved, or the
	// guest start would take the node off its own network.
	yes := true
	gpu := thunderboltGPU()
	gpu.GroupMembers = append(gpu.GroupMembers, fn("0000:09:00.0", "0x020000", "8086", "125c", "igc"))
	facts := proxmoxNode(devicereadiness.AcceleratorFacts{IOMMUEnabled: &yes, IOMMUGroups: 12, GPUs: []devicereadiness.PCIGPU{gpu}})

	report := devicereadiness.Evaluate(facts)

	if got := checkStatus(t, report, devicereadiness.CheckIOMMUGroup); got != devicereadiness.StatusBlocked {
		t.Fatalf("shared group: group check = %q, want blocked", got)
	}
	if params := devicereadiness.AcceleratorParameters(facts); len(params.VFIOIDs) != 0 || len(params.PCIFunctions) != 0 {
		t.Fatalf("a GPU sharing its group was selected for reservation: %+v", params)
	}
	if report.Accelerator != nil {
		t.Fatalf("a GPU sharing its group was proposed for a guest: %+v", report.Accelerator)
	}
}

func TestUnobservedNodeNeverReportsAGPUReady(t *testing.T) {
	facts := healthyFacts()
	facts.Hypervisor = devicereadiness.HypervisorFacts{Observed: true, Kind: "proxmox"}

	report := devicereadiness.Evaluate(facts)

	for _, id := range []string{devicereadiness.CheckThunderbolt, devicereadiness.CheckIOMMU, devicereadiness.CheckIOMMUGroup, devicereadiness.CheckAcceleratorBind} {
		if got := checkStatus(t, report, id); got != devicereadiness.StatusUnknown {
			t.Fatalf("PCI devices not observed: %s = %q, want unknown", id, got)
		}
	}
}

func TestPassthroughFixesRefuseValuesNoReportProduced(t *testing.T) {
	// The values reach a modprobe file, a udev rule and a root command. A value
	// shaped like anything but a PCI id, a PCI address or a Thunderbolt unique
	// id is refused before the session is used at all.
	var session devicereadiness.Session
	cases := map[string]struct {
		resolution string
		params     devicereadiness.Parameters
	}{
		"no GPU chosen":       {devicereadiness.ResolutionVFIOBind, devicereadiness.Parameters{}},
		"id carrying a line":  {devicereadiness.ResolutionVFIOBind, devicereadiness.Parameters{VFIOIDs: []string{"10de:2503\ninstall vfio-pci /bin/sh"}, PCIFunctions: []string{"0000:08:00.0"}}},
		"no enclosure chosen": {devicereadiness.ResolutionThunderboltAuthorize, devicereadiness.Parameters{}},
		"rule-breaking id":    {devicereadiness.ResolutionThunderboltAuthorize, devicereadiness.Parameters{ThunderboltUniqueID: `x", RUN+="/bin/sh`}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			receipt, err := session.Apply(context.Background(), tc.resolution, tc.params)
			if err == nil || receipt.Succeeded {
				t.Fatalf("%s was carried out with %+v", tc.resolution, tc.params)
			}
		})
	}
}
