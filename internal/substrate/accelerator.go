package substrate

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// GPU passthrough gives one guest a whole PCI device. The node has to be
// prepared first (device readiness: Thunderbolt approval, IOMMU, vfio-pci
// reservation); this adapter only places an already reservable device into a
// guest it owns, and refuses one that another guest holds or that shares its
// IOMMU group with anything but its own functions.

var (
	pciDevicePattern   = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}$`)
	pciFunctionPattern = regexp.MustCompile(`^[0-9a-f]{4}:[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)
	thunderboltUID     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// PCIPassthrough is the device a guest receives, with what its pre-start
// hookscript needs to bring a late hot-plug device up.
type PCIPassthrough struct {
	// Address names the device without a function, so every function (the
	// GPU and its HDMI audio) goes to the guest together.
	Address string `json:"address"`
	// RootPort and Subtree are set for a device behind a hot-plug link. An
	// enclosure that initialises late is enumerated without its memory
	// windows; removing the subtree below the root port and rescanning from
	// the root port is the recovery, and QEMU crashes without it.
	RootPort string `json:"root_port,omitempty"`
	Subtree  string `json:"subtree,omitempty"`
	// ThunderboltUniqueID re-approves the enclosure if it was re-plugged while
	// the persistent approval rule had not fired yet.
	ThunderboltUniqueID string `json:"thunderbolt_unique_id,omitempty"`
}

func (p PCIPassthrough) validate() error {
	if !pciDevicePattern.MatchString(p.Address) {
		return errors.New("substrate: accelerator address must name a PCI device such as 0000:08:00")
	}
	if (p.RootPort == "") != (p.Subtree == "") {
		return errors.New("substrate: a hot-plug accelerator needs both its root port and its subtree")
	}
	if p.RootPort != "" && (!pciFunctionPattern.MatchString(p.RootPort) || !pciFunctionPattern.MatchString(p.Subtree)) {
		return errors.New("substrate: accelerator root port and subtree must be PCI function addresses")
	}
	if p.ThunderboltUniqueID != "" && !thunderboltUID.MatchString(p.ThunderboltUniqueID) {
		return errors.New("substrate: accelerator Thunderbolt id is not a unique id")
	}
	return nil
}

// PCIDevice is one device from the node's own PCI listing. Proxmox omits
// bridges by default, which is what a group-isolation decision needs: a bridge
// in the group does not go to the guest.
type PCIDevice struct {
	ID         string `json:"id"`
	Class      string `json:"class"`
	Vendor     string `json:"vendor"`
	Device     string `json:"device"`
	IOMMUGroup int    `json:"iommugroup"`
	VendorName string `json:"vendor_name,omitempty"`
	DeviceName string `json:"device_name,omitempty"`
}

// CheckAccelerator decides from the node's PCI listing whether address can be
// passed through: it is a display device, the IOMMU placed it in a group, and
// nothing outside its own functions shares that group.
func CheckAccelerator(devices []PCIDevice, address string) error {
	var gpu *PCIDevice
	for index := range devices {
		if devices[index].ID == address+".0" {
			gpu = &devices[index]
		}
	}
	switch {
	case gpu == nil:
		return fmt.Errorf("substrate: no PCI device %s on this node", address)
	case !strings.HasPrefix(gpu.Class, "0x03"):
		return fmt.Errorf("substrate: PCI device %s is not a display controller", address)
	case gpu.IOMMUGroup < 0:
		return errors.New("substrate: the node has no IOMMU group for the accelerator; enable the IOMMU first")
	}
	for _, device := range devices {
		if device.IOMMUGroup == gpu.IOMMUGroup && !strings.HasPrefix(device.ID, address+".") {
			return fmt.Errorf("substrate: accelerator %s shares IOMMU group %d with %s", address, gpu.IOMMUGroup, device.ID)
		}
	}
	return nil
}

func (c *Client) pciDevices(ctx context.Context) ([]PCIDevice, error) {
	var devices []PCIDevice
	err := c.request(ctx, http.MethodGet, c.nodePath()+"/hardware/pci", nil, &devices)
	return devices, err
}

// assertAcceleratorFree refuses a device another guest already names. Two
// guests holding one GPU means the second start fails, or worse, the first
// loses its device while running.
func (c *Client) assertAcceleratorFree(ctx context.Context, self int, address string) error {
	var guests []Guest
	if err := c.request(ctx, http.MethodGet, c.nodePath()+"/qemu", nil, &guests); err != nil {
		return err
	}
	for _, guest := range guests {
		if guest.ID == self {
			continue
		}
		config := map[string]any{}
		if err := c.request(ctx, http.MethodGet, c.vmPath(guest.ID)+"/config", nil, &config); err != nil {
			return err
		}
		for key, value := range config {
			text, _ := value.(string)
			if !strings.HasPrefix(key, "hostpci") {
				continue
			}
			host := strings.TrimPrefix(strings.SplitN(text, ",", 2)[0], "host=")
			for _, assigned := range strings.Split(host, ";") {
				if strings.Count(assigned, ":") == 1 {
					assigned = "0000:" + assigned
				}
				if assigned == address || strings.HasPrefix(assigned, address+".") {
					return fmt.Errorf("substrate: accelerator %s is already assigned to guest %d: %w", address, guest.ID, ErrConflict)
				}
			}
		}
	}
	return nil
}

