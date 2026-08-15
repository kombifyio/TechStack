package portinventory

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"
)

const (
	ErrorCodeAllocationConflict = "port_allocation_conflict"
	ReasonCodeHostPortReserved  = "host_port_reserved"
)

var (
	ErrInvalidRequirement      = errors.New("portinventory: invalid runtime listener requirement")
	ErrInvalidRequest          = errors.New("portinventory: invalid request")
	ErrAllocationConflict      = errors.New("portinventory: host port is already reserved")
	ErrInvalidTransition       = errors.New("portinventory: invalid claim transition")
	ErrStaleServerGeneration   = errors.New("portinventory: stale server generation")
	ErrClaimGenerationNotFound = errors.New("portinventory: claim generation not found")
)

// StaleServerGenerationError identifies a request fenced by the canonical
// server generation without exposing any cross-tenant inventory.
type StaleServerGenerationError struct {
	ServerID            string `json:"server_id"`
	RequestedGeneration int64  `json:"requested_generation"`
	ActualGeneration    int64  `json:"actual_generation,omitempty"`
}

func (e *StaleServerGenerationError) Error() string {
	return fmt.Sprintf("%s: server %s requested generation %d, actual generation %d", ErrStaleServerGeneration, e.ServerID, e.RequestedGeneration, e.ActualGeneration)
}

func (e *StaleServerGenerationError) Unwrap() error { return ErrStaleServerGeneration }

type Transport string

const (
	TransportTCP Transport = "tcp"
	TransportUDP Transport = "udp"
)

type Sharing string

const (
	SharingExclusive   Sharing = "exclusive"
	SharingVirtualHost Sharing = "virtual-host"
)

type Exposure string

const (
	ExposureLocal         Exposure = "local"
	ExposureRemotePrivate Exposure = "remote-private"
	ExposurePublic        Exposure = "public"
)

type ClaimState string

const (
	ClaimStatePending   ClaimState = "pending"
	ClaimStateMutating  ClaimState = "mutating"
	ClaimStateActive    ClaimState = "active"
	ClaimStateUncertain ClaimState = "uncertain"
	ClaimStateReleased  ClaimState = "released"
)

type ReservationState string

const (
	ReservationStateReserved ReservationState = "reserved"
	ReservationStateReleased ReservationState = "released"
)

type ServerRef struct {
	TenantID         string `json:"tenant_id"`
	ServerID         string `json:"server_id"`
	ServerGeneration int64  `json:"server_generation"`
}

type GenerationRef struct {
	ServerRef
	StackID          string `json:"stack_id"`
	ResolvedPlanHash string `json:"resolved_plan_hash"`
}

// Requirement is one compiler-declared, node-realized runtime listener. It is
// deliberately provider-free and must not be synthesized from URLs, routes,
// container ports, or provider inventory.
type Requirement struct {
	ID               string    `json:"id"`
	NodeRef          string    `json:"node_ref,omitempty"`
	Transport        Transport `json:"transport"`
	BindAddress      string    `json:"bind_address"`
	Port             uint16    `json:"port"`
	Sharing          Sharing   `json:"sharing"`
	ListenerGroupRef string    `json:"listener_group_ref,omitempty"`
	Exposure         Exposure  `json:"exposure"`
	SourceRouteRefs  []string  `json:"source_route_refs,omitempty"`
}

type AdmissionRequest struct {
	ServerRef
	StackID          string        `json:"stack_id"`
	ResolvedPlanHash string        `json:"resolved_plan_hash"`
	Requirements     []Requirement `json:"requirements"`
}

// CurrentAdmissionRequest deliberately omits ServerGeneration. Only the
// durable authority may resolve the canonical servers.generation used for
// conflict evaluation and admission.
type CurrentAdmissionRequest struct {
	TenantID         string        `json:"tenant_id"`
	ServerID         string        `json:"server_id"`
	OwnerSubjectID   string        `json:"owner_subject_id"`
	StackID          string        `json:"stack_id"`
	ResolvedPlanHash string        `json:"resolved_plan_hash"`
	Requirements     []Requirement `json:"requirements"`
}

type CurrentAdmission struct {
	GenerationRef GenerationRef `json:"generation_ref"`
	State         ClaimState    `json:"state"`
	Admission     Admission     `json:"admission"`
}

