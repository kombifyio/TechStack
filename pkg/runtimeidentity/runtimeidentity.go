// Package runtimeidentity defines stable identifiers shared by the runtime
// rollout, enrollment, and inventory paths.
package runtimeidentity

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// LeaseServerID returns the canonical server read-model identity for a managed
// runtime lease. It intentionally does not expose the provider lease ID in a
// public server identifier, while remaining stable across bootstrap retries.
func LeaseServerID(leaseID string) string {
	leaseID = strings.TrimSpace(leaseID)
	if leaseID == "" {
		return ""
	}
	return "server_" + stableID("lease", leaseID)
}

// LeaseRuntimeAgentID returns the canonical Guard identity for a managed
// runtime lease. Provisioning, enrollment and rollout command delivery must
// all use this exact identity.
func LeaseRuntimeAgentID(tenantID, leaseID string) string {
	tenantID = strings.TrimSpace(tenantID)
	leaseID = strings.TrimSpace(leaseID)
	if tenantID == "" || leaseID == "" {
		return ""
	}
	return "worker_" + stableID(tenantID, leaseID)
}

// StackServerID returns the stable desired-runtime identity for a server that
// is known before a provider lease or Guard worker exists. The instance key is
// part of the identity so later multi-server stacks do not need a new scheme.
func StackServerID(stackID, instance string) string {
	stackID = strings.TrimSpace(stackID)
	if stackID == "" {
		return ""
	}
	instance = strings.TrimSpace(instance)
	if instance == "" {
		instance = "primary"
	}
	return "server_" + stableID("stack", stackID, instance)
}

// ServiceID returns a deterministic service identity across repeated Guard
// observations and rollout retries. Reported container/platform IDs remain
// evidence only and cannot replace this stack/server/service identity.
func ServiceID(stackID, serverID, serviceKey, instance string) string {
	stackID = strings.TrimSpace(stackID)
	serverID = strings.TrimSpace(serverID)
	serviceKey = strings.ToLower(strings.TrimSpace(serviceKey))
	if stackID == "" || serverID == "" || serviceKey == "" {
		return ""
	}
	instance = strings.ToLower(strings.TrimSpace(instance))
	if instance == "" {
		instance = "default"
	}
	return "service_" + stableID("service", stackID, serverID, serviceKey, instance)
}

func stableID(values ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(values, "\x00")))
	return hex.EncodeToString(sum[:])[:24]
}
