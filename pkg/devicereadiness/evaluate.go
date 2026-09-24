package devicereadiness

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Check ids. They are part of the report boundary: a resolution binds to the
// check that justifies it, and the creation flow renders by id, not by text.
const (
	CheckHypervisor    = "device-hypervisor"
	CheckLink          = "network-link"
	CheckDriver        = "network-driver"
	CheckAddress       = "network-address"
	CheckRoute         = "network-route"
	CheckNetplan       = "network-declared-config"
	CheckResolver      = "network-resolver"
	CheckControlPlane  = "reach-control-plane"
	CheckPackageMirror = "reach-package-mirror"
	CheckRegistry      = "reach-image-registry"
	CheckClock         = "host-clock"
	CheckPackages      = "host-package-manager"
	CheckCloudInit     = "host-cloud-init"
	CheckOS            = "host-operating-system"
)

// maxAcceptableSkew is the point past which TLS starts failing in ways that
// look like a network fault. It is deliberately generous: the check exists to
// catch a device with no time source at all, not to police drift.
const maxAcceptableSkew = 5 * time.Minute

// Evaluate turns measured facts into a readiness report.
//
// A fact that was not observed produces an unknown check, never a pass. The
// report's readiness is the answer to "what happens next", which is not the
// same as the worst check: a device can be blocked and still repairable, and a
// hypervisor node is neither.
func Evaluate(facts Facts) Report {
	checks := []Check{
		checkHypervisor(facts),
		checkOS(facts),
		checkDriver(facts),
		checkLink(facts),
		checkAddress(facts),
		checkRoute(facts),
		checkNetplan(facts),
		checkResolver(facts),
		checkClock(facts),
		checkPackages(facts),
		checkCloudInit(facts),
	}
	checks = append(checks, reachChecks(facts)...)
	sort.SliceStable(checks, func(i, j int) bool { return checks[i].ID < checks[j].ID })

	report := Report{
		SchemaVersion: SchemaVersion,
		EvaluatedAt:   time.Now().UTC(),
		Checks:        checks,
		Facts:         facts,
	}
	report.Status = worstStatus(checks)
	report.Resolutions = ResolutionsForReport(report)
	report.Readiness = decideReadiness(report, facts)
	return report
}

// decideReadiness answers what the operator can do next.
func decideReadiness(report Report, facts Facts) Readiness {
	if facts.Hypervisor.Observed && facts.Hypervisor.Kind != "" {
		return ReadinessSubstrate
	}
	blocking := report.Blocking()
	if len(blocking) == 0 {
		return ReadinessReady
	}
	// Repairable means something in the catalog can actually be carried out
	// for a blocking check. A catalog entry that only advises leaves the
	// decision with the owner, which is a different answer and must not be
	// dressed up as a fix.
	blockingIDs := make(map[string]bool, len(blocking))
	for _, check := range blocking {
		blockingIDs[check.ID] = true
	}
	for _, resolution := range report.Resolutions {
		if resolution.Mode == ModeApply && blockingIDs[resolution.AppliesTo] {
			return ReadinessRepairable
		}
	}
	return ReadinessNeedsOwner
}

func checkHypervisor(facts Facts) Check {
	check := Check{ID: CheckHypervisor}
	switch {
	case !facts.Hypervisor.Observed:
		check.Status = StatusUnknown
		check.Summary = "Could not determine whether this device is a hypervisor node"
	case facts.Hypervisor.Kind == "":
		check.Status = StatusPass
		check.Summary = "This device is an ordinary host, not a hypervisor node"
	default:
		check.Status = StatusWarning
		check.Summary = "This device is a " + facts.Hypervisor.Kind + " hypervisor node"
		check.Detail = "Register it as a substrate and create a guest on it rather than installing onto the node itself."
	}
	return check
}

func checkOS(facts Facts) Check {
	check := Check{ID: CheckOS}
	if !facts.OS.Observed {
		check.Status = StatusUnknown
		check.Summary = "The operating system could not be identified"
		return check
	}
	switch facts.OS.ID {
	case "ubuntu", "debian":
		check.Status = StatusPass
		check.Summary = "Operating system " + facts.OS.Pretty + " is supported"
	case "":
		check.Status = StatusUnknown
		check.Summary = "The operating system did not identify itself"
	default:
		check.Status = StatusWarning
		check.Summary = "Operating system " + facts.OS.Pretty + " is outside the tested set"
		check.Detail = "Readiness still reports what it measured; enrollment may refuse later."
	}
	return check
}

