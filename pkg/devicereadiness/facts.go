// Package devicereadiness observes a device that is not enrolled yet, says why
// it cannot be enrolled, and offers a closed set of fixes for what it found.
//
// It exists because a host can be perfectly good hardware and still be
// unreachable: a network interface whose driver never loaded, a netplan file
// naming an interface that does not exist, a resolver that answers nothing, a
// clock far enough off that TLS fails. Such a host cannot run the one-liner and
// cannot be reached by the control plane, but it is reachable over SSH from
// inside the user's own network. This package is what an executor on that
// network runs against it.
//
// Two rules are inherited from the StackKits host preflight and are not
// negotiable here. A fact that could not be observed is reported as unknown and
// never as pass, so a probe that silently failed can never admit a device it
// did not measure. And nothing in this package repairs anything on its own:
// every fix is a catalog entry that an executor applies only after an explicit
// decision.
package devicereadiness

import "time"

// SchemaVersion identifies the machine-readable readiness report. It is part of
// the HTTP and CLI boundary, so it changes only with the report shape.
const SchemaVersion = "techstack.device-readiness/v1"

// Status is the closed outcome vocabulary for a single check and for a report.
type Status string

const (
	// StatusPass means the check was performed and satisfied.
	StatusPass Status = "pass"
	// StatusWarning means the check was performed and the device is usable but
	// degraded. Enrollment proceeds.
	StatusWarning Status = "warning"
	// StatusBlocked means the check was performed and enrollment cannot
	// succeed until it is resolved.
	StatusBlocked Status = "blocked"
	// StatusUnknown means the fact could not be observed. It never silently
	// passes.
	StatusUnknown Status = "unknown"
	// StatusSkipped means the check does not apply to this device.
	StatusSkipped Status = "skipped"
)

// Reach is the outcome of one reachability attempt from the device outward.
type Reach string

const (
	// ReachOK means the endpoint answered.
	ReachOK Reach = "ok"
	// ReachDNSFailed means the name did not resolve, so the transport was
	// never attempted.
	ReachDNSFailed Reach = "dns_failed"
	// ReachUnreachable means the name resolved but the connection did not
	// complete: no route, refused, filtered, or timed out.
	ReachUnreachable Reach = "unreachable"
	// ReachTLSFailed means the connection completed but the TLS handshake did
	// not, which on an otherwise healthy network usually means the clock.
	ReachTLSFailed Reach = "tls_failed"
	// ReachUnknown means the probe could not perform the attempt at all,
	// typically because the device has no usable HTTP client.
	ReachUnknown Reach = "unknown"
)

// Facts is everything one probe run observed. Every optional field is a pointer
// or carries its own observed flag: the absence of a value means "not
// measured", never "measured as zero".
type Facts struct {
	SchemaVersion string    `json:"schemaVersion"`
	ObservedAt    time.Time `json:"observedAt"`

	OS        OSFacts        `json:"os"`
	Kernel    KernelFacts    `json:"kernel"`
	Interface []NetInterface `json:"interfaces,omitempty"`
	// UnclaimedNICs are network controllers the kernel enumerated on the bus
	// but bound no driver to. They are the reason a device with a perfectly
	// good cable reports no interface at all.
	UnclaimedNICs []NetController `json:"unclaimedNics,omitempty"`
	Routes        RouteFacts      `json:"routes"`
	Netplan       NetplanFacts    `json:"netplan"`
	Resolver      ResolverFacts   `json:"resolver"`
	Reachability  []ReachProbe    `json:"reachability,omitempty"`
	Proxy         ProxyFacts      `json:"proxy"`
	Clock         ClockFacts      `json:"clock"`
	Packages      PackageFacts    `json:"packages"`
	CloudInit     CloudInitFacts  `json:"cloudInit"`
	Hypervisor    HypervisorFacts `json:"hypervisor"`

	// ProbeErrors record what the probe itself could not do. They exist so a
	// half-failed probe degrades into unknown checks instead of into a report
	// that looks clean.
	ProbeErrors []string `json:"probeErrors,omitempty"`
}

// OSFacts identifies the operating system as the device describes itself.
type OSFacts struct {
	Observed  bool   `json:"observed"`
	ID        string `json:"id,omitempty"`
	VersionID string `json:"versionId,omitempty"`
	Codename  string `json:"codename,omitempty"`
	Pretty    string `json:"pretty,omitempty"`
}

// KernelFacts carries what the running kernel is, which decides which module
// package matches it.
type KernelFacts struct {
	Observed bool   `json:"observed"`
	Release  string `json:"release,omitempty"`
	Arch     string `json:"arch,omitempty"`
	// ExtraModulesInstalled reports whether the kernel's extra-modules package
	// is present. Its absence is the usual reason a common desktop network
	// controller has no driver on a server install.
	ExtraModulesInstalled *bool `json:"extraModulesInstalled,omitempty"`
}

// NetInterface is one network interface as the kernel currently sees it.
type NetInterface struct {
	Name string `json:"name"`
	// OperState is the kernel's own word for the link: up, down, unknown.
	OperState string `json:"operState,omitempty"`
	// Driver is empty when no driver is bound, which is what distinguishes a
	// cable problem from a missing module.
	Driver     string   `json:"driver,omitempty"`
	MACAddress string   `json:"macAddress,omitempty"`
	Addresses  []string `json:"addresses,omitempty"`
	// Virtual marks loopback, bridges, docker and similar interfaces, which
	// must never be mistaken for the device's uplink.
	Virtual bool `json:"virtual"`
}

