package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/providercatalog"
	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/monthlyruntime"
)

const managedRuntimeEnrollmentWaitStartedAtField = "managed_runtime_enrollment_wait_started_at"

func copyManagedRuntimeTargetToJob(job *Job, target *ManagedRuntimeTarget) {
	target = normalizeManagedRuntimeTarget(target)
	if job == nil || target == nil {
		return
	}
	job.mu.Lock()
	defer job.mu.Unlock()
	job.managedRuntimeTarget = cloneManagedRuntimeTarget(target)
	if job.Result == nil {
		job.Result = map[string]interface{}{}
	}
	runtimeTarget := map[string]interface{}{
		"host":      target.Host,
		"public_ip": target.PublicIP,
		"ssh_host":  target.Host,
		"ssh_port":  target.SSHPort,
		"source":    target.Source,
	}
	job.Result[metadataKeyRuntimeSSHHost] = target.Host
	job.Result[metadataKeyRuntimePublicIP] = target.PublicIP
	job.Result[metadataKeyRuntimeSSHPort] = target.SSHPort
	if target.PrivateIP != "" {
		job.Result[metadataKeyRuntimePrivateIP] = target.PrivateIP
		runtimeTarget["private_ip"] = target.PrivateIP
	}
	if target.SSHUser != "" {
		job.Result[metadataKeyRuntimeSSHUser] = target.SSHUser
		runtimeTarget["ssh_user"] = target.SSHUser
	}
	if target.DockerHost != "" {
		job.Result["runtime_docker_host"] = target.DockerHost
		runtimeTarget["docker_host"] = target.DockerHost
	}
	job.Result["runtime_target"] = runtimeTarget
	if job.Payload != nil {
		job.Payload[metadataKeyRuntimeSSHHost] = target.Host
		job.Payload[metadataKeyRuntimePublicIP] = target.PublicIP
		job.Payload[metadataKeyRuntimeSSHPort] = target.SSHPort
		if target.PrivateIP != "" {
			job.Payload[metadataKeyRuntimePrivateIP] = target.PrivateIP
		}
		if target.SSHUser != "" {
			job.Payload[metadataKeyRuntimeSSHUser] = target.SSHUser
		}
		if target.DockerHost != "" {
			job.Payload["runtime_docker_host"] = target.DockerHost
		}
	}
}

func updateManagedRuntimeLeaseMetadata(ctx context.Context, manager ManagedLeaseManager, tenantID, leaseID string, target *ManagedRuntimeTarget, metrics map[string]string) error {
	updater, ok := manager.(ManagedLeaseMetadataUpdater)
	if !ok || updater == nil {
		return nil
	}
	metadata := managedRuntimeLeaseMetadata(target, metrics)
	if len(metadata) == 0 {
		return nil
	}
	return updater.UpdateLeaseMetadata(ctx, tenantID, leaseID, metadata)
}

func managedRuntimeLeaseMetadata(target *ManagedRuntimeTarget, metrics map[string]string) map[string]string {
	metadata := map[string]string{}
	put := func(key, value string) {
		if strings.TrimSpace(key) != "" && strings.TrimSpace(value) != "" {
			metadata[key] = strings.TrimSpace(value)
		}
	}
	if target = normalizeManagedRuntimeTarget(target); target != nil {
		put(metadataKeyRuntimeSSHHost, target.Host)
		put("runtime_host", target.Host)
		put(metadataKeyRuntimePublicIP, target.PublicIP)
		put("node_public_ip", target.PublicIP)
		put("public_ip", target.PublicIP)
		put(metadataKeyRuntimePrivateIP, target.PrivateIP)
		put("node_private_ip", target.PrivateIP)
		put(metadataKeyRuntimeSSHUser, target.SSHUser)
		if target.SSHPort > 0 {
			put(metadataKeyRuntimeSSHPort, strconv.Itoa(target.SSHPort))
		}
		put("runtime_docker_host", target.DockerHost)
		put("docker_host", target.DockerHost)
	}
	for key, value := range metrics {
		put(key, value)
	}
	return metadata
}

