package routes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/runtimeproduct/serverruntime"
	"github.com/kombifyio/techstack/internal/runtimeproduct/vmlease"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/identity"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
	"github.com/kombifyio/techstack/pkg/runtimeidentity"
	"github.com/kombifyio/techstack/pkg/vmleases"
)

const (
	managedRuntimeInventorySource = "managed-runtime"
	workerRegistryInventorySource = "worker-registry"
	managedRuntimeAssignStack     = "stack"
	managedRuntimeOSLinux         = "linux"
	managedRuntimeArchAMD64       = "amd64"
	managedRuntimeStatusApproved  = "approved"
	managedRuntimeStatusRejected  = "rejected"
	managedRuntimeNodeFoundation  = "foundation"
	managedRuntimePreCheckManaged = "managed"
	managedRuntimeLeaseIDPrefix   = "lease:"
	managedRuntimeLeaseNamePrefix = "lease-"

	workerFieldHostname      = "hostname"
	workerFieldCPU           = "cpu_cores"
	workerFieldRAM           = "ram_mb"
	workerFieldDisk          = "disk_gb"
	workerFieldProvider      = "provider"
	workerFieldServerID      = "server_id"
	workerFieldCreated       = "created"
	workerFieldDockerVersion = "docker_version"
)

type managedRuntimeLeaseLister interface {
	ListByTenant(ctx context.Context, tenantID string) ([]vmlease.Lease, error)
}

type managedRuntimeLeaseInventoryLister interface {
	ListInventoryByTenant(ctx context.Context, tenantID string) ([]vmleases.LeaseInventoryRecord, error)
}

type managedRuntimeInventoryItem struct {
	ID                 string
	LeaseID            string
	Hostname           string
	Role               string
	Status             string
	StackID            string
	Provider           string
	RuntimeLane        string
	RuntimeOffering    string
	DesiredState       string
	EnrollmentStatus   string
	RuntimeHost        string
	RuntimeSSHUser     string
	RuntimeSSHPort     int
	PublicIP           string
	PrivateIP          string
	IP                 string
	Region             string
	Image              string
	VCPUs              int
	MemoryMB           int
	DiskGB             int
	CPUPercent         stackMetricValue
	MemoryPercent      stackMetricValue
	DiskPercent        stackMetricValue
	UptimeSeconds      stackMetricValue
	Approved           bool
	ExecutionAuthority string
	AuthorityState     string
	NativeActive       bool
	UpdatedAt          string
	Cancelled          bool
	CustodyResolved    bool
	ObservedState      string
}

// Absence reasons explain why a lease is a custody record rather than a
// machine. They are wire-visible so an operator can tell an expired lease from
// one whose VM was deleted at the provider.
const (
	managedRuntimeAbsenceCancelled     = "lease_cancelled"
	managedRuntimeAbsenceArchived      = "lease_archived"
	managedRuntimeAbsenceProviderGone  = "provider_reports_absent"
	managedRuntimeAbsenceEnrollFailed  = "enrollment_failed"
	managedRuntimeAbsenceNoCustody     = "no_execution_authority"
	managedRuntimeAbsenceNeverObserved = "never_observed"
)

// managedRuntimeLeaseMachineEvidence decides whether a lease may be presented
// as a server. A lease is a billing and custody record; it is not by itself
// evidence that a machine exists. Rendering every lease as a server produced
// cards for VMs that had been deleted at the provider, complete with a stale
// address and action buttons.
//
// A lease counts as a machine only with positive evidence: either the control
// plane holds a live canonical server row bound to it, or the lease is under
// native custody, wants to run, and nothing has observed it gone. Everything
// else is reported as a custody record instead, so the exposure stays visible
// without claiming to be infrastructure.
func managedRuntimeLeaseMachineEvidence(item managedRuntimeInventoryItem, hasCanonicalServer bool) (bool, string) {
	if hasCanonicalServer {
		return true, ""
	}
	switch {
	case item.ObservedState == managedRuntimeObservedStateNotFound:
		return false, managedRuntimeAbsenceProviderGone
	case item.Cancelled:
		return false, managedRuntimeAbsenceCancelled
	case strings.EqualFold(item.DesiredState, string(vmlease.DesiredStateArchived)):
		return false, managedRuntimeAbsenceArchived
	case item.EnrollmentStatus == monthlyRuntimeEnrollmentStatusFailed:
		return false, managedRuntimeAbsenceEnrollFailed
	case !item.NativeActive:
		// Legacy-quarantined, unbound, inactive: no custody path can act on it
		// and no agent ever enrolled, so nothing backs a server claim.
		return false, managedRuntimeAbsenceNoCustody
	default:
		return true, ""
	}
}

