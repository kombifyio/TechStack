package portinventory

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"
	"time"
)

const portObservationTTL = 90 * time.Second

type FactKind string

const (
	FactKindObserved FactKind = "observed"
	FactKindExposed  FactKind = "exposed"
)

type RuntimeFact struct {
	Kind        FactKind  `json:"kind"`
	Transport   Transport `json:"transport"`
	BindAddress string    `json:"bind_address"`
	Port        uint16    `json:"port"`
	Exposure    Exposure  `json:"exposure"`
}

type Observation struct {
	ServerRef
	SourceEpoch       string        `json:"source_epoch"`
	SourceSequence    int64         `json:"source_sequence"`
	InventoryRevision int64         `json:"inventory_revision"`
	ListenersComplete bool          `json:"listeners_complete"`
	ExposuresComplete bool          `json:"exposures_complete"`
	ObservedAt        time.Time     `json:"observed_at"`
	ExpiresAt         time.Time     `json:"expires_at"`
	Facts             []RuntimeFact `json:"facts"`
}

type GuardObservation struct {
	TenantID          string
	RuntimeAgentID    string
	SourceEpoch       string
	SourceSequence    int64
	InventoryRevision int64
	ObservedAt        time.Time
	ListenersComplete bool
	OpenPorts         []string
}

type InventoryRequest struct {
	TenantID       string `json:"tenant_id"`
	ServerID       string `json:"server_id"`
	OwnerSubjectID string `json:"owner_subject_id,omitempty"`
}

type EvidenceState string

const (
	EvidenceUnknown EvidenceState = "unknown"
	EvidencePresent EvidenceState = "present"
	EvidenceMissing EvidenceState = "missing"
	EvidenceStale   EvidenceState = "stale"
)

type DriftState string

const (
	DriftConsistent DriftState = "consistent"
	DriftMissing    DriftState = "missing"
	DriftUnexpected DriftState = "unexpected"
	DriftUnknown    DriftState = "unknown"
)

type Allocation struct {
	ID               string           `json:"id"`
	StackID          string           `json:"kit_deployment_id,omitempty"`
	ResolvedPlanHash string           `json:"resolved_plan_hash,omitempty"`
	NodeRef          string           `json:"node_ref,omitempty"`
	Transport        Transport        `json:"transport"`
	BindAddress      string           `json:"bind_address"`
	Port             uint16           `json:"port"`
	Sharing          Sharing          `json:"sharing,omitempty"`
	ListenerGroupRef string           `json:"listener_group_ref,omitempty"`
	Exposure         Exposure         `json:"exposure"`
	SourceRouteRefs  []string         `json:"source_route_refs,omitempty"`
	ReservationState ReservationState `json:"reservation_state,omitempty"`
	ClaimState       ClaimState       `json:"claim_state,omitempty"`
	ObservedState    EvidenceState    `json:"observed_state"`
	ExposedState     EvidenceState    `json:"exposed_state"`
	DriftState       DriftState       `json:"drift_state"`
	Desired          bool             `json:"desired"`
}

type Inventory struct {
	TenantID          string       `json:"-"`
	ServerID          string       `json:"server_id"`
	ServerGeneration  int64        `json:"server_generation"`
	ObservedAt        *time.Time   `json:"observed_at,omitempty"`
	ExpiresAt         *time.Time   `json:"expires_at,omitempty"`
	InventoryRevision int64        `json:"inventory_revision"`
	ListenersComplete bool         `json:"listeners_complete"`
	ExposuresComplete bool         `json:"exposures_complete"`
	Allocations       []Allocation `json:"allocations"`
}

// ObservationWriter is the authenticated Guard inventory boundary. Implementations
// resolve the canonical server generation from the tenant-bound Agent identity;
// callers never supply a server or generation.
type ObservationWriter interface {
	RecordGuardPorts(context.Context, GuardObservation) error
}

type ReadAuthority interface {
	ReadCurrent(context.Context, InventoryRequest, time.Time) (Inventory, error)
}

func parseAgentPortFacts(values []string) ([]RuntimeFact, bool) {
	facts := make([]RuntimeFact, 0, len(values))
	complete := true
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		fact, err := parseAgentPortFact(value)
		if err != nil {
			complete = false
			continue
		}
		key := fmt.Sprintf("%s\x00%s\x00%d", fact.Transport, fact.BindAddress, fact.Port)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		facts = append(facts, fact)
	}
	sort.Slice(facts, func(i, j int) bool {
		if facts[i].Port != facts[j].Port {
			return facts[i].Port < facts[j].Port
		}
		if facts[i].Transport != facts[j].Transport {
			return facts[i].Transport < facts[j].Transport
		}
		return facts[i].BindAddress < facts[j].BindAddress
	})
	return facts, complete
}

func parseAgentPortFact(value string) (RuntimeFact, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	transport := TransportTCP
	for _, scheme := range []struct {
		prefix string
		value  Transport
	}{{"tcp://", TransportTCP}, {"udp://", TransportUDP}} {
		if strings.HasPrefix(value, scheme.prefix) {
			transport = scheme.value
			value = strings.TrimPrefix(value, scheme.prefix)
		}
	}
	if slash := strings.LastIndexByte(value, '/'); slash >= 0 {
		transport = Transport(strings.TrimSpace(value[slash+1:]))
		value = strings.TrimSpace(value[:slash])
	}
	if transport != TransportTCP && transport != TransportUDP {
		return RuntimeFact{}, ErrInvalidRequirement
	}

	address, portText := "*", value
	if strings.Contains(value, ":") {
		host, port, err := net.SplitHostPort(value)
		if err != nil && strings.Count(value, ":") == 1 {
			host, port, err = net.SplitHostPort("[" + strings.SplitN(value, ":", 2)[0] + "]:" + strings.SplitN(value, ":", 2)[1])
		}
		if err != nil {
			return RuntimeFact{}, ErrInvalidRequirement
		}
		address, portText = host, port
	}
	port, err := strconv.ParseUint(portText, 10, 16)
	if err != nil || port == 0 {
		return RuntimeFact{}, ErrInvalidRequirement
	}
	address, err = normalizeBindAddress(address)
	if err != nil {
		return RuntimeFact{}, err
	}
	return RuntimeFact{
		Kind: FactKindObserved, Transport: transport, BindAddress: address,
		Port: uint16(port), Exposure: exposureForBindAddress(address),
	}, nil
}

func exposureForBindAddress(value string) Exposure {
	if value == "*" {
		return ExposurePublic
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return ExposurePublic
	}
	if address.IsLoopback() {
		return ExposureLocal
	}
	if address.IsPrivate() || address.IsLinkLocalUnicast() {
		return ExposureRemotePrivate
	}
	return ExposurePublic
}
