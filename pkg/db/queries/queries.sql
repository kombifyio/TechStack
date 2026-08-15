-- name: GetStack :one
SELECT *
FROM stacks
WHERE id = $1
  AND deleted_at IS NULL;

-- name: ListStacksByTenant :many
SELECT *
FROM stacks
WHERE tenant_id = $1
  AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: ListStacksByInstance :many
SELECT *
FROM stacks
WHERE instance_id = $1
  AND deleted_at IS NULL
ORDER BY created_at DESC;

-- name: CreateStack :one
INSERT INTO stacks (
    id,
    tenant_id,
    instance_id,
    owner_subject_id,
    name,
    description,
    mode,
    status,
    config_json,
    services_json
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
RETURNING *;

-- name: UpdateStackRuntimeSummary :one
UPDATE stacks
SET status = $2,
    runtime_summary_json = $3,
    drift_status = $4,
    drift_checked_at = $5
WHERE id = $1
RETURNING *;

-- name: SoftDeleteStack :exec
UPDATE stacks
SET deleted_at = now(),
    status = 'stopped'
WHERE id = $1;

-- name: GetJob :one
SELECT *
FROM jobs
WHERE id = $1;

-- name: ListJobsByStack :many
SELECT *
FROM jobs
WHERE stack_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: ListPendingJobs :many
SELECT *
FROM jobs
WHERE state = 'pending'
  AND scheduled_for <= now()
ORDER BY priority DESC, created_at
LIMIT $1;

-- name: UpsertJobSummary :one
INSERT INTO jobs (
    id,
    tenant_id,
    instance_id,
    stack_id,
    type,
    state,
    priority,
    progress,
    step,
    message,
    error,
    error_details,
    logs_json,
    result_json,
    scheduled_for
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13, $14, $15
)
ON CONFLICT (id) DO UPDATE SET
    state = EXCLUDED.state,
    priority = EXCLUDED.priority,
    progress = EXCLUDED.progress,
    step = EXCLUDED.step,
    message = EXCLUDED.message,
    error = EXCLUDED.error,
    error_details = EXCLUDED.error_details,
    logs_json = EXCLUDED.logs_json,
    result_json = EXCLUDED.result_json,
    scheduled_for = EXCLUDED.scheduled_for
RETURNING *;

-- name: StartJob :one
UPDATE jobs
SET state = 'running',
    started_at = now()
WHERE id = $1
RETURNING *;

-- name: CompleteJob :one
UPDATE jobs
SET state = 'completed',
    progress = 100,
    result_json = $2,
    completed_at = now()
WHERE id = $1
RETURNING *;

-- name: FailJob :one
UPDATE jobs
SET state = 'failed',
    error = $2,
    error_details = $3,
    completed_at = now()
WHERE id = $1
RETURNING *;

-- name: GetWorker :one
SELECT *
FROM workers
WHERE id = $1;

-- name: ListWorkersByInstance :many
SELECT *
FROM workers
WHERE instance_id = $1
ORDER BY hostname;

-- name: ListWorkersByTenantAndStatus :many
SELECT *
FROM workers
WHERE tenant_id = $1
  AND status = $2
ORDER BY last_seen_at DESC NULLS LAST, hostname;

-- name: UpsertWorkerHeartbeat :one
INSERT INTO workers (
    id,
    tenant_id,
    instance_id,
    hostname,
    ip,
    os,
    arch,
    status,
    last_seen_at,
    cpu_cores,
    ram_mb,
    disk_gb,
    gpu,
    has_nvme,
    has_hw_transcode,
    docker_version,
    type,
    provider,
    tags_json,
    capabilities_json,
    resources_json
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, now(), $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20
)
ON CONFLICT (tenant_id, id) DO UPDATE SET
    instance_id = EXCLUDED.instance_id,
    hostname = EXCLUDED.hostname,
    ip = EXCLUDED.ip,
    os = EXCLUDED.os,
    arch = EXCLUDED.arch,
    status = EXCLUDED.status,
    last_seen_at = now(),
    cpu_cores = EXCLUDED.cpu_cores,
    ram_mb = EXCLUDED.ram_mb,
    disk_gb = EXCLUDED.disk_gb,
    gpu = EXCLUDED.gpu,
    has_nvme = EXCLUDED.has_nvme,
    has_hw_transcode = EXCLUDED.has_hw_transcode,
    docker_version = EXCLUDED.docker_version,
    type = EXCLUDED.type,
    provider = EXCLUDED.provider,
    tags_json = EXCLUDED.tags_json,
    capabilities_json = EXCLUDED.capabilities_json,
    resources_json = EXCLUDED.resources_json
RETURNING *;

-- name: ApproveWorker :one
UPDATE workers
SET approved = true,
    status = 'approved',
    approved_at = now(),
    owner_subject_id = $2
WHERE id = $1
RETURNING *;

-- name: EnqueueAgentCommand :one
INSERT INTO agent_commands (
    id,
    tenant_id,
    instance_id,
    worker_id,
    stack_id,
    type,
    state,
    payload_json
) VALUES (
    $1, $2, $3, $4, $5, $6, 'queued', $7
)
RETURNING *;

-- name: ListQueuedAgentCommands :many
SELECT *
FROM agent_commands
WHERE worker_id = $1
  AND state = 'queued'
ORDER BY queued_at
LIMIT $2;

-- name: CompleteAgentCommand :one
UPDATE agent_commands
SET state = 'completed',
    result_json = $2,
    completed_at = now()
WHERE id = $1
RETURNING *;

-- name: CreateDriftResult :one
INSERT INTO drift_results (
    id,
    tenant_id,
    instance_id,
    stack_id,
    job_id,
    status,
    affected_resources,
    plan_summary,
    details_json
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING *;

-- name: ListDriftResultsByStack :many
SELECT *
FROM drift_results
WHERE stack_id = $1
ORDER BY created_at DESC
LIMIT $2;

-- name: CreatePrecheckResult :one
INSERT INTO precheck_results (
    id,
    tenant_id,
    instance_id,
    stack_id,
    worker_id,
    check_key,
    status,
    severity,
    message,
    details_json
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10
)
RETURNING *;

-- name: ListPrecheckResultsByStack :many
SELECT *
FROM precheck_results
WHERE stack_id = $1
ORDER BY created_at DESC;

-- name: GetFeatureRollout :one
SELECT *
FROM feature_rollouts
WHERE feature_key = $1;

-- name: ListFeatureRollouts :many
SELECT *
FROM feature_rollouts
ORDER BY feature_key;

-- name: UpsertFeatureRollout :one
INSERT INTO feature_rollouts (
    feature_key,
    enabled,
    reason,
    applied_by,
    source,
    metadata_json
) VALUES (
    $1, $2, $3, $4, $5, $6
)
ON CONFLICT (feature_key) DO UPDATE SET
    enabled = EXCLUDED.enabled,
    reason = EXCLUDED.reason,
    applied_by = EXCLUDED.applied_by,
    source = EXCLUDED.source,
    metadata_json = EXCLUDED.metadata_json
RETURNING *;

-- name: UpsertFeaturePreference :one
INSERT INTO feature_preferences (
    id,
    tenant_id,
    subject_id,
    feature_key,
    enabled
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (tenant_id, subject_id, feature_key) DO UPDATE SET
    enabled = EXCLUDED.enabled
RETURNING *;

-- name: GetIdentityProviderByKey :one
SELECT *
FROM identity_providers
WHERE provider_key = $1;

-- name: ListEnabledIdentityProviders :many
SELECT *
FROM identity_providers
WHERE enabled = true
ORDER BY display_name;

-- name: UpsertIdentityProvider :one
INSERT INTO identity_providers (
    provider_key,
    provider_type,
    display_name,
    issuer_url,
    client_id,
    audience,
    admin_group,
    enabled,
    config_json
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
ON CONFLICT (provider_key) DO UPDATE SET
    provider_type = EXCLUDED.provider_type,
    display_name = EXCLUDED.display_name,
    issuer_url = EXCLUDED.issuer_url,
    client_id = EXCLUDED.client_id,
    audience = EXCLUDED.audience,
    admin_group = EXCLUDED.admin_group,
    enabled = EXCLUDED.enabled,
    config_json = EXCLUDED.config_json
RETURNING *;

-- name: GetIdentitySubjectLink :one
SELECT *
FROM identity_subject_links
WHERE tenant_id = $1
  AND provider_key = $2
  AND subject_id = $3;

-- name: ListIdentitySubjectLinksByInstance :many
SELECT *
FROM identity_subject_links
WHERE instance_id = $1
ORDER BY provider_key, subject_email;

-- name: UpsertIdentitySubjectLink :one
INSERT INTO identity_subject_links (
    id,
    tenant_id,
    instance_id,
    provider_key,
    subject_id,
    subject_email,
    subject_display_name,
    local_role,
    is_admin,
    metadata_json,
    last_login_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, now()
)
ON CONFLICT (tenant_id, provider_key, subject_id) DO UPDATE SET
    instance_id = EXCLUDED.instance_id,
    subject_email = EXCLUDED.subject_email,
    subject_display_name = EXCLUDED.subject_display_name,
    local_role = EXCLUDED.local_role,
    is_admin = EXCLUDED.is_admin,
    metadata_json = EXCLUDED.metadata_json,
    last_login_at = now()
RETURNING *;

-- name: TouchIdentitySubjectLogin :one
UPDATE identity_subject_links
SET last_login_at = now()
WHERE tenant_id = $1
  AND provider_key = $2
  AND subject_id = $3
RETURNING *;

-- name: UpsertWalletItem :one
INSERT INTO wallet_items (
    id,
    tenant_id,
    instance_id,
    stack_id,
    item_type,
    provider,
    external_ref,
    metadata_json
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8
)
ON CONFLICT (id) DO UPDATE SET
    stack_id = EXCLUDED.stack_id,
    item_type = EXCLUDED.item_type,
    provider = EXCLUDED.provider,
    external_ref = EXCLUDED.external_ref,
    metadata_json = EXCLUDED.metadata_json
RETURNING *;

-- name: CreateAuditEvent :one
INSERT INTO audit_events (
    tenant_id,
    instance_id,
    actor_subject_id,
    action,
    resource_type,
    resource_id,
    details_json,
    ip_address,
    user_agent
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9
)
RETURNING *;

-- name: GetTofuState :one
SELECT *
FROM tofu_states
WHERE stack_id = $1
  AND workspace = $2;

-- name: UpsertTofuState :one
INSERT INTO tofu_states (
    id,
    tenant_id,
    instance_id,
    stack_id,
    workspace,
    state_json,
    version
) VALUES (
    $1, $2, $3, $4, $5, $6, 1
)
ON CONFLICT (stack_id, workspace) DO UPDATE SET
    state_json = EXCLUDED.state_json,
    version = tofu_states.version + 1
RETURNING *;

-- name: LockTofuState :one
UPDATE tofu_states
SET lock_id = $3,
    locked_at = now(),
    locked_by_subject_id = $4
WHERE stack_id = $1
  AND workspace = $2
  AND lock_id IS NULL
RETURNING *;

-- name: UnlockTofuState :one
UPDATE tofu_states
SET lock_id = NULL,
    locked_at = NULL,
    locked_by_subject_id = NULL
WHERE stack_id = $1
  AND workspace = $2
  AND lock_id = $3
RETURNING *;