// managedRuntimeObservedStateNotFound mirrors the value the monthly-runtime
// lifecycle writes into lease metadata once a provider read reports the VM
// gone (pkg/monthlyruntime: runtimeObservedStateNotFound).
const managedRuntimeObservedStateNotFound = "not_found"

func requestTenantID(e *httpx.Event, fallback string) string {
	if tenantID := requestExplicitTenantID(e); tenantID != "" {
		return tenantID
	}
	return strings.TrimSpace(fallback)
}

func requestExplicitTenantID(e *httpx.Event) string {
	if e != nil && e.Request != nil {
		if id := identity.FromContext(e.Request.Context()); id != nil && strings.TrimSpace(id.OrgID) != "" {
			return strings.TrimSpace(id.OrgID)
		}
	}
	if e != nil && e.Auth != nil {
		if orgID := strings.TrimSpace(e.Auth.GetString("org_id")); orgID != "" {
			return orgID
		}
	}
	return ""
}

func projectManagedRuntimeLeasesChecked(ctx context.Context, lister managedRuntimeLeaseLister, tenantID, ownerID, stackID string) ([]managedRuntimeInventoryItem, error) {
	records, err := listManagedRuntimeLeasesChecked(ctx, lister, tenantID, ownerID)
	if err != nil {
		return nil, err
	}
	return projectManagedRuntimeLeaseSnapshot(records, stackID), nil
}

func listManagedRuntimeLeasesChecked(ctx context.Context, lister managedRuntimeLeaseLister, tenantID, ownerID string) ([]vmleases.LeaseInventoryRecord, error) {
	records, err := listTenantManagedRuntimeLeasesChecked(ctx, lister, tenantID)
	if err != nil {
		return nil, err
	}
	return visibleManagedRuntimeLeases(records, tenantID, ownerID), nil
}

// listTenantManagedRuntimeLeasesChecked returns the complete tenant-scoped
// monthly-runtime authority snapshot. Safety decisions must use this snapshot
// before applying caller visibility; filtering first can hide a live lease
// that still protects a shared stack from destructive cleanup.
func listTenantManagedRuntimeLeasesChecked(ctx context.Context, lister managedRuntimeLeaseLister, tenantID string) ([]vmleases.LeaseInventoryRecord, error) {
	tenantID = strings.TrimSpace(tenantID)
	if lister == nil || tenantID == "" {
		return nil, vmleases.ErrLeaseInventoryUnavailable
	}
	inventory, ok := lister.(managedRuntimeLeaseInventoryLister)
	if !ok {
		return nil, vmleases.ErrLeaseInventoryUnavailable
	}
	records, err := inventory.ListInventoryByTenant(ctx, tenantID)
	if err != nil {
		return nil, err
	}
	items := make([]vmleases.LeaseInventoryRecord, 0, len(records))
	for _, record := range records {
		lease := record.Lease
		if strings.TrimSpace(lease.Subject.OrgID) != tenantID {
			continue
		}
		if !monthlyruntime.IsMonthlyRuntimeMetadata(lease.Metadata) {
			continue
		}
		items = append(items, record)
	}
	return items, nil
}