// NetController is a network controller found on the bus. It is recorded
// separately from NetInterface because the whole point is that it has no
// interface.
type NetController struct {
	Slot     string `json:"slot"`
	VendorID string `json:"vendorId,omitempty"`
	DeviceID string `json:"deviceId,omitempty"`
	Modalias string `json:"modalias,omitempty"`
	// Description is whatever human-readable name the device exposed, if any.
	Description string `json:"description,omitempty"`
}

// RouteFacts summarises whether the device knows where to send a packet.
type RouteFacts struct {
	Observed       bool   `json:"observed"`
	DefaultGateway string `json:"defaultGateway,omitempty"`
	DefaultDevice  string `json:"defaultDevice,omitempty"`
}

// NetplanFacts describes the declared network configuration and whether it
// matches reality. A file naming an interface the kernel does not have is the
// single most common cause of a server that boots without a network.
type NetplanFacts struct {
	Observed bool     `json:"observed"`
	Present  bool     `json:"present"`
	Files    []string `json:"files,omitempty"`
	// Renderers are the renderers the files ask for. A file rendered by
	// NetworkManager on a server without it configures nothing.
	Renderers []string `json:"renderers,omitempty"`
	// ConfiguredInterfaces are the interface names the files name, before any
	// wildcard matching.
	ConfiguredInterfaces []string `json:"configuredInterfaces,omitempty"`
	// ParseError is set when the device's own tooling refused the files.
	ParseError string `json:"parseError,omitempty"`
	// RendererAvailable reports whether the renderer the files ask for is
	// actually installed and running.
	RendererAvailable *bool `json:"rendererAvailable,omitempty"`
}

// ResolverFacts describes name resolution.
type ResolverFacts struct {
	Observed bool `json:"observed"`
	// Servers are the nameservers currently in effect.
	Servers []string `json:"servers,omitempty"`
	// StubListener reports whether the systemd-resolved stub is holding the
	// loopback resolver port.
	StubListener *bool `json:"stubListener,omitempty"`
	// ResolvConfTarget is what /etc/resolv.conf points at, which distinguishes
	// a managed resolver from a hand-written file.
	ResolvConfTarget string `json:"resolvConfTarget,omitempty"`
}

// ReachProbe is one outbound attempt the device made on its own behalf.
type ReachProbe struct {
	// Endpoint is the host the probe tried, never a full URL with credentials.
	Endpoint string `json:"endpoint"`
	Result   Reach  `json:"result"`
	// Detail is a short, non-structural note for the operator. Nothing asserts
	// on it.
	Detail string `json:"detail,omitempty"`
}

// ProxyFacts records an already-configured egress path, which must be honoured
// rather than fought.
type ProxyFacts struct {
	Observed bool `json:"observed"`
	// Environment is true when a proxy is set for the login shell.
	Environment bool `json:"environment"`
	// APT is true when the package manager has its own proxy configuration.
	APT bool `json:"apt"`
}

// ClockFacts records time synchronisation, because a device far enough off
// cannot complete a TLS handshake and every symptom then looks like a network
// fault.
type ClockFacts struct {
	Observed bool `json:"observed"`
	// Synchronized is what the device believes about itself.
	Synchronized *bool `json:"synchronized,omitempty"`
	// SkewSeconds is the device clock minus the executor clock. It is the only
	// measurement that survives a device with no time source at all.
	SkewSeconds *float64 `json:"skewSeconds,omitempty"`
}

// PackageFacts describes the package manager's own state, which decides whether
// a fix that installs something can run at all.
type PackageFacts struct {
	Observed bool `json:"observed"`
	// Locked is true when another process holds the package manager, typically
	// unattended upgrades on a freshly installed device.
	Locked *bool `json:"locked,omitempty"`
	// SourcesConfigured is false when the device has no usable package
	// sources, which no amount of network repair will fix.
	SourcesConfigured *bool `json:"sourcesConfigured,omitempty"`
}

// CloudInitFacts reports first-boot provisioning, which can still be running
// and rewriting the very network configuration being repaired.
type CloudInitFacts struct {
	Observed bool   `json:"observed"`
	Present  bool   `json:"present"`
	Status   string `json:"status,omitempty"`
}

// HypervisorFacts reports whether this device is itself a hypervisor node. It
// is the branch point to substrate enrollment: such a device is not repaired
// into a StackKits host, it is registered as the place hosts come from.
type HypervisorFacts struct {
	Observed bool `json:"observed"`
	// Kind is empty when the device is not a hypervisor node.
	Kind    string `json:"kind,omitempty"`
	Version string `json:"version,omitempty"`
	// Bridges are the node's configured network bridges, which a guest needs.
	Bridges []string `json:"bridges,omitempty"`
}

// PrimaryInterfaces returns the interfaces that could carry the device's
// uplink, in probe order. Virtual interfaces are excluded: a device whose only
// interface is a bridge or a loopback has no uplink, and reporting one would
// turn a blocked check into a passing one.
func (f Facts) PrimaryInterfaces() []NetInterface {
	primary := make([]NetInterface, 0, len(f.Interface))
	for _, candidate := range f.Interface {
		if candidate.Virtual {
			continue
		}
		primary = append(primary, candidate)
	}
	return primary
}

// HasAddress reports whether any non-virtual interface carries an address.
func (f Facts) HasAddress() bool {
	for _, candidate := range f.PrimaryInterfaces() {
		if len(candidate.Addresses) > 0 {
			return true
		}
	}
	return false
}