func mergeRuntimeMetrics(dst map[string]string, result map[string]interface{}) {
	if dst == nil || result == nil {
		return
	}
	metrics := mapFromInterface(result["runtime_metrics"])
	if len(metrics) == 0 {
		return
	}
	copyMetric := func(outKey string, keys ...string) {
		for _, key := range keys {
			if value := metricStringFromInterface(metrics[key]); value != "" {
				dst[outKey] = value
				return
			}
		}
	}
	copyMetric("runtime_cpu_percent", "cpu_percent", "cpuPercent")
	copyMetric("runtime_memory_percent", "memory_percent", "memoryPercent")
	copyMetric("runtime_disk_percent", "disk_percent", "diskPercent")
	copyMetric("runtime_uptime_seconds", "uptime_seconds", "uptimeSeconds")
	copyMetric("runtime_metrics_updated_at", "updated_at", "updatedAt")
}

func metricStringFromInterface(value interface{}) string {
	switch v := value.(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case json.Number:
		return strings.TrimSpace(v.String())
	default:
		return ""
	}
}

func stringInterfaceMap(values map[string]string) map[string]interface{} {
	out := make(map[string]interface{}, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func managedRuntimeTargetFromRuntimeMap(values map[string]interface{}, source string) *ManagedRuntimeTarget {
	if values == nil {
		return nil
	}
	nested := managedRuntimeTargetFromNestedMap(values["runtime_target"], source)
	target := &ManagedRuntimeTarget{
		Host: firstNonEmpty(
			stringFromMap(values, metadataKeyRuntimeSSHHost),
			stringFromMap(values, "ssh_host"),
			stringFromMap(values, "host"),
			stringFromMap(values, metadataKeyRuntimePublicIP),
			stringFromMap(values, "node_public_ip"),
			stringFromMap(values, "public_ip"),
			stringFromMap(values, metadataKeyRuntimePrivateIP),
			stringFromMap(values, "node_private_ip"),
			stringFromMap(values, "private_ip"),
		),
		PublicIP:            firstNonEmpty(stringFromMap(values, metadataKeyRuntimePublicIP), stringFromMap(values, "node_public_ip"), stringFromMap(values, "public_ip")),
		PrivateIP:           firstNonEmpty(stringFromMap(values, metadataKeyRuntimePrivateIP), stringFromMap(values, "node_private_ip"), stringFromMap(values, "private_ip")),
		SSHUser:             firstNonEmpty(stringFromMap(values, metadataKeyRuntimeSSHUser), stringFromMap(values, "ssh_user")),
		SSHPort:             firstPositiveInt(intFromMap(values, metadataKeyRuntimeSSHPort), intFromMap(values, "ssh_port")),
		SSHKeyPath:          firstNonEmpty(stringFromMap(values, "runtime_ssh_key_path"), stringFromMap(values, "ssh_key_path"), stringFromMap(values, "key_path")),
		SSHPrivateKey:       firstNonEmpty(stringFromMap(values, "runtime_ssh_private_key"), stringFromMap(values, "ssh_private_key"), stringFromMap(values, "private_key")),
		SSHClientPrivateKey: firstNonEmpty(stringFromMap(values, "runtime_client_private_key"), stringFromMap(values, "client_private_key")),
		SSHPassword:         firstNonEmpty(stringFromMap(values, "runtime_ssh_password"), stringFromMap(values, "ssh_password"), stringFromMap(values, "password")),
		DockerHost:          firstNonEmpty(stringFromMap(values, "runtime_docker_host"), stringFromMap(values, "docker_host")),
		Source:              source,
	}
	target = mergeManagedRuntimeTarget(target, nested)
	if normalized := normalizeManagedRuntimeTarget(target); normalized != nil {
		return normalized
	}
	return nested
}

func cloneManagedRuntimeTarget(target *ManagedRuntimeTarget) *ManagedRuntimeTarget {
	if target == nil {
		return nil
	}
	copied := *target
	return &copied
}

func mergeManagedRuntimeTarget(primary, fallback *ManagedRuntimeTarget) *ManagedRuntimeTarget {
	if primary == nil {
		return cloneManagedRuntimeTarget(fallback)
	}
	if fallback == nil {
		return primary
	}
	if primary.Host == "" {
		primary.Host = fallback.Host
	}
	if primary.PublicIP == "" {
		primary.PublicIP = fallback.PublicIP
	}
	if primary.PrivateIP == "" {
		primary.PrivateIP = fallback.PrivateIP
	}
	if primary.SSHUser == "" {
		primary.SSHUser = fallback.SSHUser
	}
	if primary.SSHPort <= 0 {
		primary.SSHPort = fallback.SSHPort
	}
	if primary.SSHKeyPath == "" {
		primary.SSHKeyPath = fallback.SSHKeyPath
	}
	if primary.SSHPrivateKey == "" {
		primary.SSHPrivateKey = fallback.SSHPrivateKey
	}
	if primary.SSHClientPrivateKey == "" {
		primary.SSHClientPrivateKey = fallback.SSHClientPrivateKey
	}
	if primary.SSHPassword == "" {
		primary.SSHPassword = fallback.SSHPassword
	}
	if primary.DockerHost == "" {
		primary.DockerHost = fallback.DockerHost
	}
	if primary.Source == "" {
		primary.Source = fallback.Source
	}
	return primary
}

func managedRuntimeTargetFromNestedMap(raw interface{}, source string) *ManagedRuntimeTarget {
	switch value := raw.(type) {
	case map[string]interface{}:
		return managedRuntimeTargetFromRuntimeMap(value, source)
	default:
		return nil
	}
}

func managedRuntimeOwnerID(job *Job, spec *core.KombinationSpec) string {
	if job == nil {
		return metadataString(spec, "owner_id")
	}
	return managedRuntimeOwnerIDFromSnapshot(job.Snapshot(), spec)
}

func managedRuntimeOwnerIDFromSnapshot(job JobSnapshot, spec *core.KombinationSpec) string {
	return firstNonEmpty(
		stringFromMap(job.Payload, "owner_id"),
		stringFromMap(job.Result, "owner_id"),
		metadataString(spec, "owner_id"),
	)
}

func managedRuntimeTenantID(job *Job, spec *core.KombinationSpec) string {
	if job == nil {
		return metadataString(spec, tenantIDField)
	}
	return managedRuntimeTenantIDFromSnapshot(job.Snapshot(), spec)
}

func managedRuntimeTenantIDFromSnapshot(job JobSnapshot, spec *core.KombinationSpec) string {
	return firstNonEmpty(
		stringFromMap(job.Payload, tenantIDField),
		stringFromMap(job.Result, tenantIDField),
		metadataString(spec, tenantIDField),
		managedRuntimeOwnerIDFromSnapshot(job, spec),
	)
}

func resolveManagedRuntimeTarget(ctx context.Context, cfg *ProvisionConfig, job *Job, spec *core.KombinationSpec) (*ManagedRuntimeTarget, error) {
	var fallbackTarget *ManagedRuntimeTarget
	var snapshot JobSnapshot
	if job != nil {
		job.mu.RLock()
		if managedRuntimeTargetHasRuntimeActionCredential(job.managedRuntimeTarget) {
			fallbackTarget = cloneManagedRuntimeTarget(job.managedRuntimeTarget)
		}
		job.mu.RUnlock()
		snapshot = job.Snapshot()
		if target := managedRuntimeTargetFromRuntimeMap(snapshot.Result, "job-result"); target != nil {
			if fallbackTarget == nil {
				fallbackTarget = target
			}
		}
		if fallbackTarget == nil {
			if target := managedRuntimeTargetFromRuntimeMap(snapshot.Payload, "job-payload"); target != nil {
				fallbackTarget = target
			}
		}
	}
	if fallbackTarget == nil && spec != nil {
		if target := ManagedRuntimeTargetFromMetadata(spec.Metadata); target != nil {
			fallbackTarget = target
		}
	}
	leaseID := firstNonEmpty(
		stringFromMap(snapshot.Result, leaseIDField),
		stringFromMap(snapshot.Payload, leaseIDField),
		metadataString(spec, leaseIDField),
	)
	if cfg != nil && cfg.RuntimeActions.RuntimeTargetResolver != nil && leaseID != "" {
		target, err := cfg.RuntimeActions.RuntimeTargetResolver.ResolveManagedRuntimeTarget(ctx, ManagedRuntimeTargetRequest{
			StackID:   snapshot.TargetID,
			StackName: firstNonEmpty(metadataString(spec, "stack_name"), snapshot.TargetName),
			StackKit:  metadataString(spec, metadataKeyStackKitCatalogRef),
			TenantID:  managedRuntimeTenantIDFromSnapshot(snapshot, spec),
			OwnerID:   managedRuntimeOwnerIDFromSnapshot(snapshot, spec),
			LeaseID:   leaseID,
			Provider:  firstNonEmpty(stringFromMap(snapshot.Result, metadataKeyProviderID), metadataString(spec, metadataKeyProviderID)),
			Metadata:  specMetadata(spec),
		})
		if err != nil {
			if managedRuntimeTargetTerminalError(err) {
				// Terminal states ("enrollment failed", "VM gone", forbidden, ...)
				// must not read as a transient "not available yet" wait message.
				return nil, err
			}
			return nil, fmt.Errorf("managed runtime lease address is not available yet for lease %q: %w", leaseID, err)
		}
		if normalized := normalizeManagedRuntimeTarget(target); normalized != nil {
			if !managedRuntimeTargetHasRuntimeActionCredential(normalized) {
				return nil, managedRuntimeTargetCredentialUnavailableError(leaseID, normalized)
			}
			return normalized, nil
		}
		return nil, fmt.Errorf("managed runtime lease address missing for lease %q; the current lease resolver returned no rollout target", leaseID)
	}

	// Job result and payload target fields are an unbound cache: they do not
	// carry the lease revision that produced them. Reusing that cache while a
	// lease resolver is available can split one rollout across two machines
	// after a lease replacement (SSH bootstrap on the stale host, typed
	// StackKits commands on the current runtime agent). Only use the cache when
	// there is no lease-backed authority capable of refreshing it.
	if managedRuntimeTargetHasRuntimeActionCredential(fallbackTarget) {
		return fallbackTarget, nil
	}

	if fallbackTarget != nil {
		return nil, managedRuntimeTargetCredentialUnavailableError(leaseID, fallbackTarget)
	}
	if leaseID != "" {
		return nil, fmt.Errorf("managed runtime lease address missing for lease %q; wait until VM lease enrollment exposes %s or %s, then retry rollout", leaseID, metadataKeyRuntimeSSHHost, metadataKeyRuntimePublicIP)
	}
	return nil, fmt.Errorf("managed runtime lease address missing; wait until VM lease enrollment exposes %s or %s, then retry rollout", metadataKeyRuntimeSSHHost, metadataKeyRuntimePublicIP)
}

func resolveManagedRuntimeTargetWithWait(ctx context.Context, cfg *ProvisionConfig, job *Job, spec *core.KombinationSpec, q *Queue) (*ManagedRuntimeTarget, error) {
	return resolveManagedRuntimeTargetWithWaitClock(ctx, cfg, job, spec, q, time.Now)
}

func resolveManagedRuntimeTargetWithWaitClock(ctx context.Context, cfg *ProvisionConfig, job *Job, spec *core.KombinationSpec, q *Queue, now func() time.Time) (*ManagedRuntimeTarget, error) {
	if cfg == nil || cfg.RuntimeActions.RuntimeTargetResolver == nil {
		target, err := resolveManagedRuntimeTarget(ctx, cfg, job, spec)
		clearManagedRuntimeEnrollmentWaitStart(job)
		return target, err
	}

	timeout, interval := managedRuntimeTargetWaitConfig(cfg)
	if timeout <= 0 {
		target, err := resolveManagedRuntimeTarget(ctx, cfg, job, spec)
		clearManagedRuntimeEnrollmentWaitStart(job)
		return target, err
	}

	snapshot := job.Snapshot()
	leaseID := firstNonEmpty(
		stringFromMap(snapshot.Result, leaseIDField),
		stringFromMap(snapshot.Payload, leaseIDField),
		metadataString(spec, leaseIDField),
	)
	observedAt := now().UTC()
	waitStartedAt, ok := managedRuntimeEnrollmentWaitStartedAt(snapshot.Result)
	if !ok {
		waitStartedAt = observedAt
		job.mutateResult(func(result map[string]interface{}) {
			result[managedRuntimeEnrollmentWaitStartedAtField] = waitStartedAt.Format(time.RFC3339Nano)
		})
	}
	deadline := waitStartedAt.Add(timeout)
	remaining := deadline.Sub(observedAt)
	if remaining <= 0 {
		timeoutErr := managedRuntimeEnrollmentTimeoutError(leaseID, timeout, nil)
		clearManagedRuntimeEnrollmentWaitStart(job)
		captureJobError(ctx, timeoutErr, map[string]interface{}{
			stepField:    StepPrepareRollout,
			leaseIDField: leaseID,
			reasonField:  "managed_runtime_target_wait_timeout",
		})
		return nil, timeoutErr
	}
	attemptTimeout := managedRuntimeEnrollmentBoundedDelay(managedRuntimeTargetResolveAttemptTimeout(timeout, interval), remaining)
	resumeAfter := managedRuntimeEnrollmentBoundedDelay(managedRuntimeEnrollmentResumeDelay(interval), remaining)
	if q != nil {
		if leaseID != "" {
			q.UpdateProgress(job.ID, 10, fmt.Sprintf("Waiting for managed VM lease %s enrollment...", leaseID))
		} else {
			q.UpdateProgress(job.ID, 10, "Waiting for managed VM lease enrollment...")
		}
	}
	breadcrumbStep(ctx, StepPrepareRollout, "waiting for managed VM lease target", map[string]interface{}{
		leaseIDField:        leaseID,
		"attempt_timeout_s": int(attemptTimeout.Seconds()),
		"resume_after_s":    int(resumeAfter.Seconds()),
	})

	// Perform one bounded observation per queue attempt. Enrollment is an
	// external dependency, so polling it inside the handler would occupy a
	// worker and misreport the job as running for minutes. A non-terminal result
	// is handed back to the queue, which exposes `waiting` and resumes later.
	target, err := resolveManagedRuntimeTargetAttempt(ctx, cfg, job, spec, attemptTimeout)
	if err == nil {
		clearManagedRuntimeEnrollmentWaitStart(job)
		return target, nil
	}
	if ctx.Err() != nil {
		clearManagedRuntimeEnrollmentWaitStart(job)
		return nil, ctx.Err()
	}
	if managedRuntimeTargetTerminalError(err) {
		clearManagedRuntimeEnrollmentWaitStart(job)
		captureJobError(ctx, err, map[string]interface{}{
			stepField:    StepPrepareRollout,
			leaseIDField: leaseID,
			reasonField:  "managed_runtime_target_terminal_error",
		})
		return nil, err
	}
	if managedRuntimeTargetNonWaitableError(err) {
		clearManagedRuntimeEnrollmentWaitStart(job)
		captureJobError(ctx, err, map[string]interface{}{
			stepField:    StepPrepareRollout,
			leaseIDField: leaseID,
			reasonField:  "managed_runtime_target_non_waitable_error",
		})
		return nil, err
	}

	observedAt = now().UTC()
	remaining = deadline.Sub(observedAt)
	if remaining <= 0 {
		timeoutErr := managedRuntimeEnrollmentTimeoutError(leaseID, timeout, err)
		clearManagedRuntimeEnrollmentWaitStart(job)
		captureJobError(ctx, timeoutErr, map[string]interface{}{
			stepField:    StepPrepareRollout,
			leaseIDField: leaseID,
			reasonField:  "managed_runtime_target_wait_timeout",
		})
		return nil, timeoutErr
	}
	resumeAfter = managedRuntimeEnrollmentBoundedDelay(managedRuntimeEnrollmentResumeDelay(interval), remaining)
	cause := fmt.Errorf("managed runtime lease target is not available yet: %w", err)
	return nil, newManagedRuntimeEnrollmentPendingError(leaseID, resumeAfter, cause)
}

func managedRuntimeEnrollmentWaitStartedAt(result map[string]interface{}) (time.Time, bool) {
	raw := strings.TrimSpace(stringFromMap(result, managedRuntimeEnrollmentWaitStartedAtField))
	if raw == "" {
		return time.Time{}, false
	}
	startedAt, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return time.Time{}, false
	}
	return startedAt.UTC(), true
}

func clearManagedRuntimeEnrollmentWaitStart(job *Job) {
	if job == nil {
		return
	}
	job.mutateResult(func(result map[string]interface{}) {
		delete(result, managedRuntimeEnrollmentWaitStartedAtField)
	})
}

func managedRuntimeEnrollmentBoundedDelay(requested, remaining time.Duration) time.Duration {
	if requested <= 0 || remaining <= 0 {
		return 0
	}
	if requested > remaining {
		return remaining
	}
	return requested
}

func managedRuntimeTargetNonWaitableError(err error) bool {
	category := ClassifyError(err)
	return category == ErrorCategoryPermanent || category == ErrorCategoryConfiguration
}

func managedRuntimeEnrollmentTimeoutError(leaseID string, timeout time.Duration, cause error) error {
	message := fmt.Sprintf("managed runtime target was not available within %s", timeout)
	if strings.TrimSpace(leaseID) != "" {
		message = fmt.Sprintf("managed runtime lease %s did not expose a rollout target within %s", strings.TrimSpace(leaseID), timeout)
	}
	if cause != nil {
		message = fmt.Sprintf("%s: %v", message, cause)
	}
	return fmt.Errorf("%w: %s", ErrManagedRuntimeEnrollmentFailed, message)
}

func managedRuntimeEnrollmentResumeDelay(interval time.Duration) time.Duration {
	const maxResumeDelay = 30 * time.Second
	if interval <= 0 {
		return defaultJobWaitResumeDelay
	}
	if interval > maxResumeDelay {
		return maxResumeDelay
	}
	return interval
}

func resolveManagedRuntimeTargetAttempt(ctx context.Context, cfg *ProvisionConfig, job *Job, spec *core.KombinationSpec, timeout time.Duration) (*ManagedRuntimeTarget, error) {
	if timeout <= 0 {
		return resolveManagedRuntimeTarget(ctx, cfg, job, spec)
	}
	attemptCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	target, err := resolveManagedRuntimeTarget(attemptCtx, cfg, job, spec)
	if err == nil {
		return target, nil
	}
	if attemptCtx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("managed runtime target resolve attempt timed out after %s: %w", timeout, err)
	}
	return nil, err
}