// AcceleratorHookscript renders the pre-start hookscript for a guest's GPU.
//
// It waits up to 90 seconds for the device, re-approves a re-plugged
// enclosure, recovers a device enumerated without its memory windows, and moves
// every function to vfio-pci, because a host driver that grabbed the HDMI
// audio function makes the start fail. It does not change the link speed: a
// pinned link has not been proven to stop an enclosure from dropping the GPU.
func AcceleratorHookscript(guestID int, accelerator PCIPassthrough) string {
	return fmt.Sprintf(`#!/bin/sh
# Written by Kombify Techstack for guest %d: prepare its GPU before start.
[ "$2" = pre-start ] || exit 0
ADDR=%s
ROOT=%s
SUBTREE=%s
TB_UID=%s
DEV=/sys/bus/pci/devices
present() { [ -e "$DEV/$ADDR.0" ]; }
# A device enumerated without its memory windows reports a zero first BAR.
resourced() { [ "$(awk 'NR==1{print $1}' "$DEV/$ADDR.0/resource" 2>/dev/null)" != 0x0000000000000000 ]; }
rescan() {
  [ -n "$ROOT" ] || return 0
  [ -e "$DEV/$SUBTREE/remove" ] && echo 1 > "$DEV/$SUBTREE/remove" && sleep 1
  echo 1 > "$DEV/$ROOT/rescan"; sleep 2
}
authorize() {
  [ -n "$TB_UID" ] || return 0
  for tb in /sys/bus/thunderbolt/devices/*; do
    [ "$(cat "$tb/unique_id" 2>/dev/null)" = "$TB_UID" ] && [ "$(cat "$tb/authorized" 2>/dev/null)" = 0 ] && echo 1 > "$tb/authorized"
  done
  return 0
}
driver() { basename "$(readlink "$1/driver" 2>/dev/null)" 2>/dev/null; }
reserve() {
  for fn in "$DEV/$ADDR".*; do
    slot=${fn##*/}
    [ "$(driver "$fn")" = vfio-pci ] && continue
    [ -e "$fn/driver" ] && echo "$slot" > "$fn/driver/unbind"
    echo vfio-pci > "$fn/driver_override"
    echo "$slot" > /sys/bus/pci/drivers/vfio-pci/bind || return 1
  done
}
modprobe vfio-pci
i=0
while [ $i -lt 90 ]; do
  authorize
  if ! present; then
    [ $((i %% 5)) -eq 4 ] && rescan
  elif ! resourced; then
    rescan
  elif reserve; then
    exit 0
  fi
  i=$((i + 1)); sleep 1
done
echo "kombify: GPU $ADDR is not ready for guest $1" >&2
exit 1
`, guestID, accelerator.Address, accelerator.RootPort, accelerator.Subtree, accelerator.ThunderboltUniqueID)
}

// AcceleratorObservation is what the node-local Guard reports about the GPU a
// guest on this node could receive. It travels with the inventory so the
// creation flow fills the guest's accelerator in itself: nobody types a PCI
// address, and no credential for the node leaves it. Proposed is absent when
// the node has no isolated GPU; Findings then say what stands in the way.
type AcceleratorObservation struct {
	Observed bool                 `json:"observed"`
	Proposed *PCIPassthrough      `json:"proposed,omitempty"`
	Findings []AcceleratorFinding `json:"findings,omitempty"`
}

// AcceleratorFinding is one passthrough check that did not pass, in the
// readiness report's own words.
type AcceleratorFinding struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

// Sanitized keeps what the control plane may store from a Guard report: a
// proposal only in the shape a placement accepts, and a bounded list of short
// findings.
func (o AcceleratorObservation) Sanitized() AcceleratorObservation {
	clean := AcceleratorObservation{Observed: o.Observed}
	if o.Proposed != nil && o.Proposed.validate() == nil {
		proposed := *o.Proposed
		clean.Proposed = &proposed
	}
	for _, finding := range o.Findings {
		if len(clean.Findings) == 8 {
			break
		}
		if finding.ID == "" || len(finding.ID) > 64 || len(finding.Status) > 16 || len(finding.Summary) > 300 {
			continue
		}
		clean.Findings = append(clean.Findings, finding)
	}
	return clean
}