func checkDriver(facts Facts) Check {
	check := Check{ID: CheckDriver}
	if len(facts.UnclaimedNICs) == 0 {
		if len(facts.PrimaryInterfaces()) == 0 && len(facts.Interface) == 0 {
			check.Status = StatusUnknown
			check.Summary = "Network hardware could not be enumerated"
			return check
		}
		check.Status = StatusPass
		check.Summary = "Every network controller has a driver bound"
		return check
	}
	check.Status = StatusBlocked
	check.Summary = fmt.Sprintf("%d network controller(s) have no driver bound", len(facts.UnclaimedNICs))
	check.Detail = describeControllers(facts.UnclaimedNICs)
	return check
}

func describeControllers(controllers []NetController) string {
	parts := make([]string, 0, len(controllers))
	for _, controller := range controllers {
		label := controller.Description
		if label == "" {
			label = controller.VendorID + ":" + controller.DeviceID
		}
		parts = append(parts, controller.Slot+" "+label)
	}
	return strings.Join(parts, "; ")
}

// checkLink asks whether this machine can put a packet on a network.
//
// The bus is evidence, not proof. A physical adapter appears in sysfs with a
// device behind it, but a machine can also be reached through an uplink that
// has none: a virtualized adapter, a bridge, a tunnel. Working traffic
// therefore outranks the absence of a bus entry, because a report that calls a
// machine unreachable while its own reachability probes succeeded is wrong
// about the machine and useless to the operator.
func checkLink(facts Facts) Check {
	primary := facts.PrimaryInterfaces()
	for _, iface := range primary {
		if strings.EqualFold(iface.OperState, "up") {
			return Check{ID: CheckLink, Status: StatusPass, Summary: "Interface " + iface.Name + " reports the link as up"}
		}
	}

	if uplink, ok := workingUplink(facts); ok {
		return Check{
			ID: CheckLink, Status: StatusPass,
			Summary: "Interface " + uplink + " carries working traffic",
			Detail:  "It is not a physical adapter, so it is a virtualized, bridged or tunnelled uplink.",
		}
	}

	if len(primary) == 0 {
		return Check{
			ID: CheckLink, Status: StatusBlocked,
			Summary: "This device has no usable network interface",
			Detail:  "No adapter was found and nothing is carrying traffic.",
		}
	}

	// A down link with a bound driver is usually a cable, a switch port, or an
	// interface nothing ever brought up. All three are worth naming, and the
	// first two are outside our reach.
	return Check{
		ID: CheckLink, Status: StatusBlocked,
		Summary: "No network interface reports the link as up",
		Detail:  "Interfaces found: " + describeInterfaces(primary),
	}
}

// workingUplink names an interface that is demonstrably carrying traffic: it
// holds an address, the machine has somewhere to send packets, and something
// outside answered. All three together are stronger evidence than any single
// one, and stronger than what sysfs says about the bus.
func workingUplink(facts Facts) (string, bool) {
	if !facts.Routes.Observed || facts.Routes.DefaultGateway == "" {
		return "", false
	}
	if !anyEndpointAnswered(facts) {
		return "", false
	}
	for _, iface := range facts.Interface {
		if len(iface.Addresses) == 0 || strings.EqualFold(iface.Name, "lo") {
			continue
		}
		if facts.Routes.DefaultDevice == "" || facts.Routes.DefaultDevice == iface.Name {
			return iface.Name, true
		}
	}
	return "", false
}

func anyEndpointAnswered(facts Facts) bool {
	for _, probe := range facts.Reachability {
		if probe.Result == ReachOK {
			return true
		}
	}
	return false
}

func describeInterfaces(interfaces []NetInterface) string {
	parts := make([]string, 0, len(interfaces))
	for _, iface := range interfaces {
		state := iface.OperState
		if state == "" {
			state = "unknown"
		}
		parts = append(parts, iface.Name+" ("+state+")")
	}
	return strings.Join(parts, ", ")
}