func managedRuntimeTargetTerminalError(err error) bool {
	return errors.Is(err, ErrManagedRuntimeEnrollmentFailed) ||
		errors.Is(err, ErrManagedRuntimeTargetCredentialFailed) ||
		errors.Is(err, monthlyruntime.ErrFeatureDisabled) ||
		errors.Is(err, monthlyruntime.ErrForbidden) ||
		errors.Is(err, monthlyruntime.ErrInvalidLease) ||
		errors.Is(err, monthlyruntime.ErrRuntimeClient) ||
		// A ghost lease (VM reaped/deleted provider-side) can never resolve by
		// waiting — the enrollment reset already queued a replacement path.
		errors.Is(err, monthlyruntime.ErrRuntimeVMGone)
}

func managedRuntimeTargetResolveAttemptTimeout(timeout, _ time.Duration) time.Duration {
	attemptTimeout := 30 * time.Second
	if envTimeout := parseEnvDuration("TECHSTACK_MANAGED_RUNTIME_RESOLVE_TIMEOUT"); envTimeout > 0 {
		attemptTimeout = envTimeout
	}
	if timeout > 0 && attemptTimeout > timeout {
		attemptTimeout = timeout
	}
	return attemptTimeout
}

func managedRuntimeTargetWaitConfig(cfg *ProvisionConfig) (time.Duration, time.Duration) {
	// Provider-backed monthly runtimes should expose an SSH target within a few
	// minutes of VM allocation. 5 minutes is the hard cap: anything longer
	// indicates a provider-side stall or a bug on our side, not normal startup.
	// Override with TECHSTACK_MANAGED_RUNTIME_WAIT_TIMEOUT / _POLL_INTERVAL
	// for shorter environment-specific fuses.
	maxTimeout := 5 * time.Minute
	timeout := maxTimeout
	interval := 5 * time.Second
	if cfg != nil {
		if cfg.ManagedRuntimeTargetWaitTimeout > 0 {
			timeout = cfg.ManagedRuntimeTargetWaitTimeout
		}
		if cfg.ManagedRuntimeTargetPollInterval > 0 {
			interval = cfg.ManagedRuntimeTargetPollInterval
		}
	}
	if envTimeout := parseEnvDuration("TECHSTACK_MANAGED_RUNTIME_WAIT_TIMEOUT"); envTimeout > 0 {
		timeout = envTimeout
	}
	if timeout > maxTimeout {
		timeout = maxTimeout
	}
	if envInterval := parseEnvDuration("TECHSTACK_MANAGED_RUNTIME_POLL_INTERVAL"); envInterval > 0 {
		interval = envInterval
	}
	if interval > timeout {
		interval = timeout
	}
	return timeout, interval
}