func visibleManagedRuntimeLeases(records []vmleases.LeaseInventoryRecord, tenantID, ownerID string) []vmleases.LeaseInventoryRecord {
	tenantID = strings.TrimSpace(tenantID)
	ownerID = strings.TrimSpace(ownerID)
	if tenantID == "" || ownerID == "" {
		return nil
	}
	visible := make([]vmleases.LeaseInventoryRecord, 0, len(records))
	for _, record := range records {
		if managedRuntimeLeaseVisibleToOwner(record.Lease, tenantID, ownerID) {
			visible = append(visible, record)
		}
	}
	return visible
}

func projectManagedRuntimeLeaseSnapshot(records []vmleases.LeaseInventoryRecord, stackID string) []managedRuntimeInventoryItem {
	stackID = strings.TrimSpace(stackID)
	items := make([]managedRuntimeInventoryItem, 0, len(records))
	for _, record := range records {
		item := managedRuntimeInventoryItemFromLease(record)
		if stackID != "" && item.StackID != stackID {
			continue
		}
		items = append(items, item)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if items[i].StackID != items[j].StackID {
			return items[i].StackID < items[j].StackID
		}
		return strings.ToLower(items[i].Hostname) < strings.ToLower(items[j].Hostname)
	})
	return items
}

func managedRuntimeLeaseActive(lease vmlease.Lease) bool {
	return monthlyruntime.LeaseActive(lease)
}

func managedRuntimeLeaseVisibleToOwner(lease vmlease.Lease, tenantID, ownerID string) bool {
	return monthlyruntime.LeaseVisibleToOwner(lease, tenantID, ownerID)
}

func managedRuntimeInventoryItemFromLease(record vmleases.LeaseInventoryRecord) managedRuntimeInventoryItem {
	lease := record.Lease
	metadata := lease.Metadata
	offeringID := monthlyruntime.OfferingIDFromMetadata(metadata)
	offering, _ := monthlyruntime.OfferingByID(offeringID)
	enrollmentStatus := strings.TrimSpace(metadata["runtime_enrollment_status"])
	if enrollmentStatus == "" {
		enrollmentStatus = monthlyRuntimeEnrollmentStatusPending
	}
	hostname := managedRuntimeNodeHostname(metadata, lease.ID)
	runtimeHost := firstNonEmptyString(metadata["runtime_ssh_host"], metadata["runtime_host"])
	publicIP := firstNonEmptyString(metadata["runtime_public_ip"], metadata["node_public_ip"], metadata["public_ip"])
	privateIP := firstNonEmptyString(metadata["runtime_private_ip"], metadata["node_private_ip"], metadata["private_ip"])
	runtimeTarget := firstNonEmptyString(runtimeHost, publicIP, privateIP)
	targetReady := record.NativeActive() && enrollmentStatus == monthlyRuntimeEnrollmentStatusEnrolled && managedRuntimeTargetReady(lease, runtimeTarget)
	status := managedRuntimeAuthorityStatus(record)
	if record.NativeActive() {
		status = managedRuntimeHealthState(lease, enrollmentStatus, runtimeTarget, managedRuntimeEnrollmentAge(metadata))
	}
	return managedRuntimeInventoryItem{
		ID:                 managedRuntimeServerID(lease.ID),
		LeaseID:            string(lease.ID),
		Hostname:           hostname,
		Role:               firstNonEmptyString(metadata["role"], managedRuntimeNodeFoundation),
		Status:             status,
		StackID:            strings.TrimSpace(metadata["stack_id"]),
		Provider:           firstNonEmptyString(lease.Resource.ProviderID, metadata["lease_provider"], metadata["simulate_provider_id"]),
		RuntimeLane:        firstNonEmptyString(metadata["runtime_lane"], serverruntime.RuntimeLaneMonthly),
		RuntimeOffering:    string(offeringID),
		DesiredState:       string(lease.DesiredState),
		EnrollmentStatus:   enrollmentStatus,
		RuntimeHost:        runtimeHost,
		RuntimeSSHUser:     strings.TrimSpace(metadata["runtime_ssh_user"]),
		RuntimeSSHPort:     managedRuntimeIntFromMetadata(metadata, "runtime_ssh_port"),
		PublicIP:           publicIP,
		PrivateIP:          privateIP,
		IP:                 firstNonEmptyString(publicIP, privateIP),
		Region:             firstNonEmptyString(lease.Resource.Region, offering.Region),
		Image:              offering.Image,
		VCPUs:              managedRuntimeKnownInt(offering.VCPUs, targetReady),
		MemoryMB:           managedRuntimeKnownInt(offering.MemoryMB, targetReady),
		DiskGB:             managedRuntimeKnownInt(offering.DiskGB, targetReady),
		CPUPercent:         managedRuntimeMetricFromMetadata(metadata, "%", "runtime_cpu_percent", "cpu_percent"),
		MemoryPercent:      managedRuntimeMetricFromMetadata(metadata, "%", "runtime_memory_percent", "memory_percent"),
		DiskPercent:        managedRuntimeMetricFromMetadata(metadata, "%", "runtime_disk_percent", "disk_percent"),
		UptimeSeconds:      managedRuntimeMetricFromMetadata(metadata, "s", "runtime_uptime_seconds", "uptime_seconds"),
		Approved:           targetReady,
		ExecutionAuthority: string(record.ExecutionAuthority),
		AuthorityState:     string(record.AuthorityState),
		NativeActive:       record.NativeActive(),
		UpdatedAt:          managedRuntimeUpdatedAt(lease),
		Cancelled:          lease.CancelledAt != nil,
		CustodyResolved:    strings.EqualFold(strings.TrimSpace(metadata["custody_resolution_status"]), "resolved"),
		ObservedState:      strings.TrimSpace(metadata["runtime_observed_state"]),
	}
}