func checkAddress(facts Facts) Check {
	check := Check{ID: CheckAddress}
	if uplink, ok := workingUplink(facts); ok {
		check.Status = StatusPass
		check.Summary = "Interface " + uplink + " carries an address and reaches the network"
		return check
	}
	if len(facts.PrimaryInterfaces()) == 0 {
		check.Status = StatusSkipped
		check.Summary = "No usable interface to carry an address"
		return check
	}
	if facts.HasAddress() {
		check.Status = StatusPass
		check.Summary = "A network interface carries an address"
		return check
	}
	check.Status = StatusBlocked
	check.Summary = "No network interface has an address"
	check.Detail = "The link exists but nothing assigned it an address, which points at the declared configuration or at DHCP."
	return check
}

func checkRoute(facts Facts) Check {
	check := Check{ID: CheckRoute}
	if !facts.Routes.Observed {
		check.Status = StatusUnknown
		check.Summary = "The routing table could not be observed"
		return check
	}
	if facts.Routes.DefaultGateway == "" {
		check.Status = StatusBlocked
		check.Summary = "This device has no default route"
		check.Detail = "Nothing outside the local segment is reachable until a gateway is configured."
		return check
	}
	check.Status = StatusPass
	check.Summary = "Default route via " + facts.Routes.DefaultGateway
	return check
}

func checkNetplan(facts Facts) Check {
	check := Check{ID: CheckNetplan}
	if !facts.Netplan.Observed {
		check.Status = StatusUnknown
		check.Summary = "The declared network configuration could not be read"
		return check
	}
	if !facts.Netplan.Present {
		// No declared configuration is only a problem if the device also has
		// no address. The address check already says that.
		check.Status = StatusWarning
		check.Summary = "This device declares no network configuration"
		return check
	}
	if facts.Netplan.ParseError != "" {
		check.Status = StatusBlocked
		check.Summary = "The declared network configuration is rejected by the device"
		check.Detail = facts.Netplan.ParseError
		return check
	}
	if facts.Netplan.RendererAvailable != nil && !*facts.Netplan.RendererAvailable {
		check.Status = StatusBlocked
		check.Summary = "The declared network configuration asks for a renderer this device does not run"
		check.Detail = "Renderers requested: " + strings.Join(facts.Netplan.Renderers, ", ")
		return check
	}
	if orphaned := orphanedInterfaces(facts); len(orphaned) > 0 {
		check.Status = StatusBlocked
		check.Summary = "The declared network configuration names interfaces this device does not have"
		check.Detail = "Named but absent: " + strings.Join(orphaned, ", ")
		return check
	}
	check.Status = StatusPass
	check.Summary = "The declared network configuration matches the interfaces present"
	return check
}

// orphanedInterfaces returns configured interface names with no matching
// interface on the device. Wildcards are skipped: a match rule that selects
// nothing is not distinguishable here from one that selects everything, and
// guessing would turn a passing device into a blocked one.
func orphanedInterfaces(facts Facts) []string {
	present := make(map[string]bool, len(facts.Interface))
	for _, iface := range facts.Interface {
		present[iface.Name] = true
	}
	orphaned := make([]string, 0)
	for _, name := range facts.Netplan.ConfiguredInterfaces {
		if strings.ContainsAny(name, "*?[") {
			continue
		}
		if !present[name] {
			orphaned = append(orphaned, name)
		}
	}
	return orphaned
}

func checkResolver(facts Facts) Check {
	check := Check{ID: CheckResolver}
	if !facts.Resolver.Observed {
		check.Status = StatusUnknown
		check.Summary = "Name resolution could not be observed"
		return check
	}
	if len(facts.Resolver.Servers) == 0 {
		check.Status = StatusBlocked
		check.Summary = "This device has no nameserver configured"
		return check
	}
	if dnsFailed(facts) {
		check.Status = StatusBlocked
		check.Summary = "Nameservers are configured but no name resolved"
		check.Detail = "Configured: " + strings.Join(facts.Resolver.Servers, ", ")
		return check
	}
	check.Status = StatusPass
	check.Summary = fmt.Sprintf("%d nameserver(s) configured and answering", len(facts.Resolver.Servers))
	return check
}

func dnsFailed(facts Facts) bool {
	if len(facts.Reachability) == 0 {
		return false
	}
	for _, probe := range facts.Reachability {
		if probe.Result != ReachDNSFailed {
			return false
		}
	}
	return true
}