func parseEnvDuration(key string) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return 0
	}
	if d, err := time.ParseDuration(raw); err == nil && d > 0 {
		return d
	}
	return 0
}

func specMetadata(spec *core.KombinationSpec) map[string]string {
	if spec == nil {
		return nil
	}
	return spec.Metadata
}

func metadataString(spec *core.KombinationSpec, key string) string {
	if spec == nil || spec.Metadata == nil {
		return ""
	}
	return strings.TrimSpace(spec.Metadata[key])
}

func hydrateManagedRuntimeSpec(spec *core.KombinationSpec, target *ManagedRuntimeTarget) {
	target = normalizeManagedRuntimeTarget(target)
	if spec == nil || target == nil {
		return
	}
	providerID := metadataString(spec, metadataKeyProviderID)
	if len(spec.Nodes) == 0 {
		spec.Nodes = []core.NodeSpec{{
			Name:     stackRoleMain,
			Type:     stackRoleMain,
			Provider: providerID,
		}}
	}
	index := managedRuntimeNodeIndex(spec.Nodes)
	node := &spec.Nodes[index]
	if strings.TrimSpace(node.Name) == "" {
		node.Name = stackRoleMain
	}
	if strings.TrimSpace(node.Type) == "" {
		node.Type = stackRoleMain
	}
	if strings.TrimSpace(node.Provider) == "" || normalizeProvider(node.Provider) == providerCloud {
		node.Provider = providerID
	}
	if node.SSH == nil {
		node.SSH = &core.SSHConfig{}
	}
	node.SSH.Host = target.Host
	node.SSH.User = firstNonEmpty(target.SSHUser, node.SSH.User, "root")
	node.SSH.Port = firstPositiveInt(target.SSHPort, node.SSH.Port, 22)
	if spec.Metadata == nil {
		spec.Metadata = map[string]string{}
	}
	spec.Metadata[metadataKeyRuntimeSSHHost] = target.Host
	spec.Metadata[metadataKeyRuntimePublicIP] = target.PublicIP
	spec.Metadata[metadataKeyRuntimeSSHPort] = strconv.Itoa(target.SSHPort)
	if target.PrivateIP != "" {
		spec.Metadata[metadataKeyRuntimePrivateIP] = target.PrivateIP
	}
	if target.SSHUser != "" {
		spec.Metadata[metadataKeyRuntimeSSHUser] = target.SSHUser
	}
}

