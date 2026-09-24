package devicereadiness

// Resolutions are the fixes for what readiness found.
//
// The catalog is closed on purpose. An executor that can run arbitrary commands
// against an unenrolled device is a very large amount of authority, so what it
// may do is enumerated here, bound to the check that justifies it, and each
// entry declares whether it can be undone, whether it needs root, whether it
// needs a reboot, and whether anyone may run it unattended.
//
// Nothing here executes on its own. Every apply requires an explicit decision,
// and anything that needs a credential, a reboot, or a physical act stays
// advice no matter what was decided.

// Mode says whether a resolution can be carried out or only described.
type Mode string

const (
	// ModeHint is advice. Something outside the executor's authority has to
	// change: a cable, a switch port, a credential, a decision only the owner
	// can make.
	ModeHint Mode = "hint"
	// ModeApply can be carried out against this device.
	ModeApply Mode = "apply"
)

// FileChange is one declared file edit. Content and Append are exclusive:
// content writes a self-contained drop-in, append adds a line to a file that
// must otherwise stay as it is.
type FileChange struct {
	Path string `json:"path"`
	Mode uint32 `json:"mode"`

	Content string `json:"content,omitempty"`
	Append  string `json:"append,omitempty"`

	// AppendUnlessPresent keeps an append idempotent: the line is added only
	// when the file does not already mention this substring.
	AppendUnlessPresent string `json:"appendUnlessPresent,omitempty"`

	// Backup keeps the previous content beside the file so the change can be
	// reversed without the executor remembering anything.
	Backup bool `json:"backup"`
}

// Resolution is one fix, bound to the check that justifies it.
type Resolution struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	AppliesTo string `json:"appliesTo"`
	Mode      Mode   `json:"mode"`
	Summary   string `json:"summary"`

	// Files are written before Commands run, so a service restart observes the
	// configuration it is being restarted for.
	Files    []FileChange `json:"files,omitempty"`
	Commands [][]string   `json:"commands,omitempty"`

	// Guidance is what to tell the operator: the whole content of a hint, and
	// the caveats of an apply.
	Guidance []string `json:"guidance,omitempty"`

	RequiresRoot   bool `json:"requiresRoot"`
	RequiresReboot bool `json:"requiresReboot"`

	// RequiresEgress marks a resolution that cannot run on a device with no
	// route out. The executor must open its allow-listed egress first, and the
	// operator must have consented to that.
	RequiresEgress bool `json:"requiresEgress"`

	// Reversible means this device can be put back the way it was: a backup is
	// kept, or the change is a drop-in that can simply be removed.
	Reversible bool `json:"reversible"`

	// AutoEligible marks a resolution an unattended run may carry out. It
	// requires Reversible, no reboot, no egress and no credential: a fix
	// nobody is watching must be one nobody regrets.
	AutoEligible bool `json:"autoEligible"`
}

// Resolution ids, referenced by receipts and by the creation flow.
const (
	ResolutionNetplanDHCPDropin = "netplan-dhcp-dropin"
	ResolutionNetplanRenderer   = "netplan-renderer-mismatch"
	ResolutionNetplanOrphan     = "netplan-orphaned-interface"
	ResolutionLinkUp            = "interface-link-up"
	ResolutionCableCheck        = "interface-physical-link"
	ResolutionResolverRestart   = "resolver-restart"
	ResolutionResolverFallback  = "resolver-fallback-dns"
	ResolutionClockSet          = "clock-set-from-operator"
	ResolutionDriverModules     = "nic-driver-extra-modules"
	ResolutionDriverVendor      = "nic-driver-vendor-module"
	ResolutionUplinkTether      = "uplink-usb-tether"
	ResolutionProxyHonour       = "egress-existing-proxy"
	ResolutionPackageSources    = "package-sources-missing"
	ResolutionRegisterSubstrate = "register-as-substrate"
)

// netplanDHCPDropin is written as a drop-in with a high sort order so it wins
// over whatever the device already declares, and can be removed to undo.
const netplanDHCPDropin = `# Written by Kombify Techstack device readiness.
# Remove this file and run "netplan apply" to undo.
network:
  version: 2
  renderer: networkd
  ethernets:
    kombify-readiness-uplink:
      match:
        name: "en*"
      dhcp4: true
      dhcp6: false
      optional: true
`

const resolvedFallbackDropin = `# Written by Kombify Techstack device readiness.
# Remove this file and restart systemd-resolved to undo.
[Resolve]
FallbackDNS=9.9.9.9 1.1.1.1
`