// reachEndpointChecks maps a probe endpoint role to the check it answers. The
// probe reports roles rather than hostnames so that the evaluation does not
// need to know the deployment's origins.
var reachEndpointChecks = map[string]struct {
	id      string
	subject string
	blocks  bool
}{
	"control-plane":  {CheckControlPlane, "the Techstack control plane", true},
	"package-mirror": {CheckPackageMirror, "the package mirror", false},
	"image-registry": {CheckRegistry, "the image registry", false},
}

func reachChecks(facts Facts) []Check {
	byRole := make(map[string]ReachProbe, len(facts.Reachability))
	for _, probe := range facts.Reachability {
		byRole[probe.Endpoint] = probe
	}
	checks := make([]Check, 0, len(reachEndpointChecks))
	for role, spec := range reachEndpointChecks {
		check := Check{ID: spec.id}
		probe, found := byRole[role]
		switch {
		case !found, probe.Result == ReachUnknown:
			check.Status = StatusUnknown
			check.Summary = "Reachability of " + spec.subject + " could not be tested"
		case probe.Result == ReachOK:
			check.Status = StatusPass
			check.Summary = "This device reaches " + spec.subject
		default:
			if spec.blocks {
				check.Status = StatusBlocked
			} else {
				check.Status = StatusWarning
			}
			check.Summary = "This device cannot reach " + spec.subject
			check.Detail = reachDetail(probe)
		}
		checks = append(checks, check)
	}
	return checks
}

func reachDetail(probe ReachProbe) string {
	switch probe.Result {
	case ReachDNSFailed:
		return "The name did not resolve."
	case ReachUnreachable:
		return "The name resolved but the connection did not complete."
	case ReachTLSFailed:
		return "The connection completed but the secure handshake did not, which usually means the clock."
	default:
		return probe.Detail
	}
}

func checkClock(facts Facts) Check {
	check := Check{ID: CheckClock}
	if !facts.Clock.Observed {
		check.Status = StatusUnknown
		check.Summary = "Time synchronization could not be observed"
		return check
	}
	if facts.Clock.SkewSeconds != nil {
		skew := math.Abs(*facts.Clock.SkewSeconds)
		if skew > maxAcceptableSkew.Seconds() {
			check.Status = StatusBlocked
			check.Summary = fmt.Sprintf("The device clock is %.0f seconds away from the operator clock", skew)
			check.Detail = "Secure connections fail at this offset, so every other symptom will look like a network fault."
			return check
		}
	}
	if facts.Clock.Synchronized != nil && !*facts.Clock.Synchronized {
		check.Status = StatusWarning
		check.Summary = "The device clock is not synchronized to a time source"
		check.Detail = "It is currently close enough to work, but it will drift."
		return check
	}
	check.Status = StatusPass
	check.Summary = "The device clock is usable"
	return check
}

func checkPackages(facts Facts) Check {
	check := Check{ID: CheckPackages}
	if !facts.Packages.Observed {
		check.Status = StatusUnknown
		check.Summary = "The package manager state could not be observed"
		return check
	}
	if facts.Packages.Locked != nil && *facts.Packages.Locked {
		check.Status = StatusWarning
		check.Summary = "Another process holds the package manager"
		check.Detail = "Installing anything will wait for it, which is normal shortly after a fresh install."
		return check
	}
	if facts.Packages.SourcesConfigured != nil && !*facts.Packages.SourcesConfigured {
		check.Status = StatusBlocked
		check.Summary = "This device has no package sources configured"
		check.Detail = "Repairing the network will not make packages installable."
		return check
	}
	check.Status = StatusPass
	check.Summary = "The package manager is usable"
	return check
}

func checkCloudInit(facts Facts) Check {
	check := Check{ID: CheckCloudInit}
	switch {
	case !facts.CloudInit.Observed:
		check.Status = StatusUnknown
		check.Summary = "First-boot provisioning state could not be observed"
	case !facts.CloudInit.Present:
		check.Status = StatusSkipped
		check.Summary = "This device does not use first-boot provisioning"
	case facts.CloudInit.Status == "running":
		check.Status = StatusWarning
		check.Summary = "First-boot provisioning is still running"
		check.Detail = "It may rewrite the network configuration, so let it finish before repairing anything."
	case facts.CloudInit.Status == "error":
		check.Status = StatusWarning
		check.Summary = "First-boot provisioning finished with an error"
		check.Detail = "Whatever it failed to configure is unconfigured, which may include the network."
	default:
		check.Status = StatusPass
		check.Summary = "First-boot provisioning completed"
	}
	return check
}