func hydrateManagedRuntimeUnifiedSpec(unified *core.UnifiedSpec, target *ManagedRuntimeTarget) {
	target = normalizeManagedRuntimeTarget(target)
	if unified == nil || target == nil {
		return
	}
	providerID := metadataString(&unified.KombinationSpec, metadataKeyProviderID)
	if len(unified.ResolvedNodes) == 0 {
		unified.ResolvedNodes = []core.ResolvedNode{{
			NodeSpec: core.NodeSpec{
				Name:     stackRoleMain,
				Type:     stackRoleMain,
				Provider: providerID,
				SSH: &core.SSHConfig{
					Host: target.Host,
					User: firstNonEmpty(target.SSHUser, "root"),
					Port: target.SSHPort,
				},
			},
		}}
	}
	index := managedRuntimeResolvedNodeIndex(unified.ResolvedNodes)
	node := &unified.ResolvedNodes[index]
	if node.SSH == nil {
		node.SSH = &core.SSHConfig{}
	}
	node.SSH.Host = target.Host
	node.SSH.User = firstNonEmpty(target.SSHUser, node.SSH.User, "root")
	node.SSH.Port = firstPositiveInt(target.SSHPort, node.SSH.Port, 22)
	node.PublicIP = firstNonEmpty(target.PublicIP, node.PublicIP, target.Host)
	node.PrivateIP = firstNonEmpty(target.PrivateIP, node.PrivateIP)
	if node.Tags == nil {
		node.Tags = map[string]string{}
	}
	node.Tags["runtime_target_source"] = firstNonEmpty(target.Source, "managed-runtime")
	if len(unified.Nodes) > index {
		unified.Nodes[index] = node.NodeSpec
	}
}

