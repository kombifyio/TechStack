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
- [ ] gRPC handler implementations for ReportHealEvent + ReportDetection (blocked: protoc regen)
- [ ] Auto-restart failed tunnels/services
- [ ] Drift auto-correction (remediation engine)
- [ ] Certificate auto-renewal workflow
- [ ] Event bus with correlation and notification dispatch
- [ ] Rolling update orchestration

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
- Implementation: pkg/grpcserver/, pkg/jobs/drift_handler.go, pkg/auth/certs.go
- RIL packages: pkg/ril/actions/, internal/routes/ril_*.go
- Watchdog: pkg/agent/watchdog/ (5 recipes: service-restart, tunnel-reconnect, cert-rotate, disk-cleanup, oom-recovery)
