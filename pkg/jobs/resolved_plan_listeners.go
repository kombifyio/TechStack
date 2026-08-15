package jobs

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"sort"
	"strings"
)

const (
	resolvedPlanAPIVersionV1           = "stackkit.resolved-plan/v1"
	maxResolvedPlanListenerDocumentLen = 32 << 20
)

var resolvedPlanHashPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// resolvedPlanRuntimeListener mirrors only the compiler-owned listener
// contract. TargetPort and ownership refs are validated but are deliberately
// not candidates for host-port inference: only BindAddress and Port describe
// the host reservation.
type resolvedPlanRuntimeListener struct {
	ID               string   `json:"id"`
	ModuleRef        string   `json:"moduleRef"`
	UnitRef          string   `json:"unitRef"`
	InstanceRef      string   `json:"instanceRef"`
	NodeRef          string   `json:"nodeRef"`
	ComponentRef     string   `json:"componentRef"`
	Transport        string   `json:"transport"`
	BindAddress      string   `json:"bindAddress"`
	Port             uint16   `json:"port"`
	TargetPort       uint16   `json:"targetPort"`
	Sharing          string   `json:"sharing"`
	ListenerGroupRef string   `json:"listenerGroupRef,omitempty"`
	SourceRouteRefs  []string `json:"sourceRouteRefs"`
	Exposure         string   `json:"exposure"`
}

type resolvedPlanListenerSet struct {
	StackKitInstanceID string
	PlanHash           string
	NodeRef            string
	Listeners          []resolvedPlanRuntimeListener
}

// parseResolvedPlanListenerSet consumes only network.runtimeListeners from the
// canonical ResolvedPlan. It never falls back to routes, targetPort, URLs,
// generated artifacts, provider data, or legacy OpenPorts.
//
// Techstack's current port projection is single-node because DeployHandler
// dispatches one runtime target. Unsupported and multi-node listener shapes
// remain valid StackKits plans; callers can continue without this optional
// projection after binding the plan hash.
func parseResolvedPlanListenerSet(document []byte) (resolvedPlanListenerSet, error) {
	if len(document) == 0 || len(document) > maxResolvedPlanListenerDocumentLen {
		return resolvedPlanListenerSet{}, errors.New("runtime listener admission requires a bounded canonical ResolvedPlan")
	}
	var envelope struct {
		APIVersion string          `json:"apiVersion"`
		Kind       string          `json:"kind"`
		StackID    string          `json:"stackId"`
		PlanHash   string          `json:"planHash"`
		Network    json.RawMessage `json:"network"`
	}
	if err := json.Unmarshal(document, &envelope); err != nil {
		return resolvedPlanListenerSet{}, fmt.Errorf("decode canonical ResolvedPlan listener envelope: %w", err)
	}
	envelope.APIVersion = strings.TrimSpace(envelope.APIVersion)
	envelope.Kind = strings.TrimSpace(envelope.Kind)
	envelope.StackID = strings.TrimSpace(envelope.StackID)
	envelope.PlanHash = strings.TrimSpace(envelope.PlanHash)
	if envelope.StackID == "" || !resolvedPlanHashPattern.MatchString(envelope.PlanHash) {
		return resolvedPlanListenerSet{}, errors.New("canonical ResolvedPlan identity is incomplete")
	}
	identity := resolvedPlanListenerSet{StackKitInstanceID: envelope.StackID, PlanHash: envelope.PlanHash}
	if envelope.APIVersion != resolvedPlanAPIVersionV1 || envelope.Kind != "ResolvedPlan" {
		return identity, errors.New("Techstack does not project this canonical ResolvedPlan contract version")
	}
	if len(envelope.Network) == 0 || bytes.Equal(bytes.TrimSpace(envelope.Network), []byte("null")) {
		return identity, errors.New("canonical ResolvedPlan has no network.runtimeListeners authority")
	}
	var network struct {
		RuntimeListeners json.RawMessage `json:"runtimeListeners"`
	}
	if err := json.Unmarshal(envelope.Network, &network); err != nil {
		return identity, fmt.Errorf("decode canonical ResolvedPlan network: %w", err)
	}
	if len(network.RuntimeListeners) == 0 || bytes.Equal(bytes.TrimSpace(network.RuntimeListeners), []byte("null")) {
		return identity, errors.New("canonical ResolvedPlan has no network.runtimeListeners authority")
	}

	decoder := json.NewDecoder(bytes.NewReader(network.RuntimeListeners))
	decoder.DisallowUnknownFields()
	var listeners []resolvedPlanRuntimeListener
	if err := decoder.Decode(&listeners); err != nil {
		return identity, fmt.Errorf("decode canonical ResolvedPlan network.runtimeListeners: %w", err)
	}
	if listeners == nil {
		return identity, errors.New("canonical ResolvedPlan network.runtimeListeners must be an array")
	}
	if err := requireJSONEOF(decoder); err != nil {
		return identity, err
	}

	seenIDs := make(map[string]struct{}, len(listeners))
	nodeRef := ""
	for index := range listeners {
		listener := &listeners[index]
		if err := normalizeResolvedPlanRuntimeListener(listener); err != nil {
			return identity, fmt.Errorf("network.runtimeListeners[%d]: %w", index, err)
		}
		if _, exists := seenIDs[listener.ID]; exists {
			return identity, fmt.Errorf("network.runtimeListeners[%d]: duplicate listener id %q", index, listener.ID)
		}
		seenIDs[listener.ID] = struct{}{}
		if nodeRef == "" {
			nodeRef = listener.NodeRef
		} else if listener.NodeRef != nodeRef {
			return identity, errors.New("multi-node runtime listener admission is not supported by the single-target rollout slice")
		}
	}
	sort.Slice(listeners, func(i, j int) bool { return listeners[i].ID < listeners[j].ID })
	identity.NodeRef = nodeRef
	identity.Listeners = listeners
	return identity, nil
}

