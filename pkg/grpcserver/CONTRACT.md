# Runtime Intelligence Layer

## Purpose

The beyond-IaC operational layer that manages infrastructure after provisioning. Provides live monitoring, drift detection, self-healing, auto-remediation, and Day-2+ operations through a gRPC agent network connected via mTLS.

## Core Features

- [x] gRPC agent protocol (Register, Heartbeat, CommandStream, ReportStatus, RunPreChecks)
- [x] mTLS certificate management (generation, validation, per-agent certs)
- [x] Command queue with backpressure (max 1000 pending) and persistence
- [x] Bidirectional command streaming (Core↔Agent)
- [x] OpenTofu + Terramate agent execution (TofuCommand, TerramateCommand)
- [x] Drift detection (scheduler + handler + UI: badge, diff, modal)
- [x] Health endpoints (/live, /ready, /startup)
- [x] Server inventory (RIL: ril_servers collection, inventory store, REST API)
- [x] RIL proto extensions (GetSystemInfo, ReportHealEvent, ReportDetection, StreamLogs, GetUpdateCandidates)
- [x] Action-Card CRUD (ril_action_cards collection, approve/dismiss lifecycle)
- [x] Self-Heal audit log (ril_heal_events collection, recipe registry)
- [x] Self-Heal Watchdog (agent-side: 5 curated recipes with auto-exec)
- [x] Detection Engines (update-scanner, error-matcher, resource-monitor, security-scanner, drift-detector)
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

- Architecture reference: docs/architecture/ARCHITECTURE_V2.md (Section 4)
- RIL expansion plan: docs/plans/2026-05-27-ril-expansion-v2.md
- Proto definition: api/proto/agent.proto
- Implementation: pkg/grpcserver/, pkg/jobs/drift_handler.go, pkg/auth/certs.go
- RIL packages: pkg/ril/inventory/, pkg/ril/actions/, pkg/ril/detection/, internal/routes/ril_*.go
- Watchdog: pkg/agent/watchdog/ (5 recipes: service-restart, tunnel-reconnect, cert-rotate, disk-cleanup, oom-recovery)
- Migrations: internal/migrations/036_add_ril_servers.go, 037_add_ril_commands.go