func managedRuntimeAuthorityStatus(record vmleases.LeaseInventoryRecord) string {
	switch record.AuthorityState {
	case vmleases.LeaseAuthorityStateLegacyQuarantined:
		return managedRuntimeStatusQuarantined
	case vmleases.LeaseAuthorityStateUnbound:
		return "unbound"
	case vmleases.LeaseAuthorityStateNativeInactive:
		return "inactive"
	case vmleases.LeaseAuthorityStateNativeActive:
		return monthlyRuntimeEnrollmentStatusPending
	default:
		return "authority-unknown"
	}
}

func managedRuntimeServerID(id vmlease.LeaseID) string {
	return managedRuntimeLeaseIDPrefix + string(id)
}

// reservedPlatformNodeNames are names a single node/server must never carry:
// "TechStack" is the platform's name for the WHOLE homelab (all nodes + all
// rolled-out StackKits of a user), so a single node bearing it is a dangerous
// collision.
var reservedPlatformNodeNames = map[string]bool{
	"techstack":         true,
	"kombify":           true,
	"kombify-techstack": true,
	"kombifytechstack":  true,
}

// IsReservedPlatformNodeName reports whether name collides with the platform
// brand/nomenclature and therefore must not be used for a single node/stack.
func IsReservedPlatformNodeName(name string) bool {
	normalized := normalizeReservedPlatformNodeName(name)
	return reservedPlatformNodeNames[normalized] ||
		strings.HasPrefix(normalized, "techstack-") ||
		strings.HasPrefix(normalized, "kombify-techstack-") ||
		strings.HasPrefix(normalized, "kombifytechstack-")
}

func normalizeReservedPlatformNodeName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		default:
			if !lastDash {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}