func normalizeResolvedPlanRuntimeListener(listener *resolvedPlanRuntimeListener) error {
	listener.ID = strings.TrimSpace(listener.ID)
	listener.ModuleRef = strings.TrimSpace(listener.ModuleRef)
	listener.UnitRef = strings.TrimSpace(listener.UnitRef)
	listener.InstanceRef = strings.TrimSpace(listener.InstanceRef)
	listener.NodeRef = strings.TrimSpace(listener.NodeRef)
	listener.ComponentRef = strings.TrimSpace(listener.ComponentRef)
	listener.Transport = strings.ToLower(strings.TrimSpace(listener.Transport))
	listener.BindAddress = strings.TrimSpace(listener.BindAddress)
	listener.Sharing = strings.ToLower(strings.TrimSpace(listener.Sharing))
	listener.ListenerGroupRef = strings.TrimSpace(listener.ListenerGroupRef)
	listener.Exposure = strings.ToLower(strings.TrimSpace(listener.Exposure))
	if listener.ID == "" || listener.ModuleRef == "" || listener.UnitRef == "" || listener.InstanceRef == "" ||
		listener.NodeRef == "" || listener.ComponentRef == "" || listener.Port == 0 || listener.TargetPort == 0 {
		return errors.New("listener ownership and both port fields are required")
	}
	address, err := netip.ParseAddr(listener.BindAddress)
	if err != nil {
		return errors.New("bindAddress must be a concrete IPv4 or IPv6 address")
	}
	listener.BindAddress = address.Unmap().String()
	if listener.Transport != "tcp" && listener.Transport != "udp" {
		return errors.New("transport must be tcp or udp")
	}
	if listener.Sharing != "exclusive" && listener.Sharing != "virtual-host" {
		return errors.New("sharing must be exclusive or virtual-host")
	}
	if (listener.Sharing == "exclusive" && listener.ListenerGroupRef != "") ||
		(listener.Sharing == "virtual-host" && listener.ListenerGroupRef == "") {
		return errors.New("listenerGroupRef does not match sharing")
	}
	if listener.Exposure != "local" && listener.Exposure != "remote-private" && listener.Exposure != "public" {
		return errors.New("exposure must be local, remote-private, or public")
	}
	if listener.SourceRouteRefs == nil {
		return errors.New("sourceRouteRefs is required")
	}
	listener.SourceRouteRefs = normalizeResolvedPlanRefs(listener.SourceRouteRefs)
	return nil
}

func normalizeResolvedPlanRefs(values []string) []string {
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

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("canonical ResolvedPlan runtimeListeners contains trailing JSON")
		}
		return fmt.Errorf("decode canonical ResolvedPlan runtimeListeners trailer: %w", err)
	}
	return nil
}