func managedRuntimeNodeIndex(nodes []core.NodeSpec) int {
	for i, node := range nodes {
		if strings.EqualFold(strings.TrimSpace(node.Type), stackRoleMain) || normalizeProvider(node.Provider) != providerLocal {
			return i
		}
	}
	return 0
}

func managedRuntimeResolvedNodeIndex(nodes []core.ResolvedNode) int {
	for i, node := range nodes {
		if strings.EqualFold(strings.TrimSpace(node.Type), stackRoleMain) || normalizeProvider(node.Provider) != providerLocal {
			return i
		}
	}
	return 0
}

func isManagedCloudSpec(spec *core.KombinationSpec) bool {
	if spec == nil {
		return false
	}
	if spec.Metadata != nil {
		switch spec.Metadata[metadataKeyServerMode] {
		case serverModeMonthlyRuntime, serverModeManagedCloud:
			return true
		}
		if spec.Metadata[metadataKeyRuntimeLane] == serverModeMonthlyRuntime {
			return true
		}
	}
	for _, node := range spec.Nodes {
		provider := node.Provider
		if provider == providerCloud {
			return true
		}
		if providercatalog.IsCanonicalProviderID(provider) {
			return true
		}
	}
	return false
}

func serverModeForSpec(spec *core.KombinationSpec) string {
	if isManagedCloudSpec(spec) {
		return serverModeMonthlyRuntime
	}
	return serverModeUserOwned
}

func providerIDForSpec(spec *core.KombinationSpec) string {
	if spec != nil && spec.Metadata != nil {
		if provider, err := providercatalog.CanonicalProviderID(spec.Metadata[metadataKeyProviderID]); err == nil {
			return provider
		}
	}
	return ""
}