// managedRuntimeNodeHostname derives a node hostname that is ALWAYS distinct
// from the stack/platform name and never a bare reserved brand name. It prefers
// an explicit reported hostname (from a paired agent), then a stack-scoped
// "<stack>-<role>-<shortid>", then a neutral generated name.
func managedRuntimeNodeHostname(metadata map[string]string, leaseID vmlease.LeaseID) string {
	if reported := strings.TrimSpace(metadata[workerFieldHostname]); reported != "" && !IsReservedPlatformNodeName(reported) {
		return reported
	}
	role := firstNonEmptyString(strings.TrimSpace(metadata["role"]), managedRuntimeNodeFoundation)
	shortID := shortManagedLeaseID(leaseID)
	if stackName := strings.TrimSpace(metadata["stack_name"]); stackName != "" && !IsReservedPlatformNodeName(stackName) {
		return stackName + "-" + role + "-" + shortID
	}
	return "node-" + role + "-" + shortID
}

// shortManagedLeaseID derives a collision-free discriminator from the whole
// lease id. Truncating to the first characters collapsed sibling leases of one
// stack onto a single name, because every lease of a stack shares the stack id
// prefix.
func shortManagedLeaseID(leaseID vmlease.LeaseID) string {
	id := strings.TrimPrefix(string(leaseID), managedRuntimeLeaseNamePrefix)
	id = strings.TrimPrefix(id, "lease-")
	if id == "" {
		return "node"
	}
	sum := sha256.Sum256([]byte(id))
	return hex.EncodeToString(sum[:3])
}

// managedRuntimeStatusProvisioned marks a managed node that is enrolled and
// reachable but has no reporting agent yet — a lease-state projection, not a
// measured-health read. Real "healthy" comes only once the runtime worker
// reports telemetry (see applyManagedRuntimeMetrics).
const (
	managedRuntimeStatusProvisioned = "provisioned"
	managedRuntimeStatusQuarantined = "quarantined"
)

// managedRuntimeStatusStalled marks a managed node whose enrollment has been
// pending past monthlyruntime.EnrollmentStalledThreshold; the owner is guided to
// reconnect. It stays dormant (age 0 -> never stalled) until the write side
// stamps enrollment_started_at.
const managedRuntimeStatusStalled = "stalled"

// managedRuntimeStatusOffline marks an enrolled node whose desired state is
// stopped/archived — intentionally offline rather than unhealthy.
const managedRuntimeStatusOffline = "offline"

// managedRuntimeHealthState projects a managed node's dashboard status from the
// shared lifecycle state machine (monthlyruntime.DeriveState) — the single
// source of truth both this projection and the enrollment write side reconcile
// against. Telemetry-based promotion to "healthy" stays in the overlay
// (applyManagedRuntimeMetrics); this is the honest lease-state projection.
func managedRuntimeHealthState(lease vmlease.Lease, enrollmentStatus, runtimeTarget string, enrollmentAge time.Duration) string {
	desiredStopped := lease.DesiredState == vmlease.DesiredStateStopped || lease.DesiredState == vmlease.DesiredStateArchived
	state := monthlyruntime.DeriveState(monthlyruntime.DeriveInputs{
		EnrollmentStatus: enrollmentStatus,
		DesiredStopped:   desiredStopped,
		HasRuntimeTarget: strings.TrimSpace(runtimeTarget) != "",
		HasTelemetry:     false, // telemetry promotion happens in the metrics overlay
		EnrollmentAge:    enrollmentAge,
	})
	return managedRuntimeProjectionStatus(state, desiredStopped)
}

// managedRuntimeProjectionStatus maps a lifecycle State to the dashboard status
// vocabulary, preserving the historical strings (provisioned/offline/stale/error/
// pending) and adding "stalled".
func managedRuntimeProjectionStatus(state monthlyruntime.State, desiredStopped bool) string {
	switch state {
	case monthlyruntime.StateFailed:
		return routeErrorField
	case monthlyruntime.StateStalled:
		return managedRuntimeStatusStalled
	case monthlyruntime.StateStale, monthlyruntime.StateDegraded:
		return "stale"
	case monthlyruntime.StateProvisioned, monthlyruntime.StateConnected:
		if desiredStopped {
			return managedRuntimeStatusOffline
		}
		return managedRuntimeStatusProvisioned
	default: // requested / provisioning / enrolled_pending_agent / decommissioning / decommissioned
		return monthlyRuntimeEnrollmentStatusPending
	}
}