type Reservation struct {
	ID               string           `json:"id"`
	ServerRef        ServerRef        `json:"server"`
	Transport        Transport        `json:"transport"`
	BindAddress      string           `json:"bind_address"`
	Port             uint16           `json:"port"`
	Sharing          Sharing          `json:"sharing"`
	ListenerGroupRef string           `json:"listener_group_ref,omitempty"`
	State            ReservationState `json:"state"`
}

type Claim struct {
	ID               string      `json:"id"`
	ReservationID    string      `json:"reservation_id"`
	ServerRef        ServerRef   `json:"server"`
	StackID          string      `json:"stack_id"`
	ResolvedPlanHash string      `json:"resolved_plan_hash"`
	Requirement      Requirement `json:"requirement"`
	State            ClaimState  `json:"state"`
}

type ClaimGeneration struct {
	GenerationRef
	ClaimSetDigest string     `json:"claim_set_digest"`
	State          ClaimState `json:"state"`
}

type Admission struct {
	Claims []Claim `json:"claims"`
}

type Snapshot struct {
	ServerRef        ServerRef         `json:"server"`
	Reservations     []Reservation     `json:"reservations"`
	ClaimGenerations []ClaimGeneration `json:"claim_generations"`
	Claims           []Claim           `json:"claims"`
}

type UserGuidance struct {
	Title     string   `json:"title"`
	Body      string   `json:"body"`
	NextSteps []string `json:"next_steps"`
}

// ConflictError is safe to return across tenant boundaries.
type ConflictError struct {
	ErrorCode    string       `json:"error_code"`
	ReasonCode   string       `json:"reason_code"`
	Retryable    bool         `json:"retryable"`
	Transport    Transport    `json:"transport"`
	BindAddress  string       `json:"bind_address"`
	Port         uint16       `json:"port"`
	UserGuidance UserGuidance `json:"user_guidance"`
}

func (e *ConflictError) Error() string {
	return fmt.Sprintf("%s: %s %s:%d", ErrAllocationConflict, e.Transport, e.BindAddress, e.Port)
}

func (e *ConflictError) Unwrap() error { return ErrAllocationConflict }

func NormalizeRequirement(input Requirement) (Requirement, error) {
	input.ID = strings.TrimSpace(input.ID)
	input.NodeRef = strings.TrimSpace(input.NodeRef)
	input.ListenerGroupRef = strings.TrimSpace(input.ListenerGroupRef)
	input.Transport = Transport(strings.ToLower(strings.TrimSpace(string(input.Transport))))
	input.Sharing = Sharing(strings.ToLower(strings.TrimSpace(string(input.Sharing))))
	input.Exposure = Exposure(strings.ToLower(strings.TrimSpace(string(input.Exposure))))
	if input.ID == "" || input.Port == 0 {
		return Requirement{}, ErrInvalidRequirement
	}
	if input.Transport != TransportTCP && input.Transport != TransportUDP {
		return Requirement{}, ErrInvalidRequirement
	}
	if input.Sharing != SharingExclusive && input.Sharing != SharingVirtualHost {
		return Requirement{}, ErrInvalidRequirement
	}
	if input.Exposure != ExposureLocal && input.Exposure != ExposureRemotePrivate && input.Exposure != ExposurePublic {
		return Requirement{}, ErrInvalidRequirement
	}
	if input.Sharing == SharingVirtualHost && input.ListenerGroupRef == "" {
		return Requirement{}, ErrInvalidRequirement
	}
	if input.Sharing == SharingExclusive && input.ListenerGroupRef != "" {
		return Requirement{}, ErrInvalidRequirement
	}

	address, err := normalizeBindAddress(input.BindAddress)
	if err != nil {
		return Requirement{}, err
	}
	input.BindAddress = address
	input.SourceRouteRefs = normalizeRefs(input.SourceRouteRefs)
	return input, nil
}

func normalizeBindAddress(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "*" || value == "0.0.0.0" || value == "::" {
		return "*", nil
	}
	address, err := netip.ParseAddr(value)
	if err != nil {
		return "", ErrInvalidRequirement
	}
	return address.Unmap().String(), nil
}

func normalizeRefs(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
