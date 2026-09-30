# Runtime Intelligence Layer

## Purpose

The beyond-IaC operational layer that manages infrastructure after provisioning. Provides live monitoring, drift detection, self-healing, auto-remediation, and Day-2+ operations through a gRPC agent network connected via mTLS.

## Core Features

- [x] gRPC agent protocol (Register, Heartbeat, CommandStream, ReportStatus, RunPreChecks)
- [x] mTLS certificate management (generation, validation, per-agent certs)
- [x] Command queue with backpressure (max 1000 pending) and persistence
- [x] Bidirectional command streaming (Core↔Agent)
- [x] OpenTofu + Terramate command wire (TofuCommand, TerramateCommand) —
  compatibility only; direct tofu/terramate dispatch is retired and execution
  authority is not owned here (ADR-015 superseded, see docs/ARCHITECTURE.md)
- [x] Drift detection (scheduler + handler; the operator UI was removed
  2026-08-19)
- [x] Health endpoints (/live, /ready, /startup)
- [x] Server inventory (RIL: ril_servers collection, inventory store, REST API)
- [x] RIL proto extensions (GetSystemInfo, ReportHealEvent, ReportDetection, StreamLogs, GetUpdateCandidates)
- [x] Action-Card CRUD (ril_action_cards collection, approve/dismiss lifecycle)
- [x] Self-Heal audit log (ril_heal_events collection, recipe registry)
- [x] Self-Heal Watchdog (agent-side: 5 curated recipes with auto-exec)
- [ ] Detection Engines (update-scanner, error-matcher, resource-monitor,
  security-scanner, drift-detector) — a first `pkg/ril/detection` draft was
  never wired and was removed 2026-08-19
- [x] Action-Card lifecycle state machine (pending→approved→executing→completed/failed, retry path)
- [x] NL Server Access API (Phase 3: command dispatch, services, containers, logs, metrics, config, updates, diff, search)
- [x] ConnectedAgent extended with Services + Containers for live agent state
- [x] ril_commands collection + migration (037)
- [x] Phase 4: Ad-hoc specialist agents (homelab-scanner Worker + homelab-diagnostic Vertex ADK in kombify-Agents)
- [x] Phase 5: Personal AI / Sovereignty mode (Ollama sidecar manager + local model API)
- [ ] gRPC handler implementations for ReportHealEvent + ReportDetection (protoc regen is unblocked; see Code generation below)
- [ ] Auto-restart failed tunnels/services
- [ ] Drift auto-correction (remediation engine)
- [ ] Certificate auto-renewal workflow
- [ ] Event bus with correlation and notification dispatch
- [ ] Rolling update orchestration

## StackKitCommand Advanced fields

- `STACKKIT_OPERATION_ADVANCED_TRUST_IMPORT = 24` imports the installation's
  Advanced issuer trust bundle. It requires Owner approval and
  `advanced_trust_bundle` (field 35, at most 64 KiB); no other operation may
  carry that field. The Agent writes the bundle to
  `.stackkit/techstack-advanced-trust-bundle.json` in the workspace and runs
  `stackkit advanced trust import --bundle <file> --expect-sha256 <digest>
  --owner-approve --json`. Agents that support it advertise
  `stackkit.advanced-trust-import.v1`. A successful result must carry
  `stackkit.local-advanced-trust/v1` evidence whose `bundleSHA256` pins the
  dispatched bytes.
- `ADVANCED_CHANGE_SET_CREATE = 25`, `ADVANCED_CHANGE_SET_APPLY = 26`,
  `ADVANCED_DRIFT_RECONCILE = 27`, `ADVANCED_ROLLBACK = 28` and
  `ADVANCED_RESTORE_DRILL = 29` are the capability-gated Advanced operations
  (`terramate.change-set.create`, `terramate.change-set.apply`,
  `drift.reconcile.advanced`, `rollback.coordinated`, `restore.drill` in the
  `stackkit.advanced-operations/v1` catalog). Each requires
  `advanced_capability` (field 36, at most 64 KiB); every other operation
  rejects it. The Agent renders the argv from the pinned release's catalog,
  refuses entries that are not available, writes the capability to a 0600
  file under `.stackkit` for `--capability` and removes it afterwards.
  Agents that support them advertise `stackkit.advanced-operations.v1`.
- `candidate_spec_json` (field 29) is accepted by `INIT` and by the change-set
  create, apply and Advanced reconcile operations (bounded JSON); without it
  those three use the workspace StackSpec at `spec_path`.
- `change_set_id` (37) and `change_set_sha256` (38) are required by
  change-set apply and Advanced reconcile and rejected elsewhere;
  `rollback_target_ref` (39) is required by `ADVANCED_ROLLBACK` (an
  executor-state snapshot id or a change-set id). Rollback and restore drill
  require Owner approval.
- `StackKitResult.advanced_change_set_sha256` (field 15) is set only for a
  successful change-set create: the SHA-256 of the stored record the result
  names in `data.path`.
- Deprecated: `DRIFT_RECONCILE` and `STACKKIT_DRIFT_MODE_STANDARD`. Validation
  rejects both; managed deployments reconcile only in Advanced Mode.
- Rollout events may carry the per-stack statuses `converged`, `drifted`,
  `pending_root` and `other_host` of the Advanced phases.

## Constraints

- MUST: All agent communication uses gRPC over mTLS (TLS 1.3)
- MUST: Every agent has a unique CA-signed certificate with agent ID in CN
- MUST: Commands are persisted to survive Core restarts
- MUST: No silent recovery — all remediation actions logged and visible in UI
- MUST: Auto-remediation is feature-gated (requires explicit user consent)
- MUST NOT: Execute remediation without a defined rollback path
- MUST NOT: Allow agents without valid mTLS certificates
- MUST NOT: Exceed command queue backpressure limit (1000 pending)

## Success Criteria

- Agents register and maintain heartbeat at ~30s intervals
- Core detects agent offline within 3 missed heartbeats
- Drift detection correctly identifies state divergence between IaC and actual
- Commands execute on agents and results stream back in real-time
- Certificate rotation completes without agent downtime

## Notes

- RIL public-beta contract: docs/RIL_HARNESS_PUBLIC_BETA.md
- Durable orchestration decision: docs/ADR/0030-ril-orchestration-engine.md
- Proto definition: api/proto/agent.proto
- Code generation: `mise run proto:generate` regenerates `pkg/api/agentpb` from
  `api/proto/agent.proto` through `buf.yaml`/`buf.gen.yaml`; `buf`,
  `protoc-gen-go` and `protoc-gen-go-grpc` are pinned in `mise.toml` [tools].
  `mise run proto:check` fails on drift and the affected dev gate runs it when
  the proto, the buf config or `pkg/api/agentpb` changes. Never hand-edit the
  generated files.
- Implementation: pkg/grpcserver/, pkg/jobs/drift_handler.go, pkg/auth/certs.go
- RIL packages: pkg/ril/actions/, internal/routes/ril_*.go
- Watchdog: pkg/agent/watchdog/ (5 recipes: service-restart, tunnel-reconnect, cert-rotate, disk-cleanup, oom-recovery)