// managedRuntimeEnrollmentAge returns how long the runtime has been enrolling,
// measured from the enrollment_started_at metadata stamp (NOT RenewedAt, which
// resets on every lease patch). Zero when unstamped, so stalled detection is
// fail-safe until the write side records the start time.
func managedRuntimeEnrollmentAge(metadata map[string]string) time.Duration {
	raw := strings.TrimSpace(metadata[monthlyruntime.EnrollmentStartedAtKey])
	if raw == "" {
		return 0
	}
	started, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return 0
	}
	if age := time.Since(started); age > 0 {
		return age
	}
	return 0
}

func managedRuntimeTargetReady(lease vmlease.Lease, runtimeTarget string) bool {
	if strings.TrimSpace(runtimeTarget) == "" {
		return false
	}
	if lease.DesiredState == vmlease.DesiredStateStopped || lease.DesiredState == vmlease.DesiredStateArchived {
		return false
	}
	return true
}

func managedRuntimeKnownInt(value int, known bool) int {
	if !known {
		return 0
	}
	return value
}

func managedRuntimeMetricFromMetadata(metadata map[string]string, unit string, keys ...string) stackMetricValue {
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			continue
		}
		return metricKnown(parsed, unit)
	}
	return metricUnknown(unit)
}

func managedRuntimeWorkerStatus(item managedRuntimeInventoryItem) string {
	if !item.NativeActive {
		return item.Status
	}
	if item.Approved {
		return managedRuntimeStatusApproved
	}
	if item.EnrollmentStatus == monthlyRuntimeEnrollmentStatusFailed {
		return managedRuntimeStatusRejected
	}
	return monthlyRuntimeEnrollmentStatusPending
}

func managedRuntimeApprovedAt(item managedRuntimeInventoryItem) string {
	if !item.Approved {
		return ""
	}
	return item.UpdatedAt
}

func managedRuntimeUpdatedAt(lease vmlease.Lease) string {
	if !lease.RenewedAt.IsZero() {
		return lease.RenewedAt.UTC().Format(time.RFC3339)
	}
	if !lease.ValidFrom.IsZero() {
		return lease.ValidFrom.UTC().Format(time.RFC3339)
	}
	return ""
}

func stackServerFromManagedRuntime(item managedRuntimeInventoryItem) stackOperationServer {
	notes := []string{"managed monthly runtime projection"}
	if !item.NativeActive {
		notes = append(notes, "provider actions disabled: execution authority is "+firstNonEmptyString(item.AuthorityState, "unknown"))
	}
	if !item.Approved {
		notes = append(notes, "runtime target not reported yet")
	}
	if item.Status == managedRuntimeStatusProvisioned {
		notes = append(notes, "no live telemetry yet — status reflects lease enrollment, not measured health (runtime agent not reporting)")
	}
	return stackOperationServer{
		ID:               item.ID,
		ServerID:         runtimeidentity.LeaseServerID(item.LeaseID),
		Hostname:         item.Hostname,
		Role:             item.Role,
		Status:           item.Status,
		Assignment:       managedRuntimeAssignStack,
		TechstackID:      item.StackID,
		AgentID:          item.ID,
		IP:               item.IP,
		OS:               managedRuntimeOSLinux,
		Arch:             managedRuntimeArchAMD64,
		LastSeen:         item.UpdatedAt,
		Approved:         item.Approved,
		ApprovedAt:       managedRuntimeApprovedAt(item),
		PreCheck:         managedRuntimePreCheckManaged,
		Source:           managedRuntimeInventorySource,
		LeaseID:          item.LeaseID,
		RuntimeLane:      item.RuntimeLane,
		RuntimeOffering:  item.RuntimeOffering,
		DesiredState:     item.DesiredState,
		EnrollmentStatus: item.EnrollmentStatus,
		Assignable:       item.Approved,
		Capabilities: map[string]any{
			workerFieldCPU:        item.VCPUs,
			workerFieldRAM:        item.MemoryMB,
			workerFieldDisk:       item.DiskGB,
			workerFieldProvider:   item.Provider,
			"runtime_lane":        item.RuntimeLane,
			"runtime_offering_id": item.RuntimeOffering,
			"lease_id":            item.LeaseID,
			"desired_state":       item.DesiredState,
			"enrollment_status":   item.EnrollmentStatus,
			"execution_authority": item.ExecutionAuthority,
			"authority_state":     item.AuthorityState,
			"runtime_ssh_host":    item.RuntimeHost,
			"runtime_ssh_user":    item.RuntimeSSHUser,
			"runtime_ssh_port":    item.RuntimeSSHPort,
			"region":              item.Region,
			"image":               item.Image,
		},
		Health: stackServerHealth{
			State:         item.Status,
			Source:        managedRuntimeInventorySource,
			CPUPercent:    item.CPUPercent,
			MemoryPercent: item.MemoryPercent,
			DiskPercent:   item.DiskPercent,
			UptimeSeconds: item.UptimeSeconds,
			UpdatedAt:     item.UpdatedAt,
			Notes:         notes,
		},
	}
}