// Resolutions is the closed catalog, ordered by the check they answer.
func Resolutions() []Resolution {
	return []Resolution{
		{
			ID: ResolutionDriverModules, Title: "Install the kernel modules for this network card",
			AppliesTo: CheckDriver, Mode: ModeApply,
			Summary: "A network controller is present but no driver is bound; the kernel's extra-modules package usually contains it.",
			Commands: [][]string{
				{"apt-get", "install", "-y", "linux-modules-extra-$(uname -r)"},
				{"update-initramfs", "-u"},
				{"modprobe", "-a", "--all-modules"},
			},
			Guidance: []string{
				"This device cannot fetch the package on its own, so the executor opens a temporary, allow-listed path out for the duration of the fix.",
				"Some controllers only come up after a restart; readiness re-probes first and asks for one only if the interface is still missing.",
				"Undo by removing the package; nothing else on the device is changed.",
			},
			RequiresRoot: true, RequiresEgress: true, Reversible: true, AutoEligible: false,
		},
		{
			ID: ResolutionDriverVendor, Title: "Install the vendor driver for this network card",
			AppliesTo: CheckDriver, Mode: ModeHint,
			Summary: "Some controllers, notably recent 2.5-gigabit Realtek and several Broadcom wireless parts, need a vendor module the distribution does not ship.",
			Guidance: []string{
				"Identify the controller from the readiness detail, then install the matching vendor module package.",
				"The build needs the kernel headers, so it will not work on a device that still has no way out; repair the uplink another way first, or attach a temporary one.",
				"Readiness will not choose a vendor module for you: which out-of-tree driver a machine runs is a trust decision.",
			},
			RequiresRoot: true, Reversible: true,
		},
		{
			ID: ResolutionLinkUp, Title: "Bring the interface up",
			AppliesTo: CheckLink, Mode: ModeApply,
			Summary: "An interface exists with a driver bound, but nothing ever brought it up.",
			Commands: [][]string{
				{"ip", "link", "set", "dev", "$IFACE", "up"},
			},
			Guidance: []string{
				"This is not persistent on its own; the declared-configuration fix is what survives a restart.",
				"Undo with the same command and \"down\".",
			},
			RequiresRoot: true, Reversible: true, AutoEligible: true,
		},
		{
			ID: ResolutionCableCheck, Title: "Check the physical link",
			AppliesTo: CheckLink, Mode: ModeHint,
			Summary: "The interface is up but the link stays down, which is a cable, a port, or a switch.",
			Guidance: []string{
				"Check the cable at both ends and confirm the switch port is enabled.",
				"If the device has more than one socket, try the other one: readiness reports which interface it measured.",
				"Nothing inside the device can fix this, which is why it is advice.",
			},
		},
		{
			ID: ResolutionNetplanDHCPDropin, Title: "Configure the uplink for automatic addressing",
			AppliesTo: CheckAddress, Mode: ModeApply,
			Summary: "The link is up but nothing assigned an address; a drop-in that matches the wired interfaces and asks for DHCP is the least surprising fix.",
			Files: []FileChange{{
				Path: "/etc/netplan/95-kombify-readiness.yaml", Mode: 0o600,
				Content: netplanDHCPDropin, Backup: false,
			}},
			Commands: [][]string{
				{"netplan", "apply"},
			},
			Guidance: []string{
				"The drop-in is additive and marked optional, so it does not fight a working configuration that is already there.",
				"It only helps where a DHCP server exists; a network that hands out nothing needs a static address instead.",
				"Undo by deleting the file and running \"netplan apply\".",
			},
			RequiresRoot: true, Reversible: true, AutoEligible: true,
		},
		{
			ID: ResolutionNetplanOrphan, Title: "Point the declared configuration at an interface that exists",
			AppliesTo: CheckNetplan, Mode: ModeApply,
			Summary: "The declared configuration names an interface this device does not have, so it configures nothing.",
			Files: []FileChange{{
				Path: "/etc/netplan/95-kombify-readiness.yaml", Mode: 0o600,
				Content: netplanDHCPDropin, Backup: false,
			}},
			Commands: [][]string{
				{"netplan", "apply"},
			},
			Guidance: []string{
				"The stale file is left exactly as it is; the drop-in sorts after it and matches by pattern rather than by name.",
				"Interface names change when hardware or firmware changes, which is why matching a pattern is the durable answer.",
				"Undo by deleting the drop-in and running \"netplan apply\".",
			},
			RequiresRoot: true, Reversible: true, AutoEligible: true,
		},
		{
			ID: ResolutionNetplanRenderer, Title: "Render the network with the manager this device runs",
			AppliesTo: CheckNetplan, Mode: ModeHint,
			Summary: "The declared configuration asks for a renderer this device does not have, so none of it takes effect.",
			Guidance: []string{
				"Either install the renderer the files ask for, or change them to the one that is running.",
				"Readiness will not rewrite an existing declaration in place: which manager owns the network is an operator decision, and getting it wrong disconnects the device.",
				"The additive drop-in fix is the safe alternative when the goal is simply to get an address.",
			},
			RequiresRoot: true, Reversible: true,
		},
		{
			ID: ResolutionResolverRestart, Title: "Restart name resolution",
			AppliesTo: CheckResolver, Mode: ModeApply,
			Summary: "Nameservers are configured but nothing resolves, which a stuck resolver explains more often than the network does.",
			Commands: [][]string{
				{"systemctl", "restart", "systemd-resolved"},
			},
			Guidance: []string{
				"Nothing on the device is reconfigured; only the running resolver is restarted.",
				"If resolution still fails afterwards, the fault is upstream of this device.",
			},
			RequiresRoot: true, Reversible: true, AutoEligible: true,
		},
		{
			ID: ResolutionResolverFallback, Title: "Add a fallback nameserver",
			AppliesTo: CheckResolver, Mode: ModeApply,
			Summary: "The device has no working nameserver, so a public fallback gets it far enough to be enrolled.",
			Files: []FileChange{{
				Path: "/etc/systemd/resolved.conf.d/95-kombify-readiness.conf", Mode: 0o644,
				Content: resolvedFallbackDropin,
			}},
			Commands: [][]string{
				{"systemctl", "restart", "systemd-resolved"},
			},
			Guidance: []string{
				"A fallback is only consulted when the configured servers answer nothing, so it does not take name resolution away from the local network.",
				"On a network that deliberately resolves internal names only, remove the drop-in once the real resolver works.",
				"Undo by deleting the drop-in and restarting the resolver.",
			},
			RequiresRoot: true, Reversible: true, AutoEligible: false,
		},
		{
			ID: ResolutionClockSet, Title: "Set the clock from the operator machine",
			AppliesTo: CheckClock, Mode: ModeApply,
			Summary: "The device clock is far enough off that secure connections fail, which makes every other fault look like a network problem.",
			Commands: [][]string{
				{"timedatectl", "set-time", "$OPERATOR_TIME"},
				{"timedatectl", "set-ntp", "true"},
			},
			Guidance: []string{
				"The time comes from the machine running this repair, which is the only trustworthy source a disconnected device has.",
				"Automatic synchronization is switched back on afterwards so the device keeps itself right once it has a network.",
			},
			RequiresRoot: true, Reversible: true, AutoEligible: true,
		},
		{
			ID: ResolutionUplinkTether, Title: "Attach a temporary uplink",
			AppliesTo: CheckRoute, Mode: ModeHint,
			Summary: "A phone shared over USB gives the device a route long enough to install what it is missing.",
			Guidance: []string{
				"Connect a phone by cable and enable USB tethering; most devices pick the interface up without any configuration.",
				"This is worth doing when the missing piece is the driver for the device's own network card.",
				"Detach it once the built-in interface works; readiness re-probes and will tell you.",
			},
		},
		{
			ID: ResolutionProxyHonour, Title: "Use the proxy this device already has",
			AppliesTo: CheckControlPlane, Mode: ModeHint,
			Summary: "This device is configured to reach the outside through a proxy, so the repair path must go through it rather than around it.",
			Guidance: []string{
				"Confirm the proxy allows the package mirror, the image registry and the Techstack origin.",
				"Readiness does not open its own way out while a proxy is configured: two egress paths on one device produce failures that are very hard to read.",
			},
		},
		{
			ID: ResolutionPackageSources, Title: "Configure package sources",
			AppliesTo: CheckPackages, Mode: ModeHint,
			Summary: "This device has no package sources, so nothing can be installed even once the network works.",
			Guidance: []string{
				"Restore the distribution's default sources, or point the device at the mirror your network provides.",
				"Readiness will not choose a mirror for you: where a machine's software comes from is a trust decision.",
			},
			RequiresRoot: true, Reversible: true,
		},
		{
			ID: ResolutionRegisterSubstrate, Title: "Register this hypervisor and create a guest on it",
			AppliesTo: CheckHypervisor, Mode: ModeHint,
			Summary: "This device is a hypervisor node, so it is where hosts come from rather than a host itself.",
			Guidance: []string{
				"Register it as a substrate; Techstack then builds a standard image on it and creates the guest that gets enrolled.",
				"The node itself is never turned into a StackKits host: that would put the workload and the thing that runs it in the same place.",
			},
		},
	}
}

// ResolutionByID returns one catalog entry.
func ResolutionByID(id string) (Resolution, bool) {
	for _, resolution := range Resolutions() {
		if resolution.ID == id {
			return resolution, true
		}
	}
	return Resolution{}, false
}

// ResolutionsForReport returns the resolutions that answer what this report
// actually found. A passing check needs no fix, so only warnings, blocks and
// unknowns bring one forward.
func ResolutionsForReport(report Report) []Resolution {
	wanted := make(map[string]bool, len(report.Checks))
	for _, check := range report.Checks {
		switch check.Status {
		case StatusWarning, StatusBlocked, StatusUnknown:
			wanted[check.ID] = true
		}
	}
	matched := make([]Resolution, 0, len(wanted))
	for _, resolution := range Resolutions() {
		if wanted[resolution.AppliesTo] {
			matched = append(matched, resolution)
		}
	}
	return matched
}