// The keys intentionally mirror the stable worker inventory wire contract.
// Keeping them adjacent makes schema comparison easier than indirection would.
//
//nolint:goconst
func managedRuntimeWorkerResponse(item managedRuntimeInventoryItem) map[string]any {
	return map[string]any{
		"id":                                    item.ID,
		workerFieldServerID:                     runtimeidentity.LeaseServerID(item.LeaseID),
		workerFieldHostname:                     item.Hostname,
		"ip":                                    item.IP,
		"os":                                    managedRuntimeOSLinux,
		"arch":                                  managedRuntimeArchAMD64,
		preCheckStackIDField:                    item.StackID,
		managedRuntimeStatusApproved:            item.Approved,
		"approved_at":                           managedRuntimeApprovedAt(item),
		"last_seen":                             item.UpdatedAt,
		workerFieldCreated:                      item.UpdatedAt,
		workerFieldCPU:                          item.VCPUs,
		workerFieldRAM:                          item.MemoryMB,
		workerFieldDisk:                         item.DiskGB,
		"gpu":                                   "",
		"has_nvme":                              false,
		"has_hw_transcode":                      false,
		workerFieldDockerVersion:                "",
		featureResponseTypeKey:                  item.Role,
		workerFieldProvider:                     item.Provider,
		"tags":                                  "managed-runtime,monthly-runtime",
		"tenant_id":                             "",
		"connected_at":                          item.UpdatedAt,
		preCheckStatusField:                     managedRuntimeWorkerStatus(item),
		"source":                                managedRuntimeInventorySource,
		"lease_id":                              item.LeaseID,
		"runtime_lane":                          item.RuntimeLane,
		"runtime_offering_id":                   item.RuntimeOffering,
		"desired_state":                         item.DesiredState,
		"managed_runtime_lease_authority_state": item.AuthorityState,
		"execution_authority":                   item.ExecutionAuthority,
		"enrollment_status":                     item.EnrollmentStatus,
		"runtime_ssh_host":                      item.RuntimeHost,
		"runtime_ssh_user":                      item.RuntimeSSHUser,
		"runtime_ssh_port":                      item.RuntimeSSHPort,
		"assignable":                            item.Approved,
	}
}

func managedRuntimeIntFromMetadata(metadata map[string]string, keys ...string) int {
	for _, key := range keys {
		value := strings.TrimSpace(metadata[key])
		if value == "" {
			continue
		}
		parsed, err := strconv.Atoi(value)
		if err == nil && parsed > 0 {
			return parsed
		}
	}
	return 0
}

func findOperationServer(servers []stackOperationServer, id string) (stackOperationServer, bool) {
	id = strings.TrimSpace(id)
	for _, server := range servers {
		if server.ID == id {
			return server, true
		}
	}
	return stackOperationServer{}, false
}
