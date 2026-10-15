<script lang="ts">
  import {
    tr,
    formatDateTime,
    getLocale,
    localeDirection,
  } from "#lib/i18n.svelte.js";
  import { formatRelative } from "#lib/dashboard/time.js";
  import {
    Download,
    Globe,
    History,
    KeyRound,
    Printer,
    RefreshCw,
    ShieldCheck,
    DatabaseBackup,
    TriangleAlert,
    Users,
    Wifi,
  } from "@lucide/svelte";
  import type { Component } from "svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import GuardStateMark from "./GuardStateMark.svelte";
  import GuardOutcomes from "./GuardOutcomes.svelte";
  import GuardReachControl from "./GuardReachControl.svelte";
  import GuardIdentityUpgrade from "./GuardIdentityUpgrade.svelte";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    collectGuardEvidence,
    newRotationKey,
    rotateGuardCredential,
    type GuardCredentialRotation,
    type GuardFinding,
  } from "#lib/api/guard.js";
  import {
    detachSelfOwnedServer,
    type CanonicalServer,
  } from "#lib/api/registry.js";
  import {
    runStackKitLifecycleOperation,
    getKitDeployment,
    stackKitRecoveryPoints,
    stackKitStagedRestore,
    stackKitRestoreAdmission,
    stackKitRestoreOutcome,
    stackKitRestoreRequestDigest,
    stackKitRestoreNotAdmitted,
    stackKitBackupResultMatchesScope,
    type StackKitRecoveryPoint,
    type StackKitBackupOperation,
    type StackKitRestoreBinding,
    type StackKitRestoreAdmission,
    type StackKitLifecycleRequest,
  } from "#lib/api/stacks.js";
  import { getJob, streamJobProgress } from "#lib/api/jobs.js";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import {
    readRestoreActivationIntent,
    retainRestoreActivationIntent,
    updateRestoreActivationIntent,
    settleRestoreActivationIntent,
    type RestoreActivationBrowserIntent,
  } from "#lib/idempotency/managed-runtime.js";
  import {
    assessGuard,
    protectionNotices,
    type GuardFact,
    type GuardFactKey,
  } from "#lib/guard/facts.js";
  import { renderGuardMessage } from "#lib/guard/render.js";
  import {
    buildGuardReport,
    renderGuardReportHtml,
    reportFileName,
  } from "#lib/guard/report.js";

  interface Props {
    server: CanonicalServer | null;
    /** Worker capability map from the server detail payload, when held. */
    capabilities?: unknown;
    serverName: string;
    stackId?: string;
    agentId?: string;
    /** The page's own verdict that this server may be detached by its owner. */
    canDetach?: boolean;
    /** Reread the read model after an action changed something. */
    onChanged?: () => void | Promise<void>;
    onDetached?: () => void | Promise<void>;
  }

  let {
    server,
    capabilities,
    serverName,
    stackId,
    agentId,
    canDetach = false,
    onChanged,
    onDetached,
  }: Props = $props();

  // Evidence ages on screen without a refetch: the clock ticks, the facts
  // re-derive, and a fact whose evidence went stale falls back to unknown.
  let now = $state(Date.now());
  $effect(() => {
    const timer = setInterval(() => (now = Date.now()), 30_000);
    return () => clearInterval(timer);
  });

  const evidence = $derived(collectGuardEvidence(server, capabilities));
  const assessment = $derived(assessGuard(evidence, now));
  const notices = $derived(protectionNotices(evidence));
  const findings = $derived(
    sortedFindings(evidence.extension.security?.findings ?? []),
  );

  const ICONS: Record<GuardFactKey, Component> = {
    bound: KeyRound,
    connected: Wifi,
    operations: History,
    configuration: ShieldCheck,
    updated: RefreshCw,
    recoverable: DatabaseBackup,
    reachable: Globe,
  };

  const formatTime = (iso: string) =>
    formatDateTime(iso, { dateStyle: "medium", timeStyle: "short" });
  const say = (message: Parameters<typeof renderGuardMessage>[0]) =>
    renderGuardMessage(message, tr, formatTime);

  function observedLabel(fact: GuardFact): string {
    return fact.observedAt
      ? tr("guard.observed", { when: formatRelative(fact.observedAt, now) })
      : tr("guard.noObservation");
  }

  // ----- findings -----------------------------------------------------------
  const SEVERITY_RANK: Record<string, number> = {
    critical: 0,
    high: 1,
    medium: 2,
    low: 3,
  };
  function sortedFindings(list: GuardFinding[]): GuardFinding[] {
    const stateRank = (finding: GuardFinding) =>
      finding.state === "drifted" ? 0 : finding.state === "unknown" ? 1 : 2;
    return [...list].sort(
      (a, b) =>
        stateRank(a) - stateRank(b) ||
        (SEVERITY_RANK[a.severity] ?? 9) - (SEVERITY_RANK[b.severity] ?? 9),
    );
  }
  function labelOr(id: string, raw: string): string {
    const known = tr(id);
    return known !== id ? known : raw.replace(/[-_]/g, " ");
  }
  function exceptionText(finding: GuardFinding): string {
    const exception = finding.exception;
    if (!exception) return "";
    const parts = [
      exception.reason,
      exception.approved_by
        ? tr("guard.findings.approvedBy", { name: exception.approved_by })
        : "",
      exception.expires_at
        ? tr("guard.findings.until", { at: formatTime(exception.expires_at) })
        : "",
    ].filter(Boolean);
    return parts.length > 0
      ? tr("guard.findings.exceptionWith", { detail: parts.join(" · ") })
      : tr("guard.findings.exception");
  }

  // ----- verify now ---------------------------------------------------------
  const verifyAvailable = $derived(
    Boolean(stackId && agentId && server?.stack_actions?.includes("verify")),
  );
  let verifyState = $state<
    | { kind: "idle" }
    | { kind: "running" }
    | { kind: "accepted"; job: string; at: string }
    | { kind: "error"; message: string }
  >({ kind: "idle" });

  async function verifyNow() {
    if (!stackId || !agentId || verifyState.kind === "running") return;
    verifyState = { kind: "running" };
    try {
      const accepted = await runStackKitLifecycleOperation(stackId, {
        operation: "verify",
        agent_id: agentId,
      });
      verifyState = {
        kind: "accepted",
        job: accepted.job_id,
        at: new Date().toISOString(),
      };
      await onChanged?.();
    } catch (cause) {
      verifyState = { kind: "error", message: parseApiError(cause).message };
    }
  }

  // The existing node lifecycle owns repository custody and restore. This
  // panel observes its job result and supplies only the selected anchor.
  const backupAvailable = $derived(
    Boolean(
      stackId && agentId && server?.stack_actions?.includes("backup_status"),
    ),
  );
  const restoreAvailable = $derived(
    backupAvailable &&
      server?.stack_actions?.includes("backup_restore") === true,
  );
  const activationAvailable = $derived(
    restoreAvailable &&
      server?.stack_actions?.includes("backup_restore_activate") === true,
  );
  const recoveryAvailable = $derived(
    restoreAvailable &&
      server?.stack_actions?.includes("backup_restore_recover") === true,
  );
  let stagedRestore = $state<StackKitRestoreBinding | null>(null);
  let restoreAdmission = $state<StackKitRestoreAdmission | null>(null);
  let confirmingActivation = $state<"activate" | "recover" | null>(null);
  let restoreVerifiedAt = $state("");
  let historicalRestore = $state(false);
  let trackedBackupOperation = $state<StackKitBackupOperation | null>(null);
  let pendingActivation = $state<{
    key: string;
    request: StackKitLifecycleRequest;
    binding: StackKitRestoreBinding;
    intent: RestoreActivationBrowserIntent;
  } | null>(null);
  const restoreBrowserScope = $derived(
    authStore.currentUser?.sub && stackId && server?.id
      ? {
          actorId: authStore.currentUser.sub,
          tenantId:
            authStore.currentUser.tenantId ?? authStore.currentUser.orgId ?? "",
          stackId,
          nodeId: server.id,
        }
      : null,
  );
  let recoveryPoints = $state<StackKitRecoveryPoint[]>([]);
  let selectedAnchor = $state("");
  let confirmingRestore = $state(false);
  let backupPhase = $state<
    | "idle"
    | "loading"
    | "loaded"
    | "restoring"
    | "staged"
    | "unverified"
    | "activating"
    | "activated"
    | "recovered"
    | "recovery-required"
  >("idle");
  let backupError = $state<string | null>(null);
  let backupJobId = $state("");
  const selectedPoint = $derived(
    recoveryPoints.find((point) => point.evidenceId === selectedAnchor),
  );
  const backupBusy = $derived(
    backupPhase === "loading" ||
      backupPhase === "restoring" ||
      backupPhase === "activating",
  );
  const backupUnresolved = $derived(backupPhase === "recovery-required");
  let backupBinding = "";
  let backupAttempt = 0;
  let closeBackupJob = () => {};

  $effect(() => {
    const binding = `${server?.id ?? ""}:${stackId ?? ""}:${agentId ?? ""}:${restoreBrowserScope?.actorId ?? ""}:${restoreBrowserScope?.tenantId ?? ""}`;
    if (binding !== backupBinding) {
      backupBinding = binding;
      backupAttempt++;
      closeBackupJob();
      recoveryPoints = [];
      selectedAnchor = "";
      confirmingRestore = false;
      backupPhase = "idle";
      backupError = null;
      backupJobId = "";
      stagedRestore = null;
      restoreAdmission = null;
      confirmingActivation = null;
      restoreVerifiedAt = "";
      historicalRestore = false;
      trackedBackupOperation = null;
      pendingActivation = null;
      if (restoreBrowserScope) {
        try {
          const retained = readRestoreActivationIntent(
            window.sessionStorage,
            restoreBrowserScope,
          );
          if (retained) {
            backupPhase = "recovery-required";
            if (retained.binding.agentId !== agentId) {
              backupError = tr("guard.backup.scopeMismatch");
            } else {
              stagedRestore = retained.binding;
              pendingActivation = {
                key: retained.key,
                binding: retained.binding,
                intent: retained,
                request: {
                  operation: "backup_restore_activate",
                  agent_id: retained.binding.agentId,
                  restore_result_id: retained.binding.restoreResultId,
                  owner_approved: true,
                },
              };
              backupJobId = retained.observationJobId ?? retained.jobId ?? "";
              trackedBackupOperation =
                retained.observationOperation ?? "backup_restore_activate";
            }
          }
        } catch {
          backupPhase = "recovery-required";
          backupError = tr("guard.backup.outcomeUnknown");
        }
      }
    }
  });
  $effect(() => () => {
    backupAttempt++;
    closeBackupJob();
  });

  function followBackupJob(
    id: string,
    operation: StackKitBackupOperation,
    requestedAnchor: string,
    node: string,
    scope: string,
    attempt: number,
    restoreBinding?: StackKitRestoreBinding,
  ) {
    let closed = false;
    let poll: ReturnType<typeof setInterval> | undefined;
    let closeStream = () => {};
    const close = () => {
      closed = true;
      closeStream();
      if (poll) clearInterval(poll);
    };
    const read = async () => {
      if (closed || attempt !== backupAttempt) return;
      try {
        const job = await getJob(id);
        if (closed || attempt !== backupAttempt) return;
        if (job.id !== id || job.kit_deployment_id !== scope) {
          throw new Error(tr("guard.backup.scopeMismatch"));
        }
        if (restoreBinding) {
          const current = await getKitDeployment(scope);
          if (closed || attempt !== backupAttempt) return;
          if (current.id !== scope || !current.stackkit_instance_id)
            throw new Error(tr("guard.backup.scopeMismatch"));
          historicalRestore =
            current.stackkit_instance_id !== restoreBinding.instanceId;
          if (operation === "backup_restore_activate" && pendingActivation) {
            const intent = pendingActivation.intent;
            const digest = stackKitRestoreRequestDigest(
              job.result,
              restoreBinding,
              intent.actorId,
              intent.tenantId,
              intent.keySHA256 ?? "",
            );
            if (
              !digest ||
              (intent.requestDigest && intent.requestDigest !== digest)
            )
              throw new Error(tr("guard.backup.scopeMismatch"));
            intent.requestDigest = digest;
            updateRestoreActivationIntent(window.sessionStorage, intent);
            if (
              job.state === "failed" &&
              stackKitRestoreNotAdmitted(
                job.result,
                restoreBinding,
                intent.actorId,
                intent.tenantId,
                intent.keySHA256 ?? "",
                digest,
                job.id,
                job.started_at,
              )
            ) {
              settleRestoreActivationIntent(window.sessionStorage, intent);
              pendingActivation = null;
              restoreAdmission = null;
              backupPhase = "staged";
              backupError = tr("guard.backup.notAdmitted");
              close();
              return;
            }
          }
          const admitted = stackKitRestoreAdmission(job.result, restoreBinding);
          if (admitted) {
            if (
              pendingActivation?.intent.jobId &&
              admitted.activationOperationId !==
                `restore-${pendingActivation.intent.jobId}`
            )
              throw new Error(tr("guard.backup.scopeMismatch"));
            if (
              restoreAdmission &&
              restoreAdmission.activationOperationId !==
                admitted.activationOperationId
            )
              throw new Error(tr("guard.backup.scopeMismatch"));
            restoreAdmission = admitted;
          }
          if (
            ["completed", "failed", "canceled", "cancelled"].includes(job.state)
          ) {
            const outcome = restoreAdmission
              ? stackKitRestoreOutcome(
                  job.result,
                  restoreBinding,
                  operation,
                  restoreAdmission,
                )
              : undefined;
            backupPhase = outcome?.mode ?? "recovery-required";
            restoreVerifiedAt = outcome?.verifiedAt ?? "";
            if (
              outcome?.mode === "activated" ||
              outcome?.mode === "recovered"
            ) {
              if (pendingActivation?.intent.requestDigest)
                settleRestoreActivationIntent(
                  window.sessionStorage,
                  pendingActivation.intent,
                );
            }
            backupError =
              outcome && outcome.mode !== "recovery-required"
                ? null
                : tr("guard.backup.outcomeUnknown");
            close();
            await onChanged?.();
          }
          return;
        }
        if (
          job.state === "failed" ||
          job.state === "canceled" ||
          job.state === "cancelled"
        ) {
          backupError = job.error || tr("guard.backup.failed");
          backupPhase = "loaded";
          close();
        } else if (job.state === "completed") {
          if (
            !stackKitBackupResultMatchesScope(
              job.result,
              scope,
              node,
              operation,
            )
          ) {
            throw new Error(tr("guard.backup.scopeMismatch"));
          }
          if (operation === "backup_status") {
            recoveryPoints = stackKitRecoveryPoints(job.result, node, scope);
            selectedAnchor = "";
            backupPhase = "loaded";
          } else {
            const staged = stackKitStagedRestore(
              job.result,
              node,
              requestedAnchor,
              scope,
            );
            stagedRestore =
              staged && server?.id
                ? {
                    ...staged,
                    stackId: scope,
                    agentId: node,
                    nodeId: server.id,
                  }
                : null;
            backupPhase = stagedRestore ? "staged" : "unverified";
          }
          close();
          await onChanged?.();
        }
      } catch (cause) {
        if (closed || attempt !== backupAttempt) return;
        backupError = parseApiError(cause).message;
        if (restoreBinding) {
          backupPhase = "recovery-required";
          close();
          return;
        }
        recoveryPoints = [];
        selectedAnchor = "";
        backupPhase = "loaded";
        close();
      }
    };
    closeStream = streamJobProgress(id, {
      onEvent: () => {
        void read();
      },
      onFallback: () => {
        if (!closed && !poll)
          poll = setInterval(() => {
            void read();
          }, 3_000);
      },
    });
    void read();
    return close;
  }

  async function loadRecoveryPoints() {
    if (
      !backupAvailable ||
      !stackId ||
      !agentId ||
      backupBusy ||
      backupUnresolved
    )
      return;
    const node = agentId;
    const scope = stackId;
    const attempt = ++backupAttempt;
    closeBackupJob();
    backupError = null;
    recoveryPoints = [];
    selectedAnchor = "";
    confirmingRestore = false;
    backupPhase = "loading";
    historicalRestore = false;
    stagedRestore = null;
    restoreAdmission = null;
    pendingActivation = null;
    confirmingActivation = null;
    restoreVerifiedAt = "";
    try {
      const accepted = await runStackKitLifecycleOperation(scope, {
        operation: "backup_status",
        agent_id: node,
      });
      if (attempt !== backupAttempt) return;
      if (
        !accepted?.job_id ||
        accepted.kit_deployment_id !== scope ||
        accepted.agent_id !== node ||
        accepted.operation !== "backup_status"
      ) {
        throw new Error(tr("guard.backup.scopeMismatch"));
      }
      backupJobId = accepted.job_id;
      trackedBackupOperation = "backup_status";
      closeBackupJob = followBackupJob(
        accepted.job_id,
        "backup_status",
        "",
        node,
        scope,
        attempt,
      );
    } catch (cause) {
      if (attempt !== backupAttempt) return;
      backupError = parseApiError(cause).message;
      backupPhase = "loaded";
    }
  }

  async function stageSelectedRestore() {
    if (
      !restoreAvailable ||
      !stackId ||
      !agentId ||
      !selectedPoint ||
      !confirmingRestore ||
      backupBusy ||
      backupUnresolved
    )
      return;
    const node = agentId;
    const scope = stackId;
    const anchor = selectedPoint.evidenceId;
    const attempt = ++backupAttempt;
    closeBackupJob();
    confirmingRestore = false;
    backupError = null;
    backupPhase = "restoring";
    historicalRestore = false;
    stagedRestore = null;
    restoreAdmission = null;
    pendingActivation = null;
    confirmingActivation = null;
    restoreVerifiedAt = "";
    try {
      const accepted = await runStackKitLifecycleOperation(scope, {
        operation: "backup_restore",
        agent_id: node,
        snapshot_anchor_id: anchor,
        owner_approved: true,
      });
      if (attempt !== backupAttempt) return;
      if (
        !accepted?.job_id ||
        accepted.kit_deployment_id !== scope ||
        accepted.agent_id !== node ||
        accepted.operation !== "backup_restore"
      ) {
        throw new Error(tr("guard.backup.scopeMismatch"));
      }
      backupJobId = accepted.job_id;
      trackedBackupOperation = "backup_restore";
      closeBackupJob = followBackupJob(
        accepted.job_id,
        "backup_restore",
        anchor,
        node,
        scope,
        attempt,
      );
    } catch (cause) {
      if (attempt !== backupAttempt) return;
      backupError = parseApiError(cause).message;
      recoveryPoints = [];
      selectedAnchor = "";
      backupPhase = "loaded";
    }
  }

  async function submitActivationRequest(retry = false) {
    if (
      !stagedRestore ||
      (!retry && !activationAvailable) ||
      !restoreBrowserScope ||
      backupBusy ||
      (!retry &&
        (confirmingActivation !== "activate" || backupPhase !== "staged"))
    )
      return;
    if (retry && !pendingActivation) return;
    const binding = { ...stagedRestore };
    const browserScope = { ...restoreBrowserScope };
    confirmingActivation = null;
    const attempt = ++backupAttempt;
    closeBackupJob();
    backupPhase = "activating";
    backupError = null;
    try {
      if (!retry) {
        const current = await getKitDeployment(binding.stackId);
        if (attempt !== backupAttempt) return;
        if (
          current.id !== binding.stackId ||
          current.stackkit_instance_id !== binding.instanceId
        ) {
          historicalRestore = true;
          backupPhase = "staged";
          return;
        }
        const intent = retainRestoreActivationIntent(
          window.sessionStorage,
          browserScope,
          binding,
        );
        pendingActivation = {
          key: intent.key,
          binding,
          intent,
          request: {
            operation: "backup_restore_activate",
            agent_id: binding.agentId,
            restore_result_id: binding.restoreResultId,
            owner_approved: true,
          },
        };
        restoreAdmission = null;
        backupJobId = "";
      }
      if (!pendingActivation) return;
      const pending = pendingActivation;
      const keyBytes = new Uint8Array(
        await crypto.subtle.digest(
          "SHA-256",
          new TextEncoder().encode(pending.key),
        ),
      );
      if (attempt !== backupAttempt) return;
      pending.intent.keySHA256 = Array.from(keyBytes, (byte) =>
        byte.toString(16).padStart(2, "0"),
      ).join("");
      updateRestoreActivationIntent(window.sessionStorage, pending.intent);
      const accepted = await runStackKitLifecycleOperation(
        pending.binding.stackId,
        pending.request,
        pending.key,
      );
      if (attempt !== backupAttempt) return;
      if (
        !accepted?.job_id ||
        accepted.kit_deployment_id !== pending.binding.stackId ||
        accepted.agent_id !== pending.binding.agentId ||
        accepted.operation !== "backup_restore_activate"
      )
        throw new Error(tr("guard.backup.scopeMismatch"));
      backupJobId = accepted.job_id;
      pending.intent.jobId = accepted.job_id;
      pending.intent.observationJobId = accepted.job_id;
      pending.intent.observationOperation = "backup_restore_activate";
      updateRestoreActivationIntent(window.sessionStorage, pending.intent);
      trackedBackupOperation = "backup_restore_activate";
      closeBackupJob = followBackupJob(
        accepted.job_id,
        "backup_restore_activate",
        pending.binding.anchorId,
        pending.binding.agentId,
        pending.binding.stackId,
        attempt,
        pending.binding,
      );
    } catch (cause) {
      if (attempt !== backupAttempt) return;
      backupPhase = pendingActivation ? "recovery-required" : "staged";
      backupError = pendingActivation
        ? tr("guard.backup.outcomeUnknown")
        : parseApiError(cause).message;
    }
  }

  async function recoverOriginalRestore() {
    if (
      !stagedRestore ||
      !restoreAdmission ||
      !recoveryAvailable ||
      backupBusy ||
      confirmingActivation !== "recover"
    )
      return;
    const binding = { ...stagedRestore };
    const original = { ...restoreAdmission };
    const attempt = ++backupAttempt;
    confirmingActivation = null;
    closeBackupJob();
    backupError = null;
    backupPhase = "activating";
    try {
      const accepted = await runStackKitLifecycleOperation(binding.stackId, {
        operation: "backup_restore_recover",
        agent_id: binding.agentId,
        restore_result_id: original.restoreResultId,
        activation_operation_id: original.activationOperationId,
        restore_plan_hash: original.planHash,
        owner_approved: true,
      });
      if (attempt !== backupAttempt) return;
      if (
        !accepted?.job_id ||
        accepted.kit_deployment_id !== binding.stackId ||
        accepted.agent_id !== binding.agentId ||
        accepted.operation !== "backup_restore_recover"
      )
        throw new Error(tr("guard.backup.scopeMismatch"));
      backupJobId = accepted.job_id;
      if (pendingActivation) {
        pendingActivation.intent.observationJobId = accepted.job_id;
        pendingActivation.intent.observationOperation =
          "backup_restore_recover";
        updateRestoreActivationIntent(
          window.sessionStorage,
          pendingActivation.intent,
        );
      }
      trackedBackupOperation = "backup_restore_recover";
      closeBackupJob = followBackupJob(
        accepted.job_id,
        "backup_restore_recover",
        binding.anchorId,
        binding.agentId,
        binding.stackId,
        attempt,
        binding,
      );
    } catch (cause) {
      if (attempt !== backupAttempt) return;
      backupPhase = "recovery-required";
      backupError = tr("guard.backup.outcomeUnknown");
    }
  }

  function observeOriginalRestore() {
    if (!stagedRestore || !backupJobId || !trackedBackupOperation || backupBusy)
      return;
    const binding = { ...stagedRestore };
    const attempt = ++backupAttempt;
    closeBackupJob();
    backupError = null;
    backupPhase = "activating";
    closeBackupJob = followBackupJob(
      backupJobId,
      trackedBackupOperation,
      binding.anchorId,
      binding.agentId,
      binding.stackId,
      attempt,
      binding,
    );
  }

  // ----- rotate credential --------------------------------------------------
  const rotateListed = $derived(
    server?.allowed_actions?.includes("rotate_credentials") === true,
  );
  const credentialGeneration = $derived(evidence.worker.credential_generation);
  let rotateStep = $state<"idle" | "confirming" | "running" | "done">("idle");
  let rotateError = $state<string | null>(null);
  let rotation = $state<GuardCredentialRotation | null>(null);
  let rotationKey = "";
  let tokenCopied = $state(false);

  function beginRotate() {
    rotateError = null;
    rotationKey = newRotationKey();
    rotateStep = "confirming";
  }

  async function confirmRotate() {
    if (!server || credentialGeneration === undefined) return;
    rotateStep = "running";
    rotateError = null;
    try {
      rotation = await rotateGuardCredential(
        server.id,
        credentialGeneration,
        rotationKey,
      );
      rotateStep = "done";
      await onChanged?.();
    } catch (cause) {
      rotateError = parseApiError(cause).message;
      rotateStep = "confirming";
    }
  }

  async function copyToken() {
    if (!rotation?.agent_token) return;
    try {
      await navigator.clipboard.writeText(rotation.agent_token);
      tokenCopied = true;
      setTimeout(() => (tokenCopied = false), 2000);
    } catch {
      rotateError = tr("guard.rotate.copyFailed");
    }
  }

  /** Drop the one-time credential from memory; it cannot be shown again. */
  function dismissRotation() {
    rotation = null;
    rotateStep = "idle";
    tokenCopied = false;
  }

  // ----- detach -------------------------------------------------------------
  let detachStep = $state<"idle" | "confirming" | "running">("idle");
  let detachError = $state<string | null>(null);

  async function confirmDetach() {
    if (!server) return;
    detachStep = "running";
    detachError = null;
    try {
      await detachSelfOwnedServer(server.id);
      await onDetached?.();
      detachStep = "idle";
    } catch (cause) {
      detachError = parseApiError(cause).message;
      detachStep = "confirming";
    }
  }

  // ----- evidence report ----------------------------------------------------
  let reportError = $state<string | null>(null);
  let reportBusy = $state(false);

  async function makeReport() {
    return buildGuardReport({
      server: { id: server?.id ?? "", name: serverName },
      evidence,
      assessment,
      generatedAt: new Date(),
      locale: getLocale(),
      translate: tr,
    });
  }

  async function downloadReport() {
    reportBusy = true;
    reportError = null;
    try {
      const report = await makeReport();
      const url = URL.createObjectURL(
        new Blob([JSON.stringify(report, null, 2)], {
          type: "application/json",
        }),
      );
      const link = document.createElement("a");
      link.href = url;
      link.download = reportFileName(report);
      document.body.appendChild(link);
      link.click();
      link.remove();
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch {
      reportError = tr("guard.report.failed");
    } finally {
      reportBusy = false;
    }
  }

  async function openPrintable() {
    reportBusy = true;
    reportError = null;
    try {
      const report = await makeReport();
      const html = renderGuardReportHtml(
        report,
        tr,
        localeDirection(getLocale()),
      );
      const url = URL.createObjectURL(new Blob([html], { type: "text/html" }));
      window.open(url, "_blank");
      setTimeout(() => URL.revokeObjectURL(url), 60_000);
    } catch {
      reportError = tr("guard.report.failed");
    } finally {
      reportBusy = false;
    }
  }
</script>

<section
  class="mb-6 rounded-xl border border-border bg-card p-5"
  aria-labelledby="guard-panel-title"
  data-testid="guard-panel"
  data-overall={assessment.overall}
>
  <header class="flex flex-wrap items-start justify-between gap-3">
    <div class="min-w-0">
      <div class="flex items-center gap-2">
        <ShieldCheck class="h-5 w-5 shrink-0 text-primary" aria-hidden="true" />
        <h2
          id="guard-panel-title"
          class="text-lg font-semibold text-foreground"
        >
          {tr("guard.name")}
        </h2>
      </div>
      <p class="mt-1 max-w-2xl text-sm text-muted-foreground">
        {tr("guard.promise")}
      </p>
    </div>
    <div
      class="flex flex-col items-end gap-1 text-end"
      data-testid="guard-overall"
    >
      <span
        class="inline-flex items-center gap-1.5 text-sm font-semibold {assessment.overall ===
        'ready'
          ? 'text-success'
          : assessment.overall === 'attention'
            ? 'text-warning'
            : 'text-muted-foreground'}"
      >
        {#if assessment.overall === "attention"}
          <TriangleAlert class="h-4 w-4" aria-hidden="true" />
        {/if}
        {tr(`guard.overall.${assessment.overall}`)}
      </span>
      <span class="text-xs text-muted-foreground">
        {tr("guard.overall.count", {
          ok: assessment.okCount,
          total: assessment.facts.length,
        })}
      </span>
    </div>
  </header>

  {#if !server}
    <p class="mt-3 text-xs text-warning" role="status">
      {tr("guard.serverUnavailable")}
    </p>
  {/if}

  <ul class="mt-4 divide-y divide-border rounded-lg border border-border">
    {#each assessment.facts as fact (fact.key)}
      {@const Icon = ICONS[fact.key]}
      <li
        class="grid gap-x-4 gap-y-1 p-3 sm:grid-cols-[13rem_minmax(0,1fr)]"
        data-testid="guard-fact"
        data-fact={fact.key}
        data-state={fact.state}
      >
        <div class="flex min-w-0 flex-col gap-1">
          <span
            class="flex items-center gap-2 text-sm font-medium text-foreground"
          >
            <Icon
              class="h-4 w-4 shrink-0 text-muted-foreground"
              aria-hidden="true"
            />
            {tr(`guard.fact.${fact.key}.title`)}
          </span>
          <GuardStateMark state={fact.state} reduced={fact.reduced} />
        </div>
        <div class="min-w-0 text-sm">
          <p class="text-foreground">{say(fact.reason)}</p>
          {#if fact.details.length > 0}
            <ul class="mt-1 list-disc ps-5 text-xs text-muted-foreground">
              {#each fact.details as detail, index (index)}
                <li>{say(detail)}</li>
              {/each}
            </ul>
          {/if}
          {#if fact.next}
            <p class="mt-1 text-xs text-foreground">
              <span class="font-semibold">{tr("guard.next")}</span>
              {say(fact.next)}
            </p>
          {/if}
          <p class="mt-1 text-[11px] text-muted-foreground">
            {observedLabel(fact)}
          </p>
        </div>
      </li>
    {/each}
  </ul>

  {#if backupAvailable}
    <section
      class="mt-5 rounded-lg border border-border p-3"
      aria-labelledby="guard-backup-title"
      data-testid="guard-backup-history"
    >
      <div class="flex flex-wrap items-center justify-between gap-2">
        <h3
          id="guard-backup-title"
          class="text-sm font-semibold text-foreground"
        >
          {tr("guard.backup.title")}
        </h3>
        <Button
          variant="outline"
          size="sm"
          disabled={backupBusy || backupUnresolved}
          onclick={loadRecoveryPoints}
        >
          {tr(
            backupPhase === "loading"
              ? "guard.backup.loading"
              : "guard.backup.load",
          )}
        </Button>
      </div>
      <p class="mt-2 text-xs text-muted-foreground">
        {tr("guard.backup.recorded")}
      </p>
      {#if backupError}
        <p class="mt-2 text-sm text-destructive" role="alert">{backupError}</p>
      {/if}
      {#if backupPhase !== "idle" && backupPhase !== "loading" && recoveryPoints.length === 0 && !backupError}
        <p class="mt-3 text-sm text-muted-foreground" role="status">
          {tr("guard.backup.empty")}
        </p>
      {/if}
      {#if recoveryPoints.length > 0}
        <label for="guard-backup-point" class="mt-3 block text-sm font-medium"
          >{tr("guard.backup.choose")}</label
        >
        <select
          id="guard-backup-point"
          bind:value={selectedAnchor}
          disabled={backupBusy || backupUnresolved}
          onchange={() => {
            confirmingRestore = false;
            stagedRestore = null;
            restoreAdmission = null;
            pendingActivation = null;
            confirmingActivation = null;
            restoreVerifiedAt = "";
            historicalRestore = false;
            if (backupPhase === "staged" || backupPhase === "unverified")
              backupPhase = "loaded";
          }}
          class="mt-1 w-full rounded-md border border-input bg-background p-2 text-sm"
        >
          <option value="">{tr("guard.backup.placeholder")}</option>
          {#each recoveryPoints as point (point.evidenceId)}
            <option value={point.evidenceId}
              >{formatTime(point.recordedAt)} · {tr(
                point.currentPlan
                  ? "guard.backup.currentPlan"
                  : "guard.backup.historicalPlan",
              )}{point.state === "clock-skew"
                ? ` · ${tr("guard.backup.clockSkew")}`
                : ""}</option
            >
          {/each}
        </select>
        {#if restoreAvailable}
          {#if confirmingRestore && selectedPoint}
            <p class="mt-3 text-sm">
              {tr("guard.backup.confirm", {
                when: formatTime(selectedPoint.recordedAt),
              })}
            </p>
            <div class="mt-2 flex flex-wrap gap-2">
              <Button
                size="sm"
                disabled={backupBusy}
                onclick={stageSelectedRestore}
                >{tr("guard.backup.confirmAction")}</Button
              >
              <Button
                variant="outline"
                size="sm"
                onclick={() => (confirmingRestore = false)}
                >{tr("guard.cancel")}</Button
              >
            </div>
          {:else}
            <Button
              class="mt-3"
              variant="outline"
              size="sm"
              disabled={!selectedPoint || backupBusy}
              onclick={() => (confirmingRestore = true)}
              >{tr("guard.backup.stage")}</Button
            >
          {/if}
        {/if}
      {/if}
      {#if backupPhase === "restoring"}
        <p class="mt-3 text-sm" role="status">{tr("guard.backup.running")}</p>
      {:else if backupPhase === "staged"}
        <p class="mt-3 text-sm" role="status">{tr("guard.backup.staged")}</p>
      {:else if backupPhase === "unverified"}
        <p class="mt-3 text-sm text-warning" role="status">
          {tr("guard.backup.unverified")}
        </p>
      {/if}
      {#if stagedRestore && backupPhase === "staged" && !historicalRestore}
        {#if activationAvailable}
          {#if confirmingActivation === "activate"}
            <p class="mt-3 text-sm">
              {tr("guard.backup.activateConfirm", { name: serverName })}
            </p>
            <div class="mt-2 flex gap-2">
              <Button size="sm" onclick={() => submitActivationRequest()}
                >{tr("guard.backup.activateAction")}</Button
              >
              <Button
                size="sm"
                variant="outline"
                onclick={() => (confirmingActivation = null)}
                >{tr("guard.cancel")}</Button
              >
            </div>
          {:else}
            <Button
              class="mt-3"
              size="sm"
              onclick={() => (confirmingActivation = "activate")}
              >{tr("guard.backup.activate")}</Button
            >
          {/if}
        {:else}
          <p class="mt-3 text-sm text-muted-foreground">
            {tr("guard.backup.activationUnavailable")}
          </p>
        {/if}
      {/if}
      {#if historicalRestore}
        <p class="mt-3 text-sm text-warning" role="status">
          {tr("guard.backup.historicalOperation")}
        </p>
      {/if}
      {#if backupPhase === "activating"}
        <p class="mt-3 text-sm" role="status">
          {tr("guard.backup.activating")}
        </p>
      {:else if backupPhase === "activated" || backupPhase === "recovered"}
        {#if !historicalRestore}
          <p class="mt-3 text-sm" role="status">
            {tr(
              backupPhase === "activated"
                ? "guard.backup.activated"
                : "guard.backup.recovered",
            )}
          </p>
          {#if restoreVerifiedAt && !historicalRestore}<p
              class="mt-1 text-xs text-muted-foreground"
            >
              {tr("guard.backup.accessVerified", {
                when: formatTime(restoreVerifiedAt),
              })}
            </p>{/if}
        {/if}
      {:else if backupPhase === "recovery-required"}
        <p class="mt-3 text-sm text-warning" role="status">
          {tr("guard.backup.recoveryRequired")}
        </p>
        <div class="mt-2 flex flex-wrap gap-2">
          {#if backupJobId && stagedRestore}
            <Button size="sm" variant="outline" onclick={observeOriginalRestore}
              >{tr("guard.backup.checkOriginal")}</Button
            >
          {:else if pendingActivation}
            <Button
              size="sm"
              variant="outline"
              onclick={() => submitActivationRequest(true)}
              >{tr("guard.backup.resumeRequest")}</Button
            >
          {/if}
          {#if stagedRestore && restoreAdmission && recoveryAvailable && !historicalRestore}
            {#if confirmingActivation === "recover"}
              <p class="w-full text-sm">{tr("guard.backup.recoverConfirm")}</p>
              <Button size="sm" onclick={recoverOriginalRestore}
                >{tr("guard.backup.recoverAction")}</Button
              >
              <Button
                size="sm"
                variant="outline"
                onclick={() => (confirmingActivation = null)}
                >{tr("guard.cancel")}</Button
              >
            {:else}
              <Button
                size="sm"
                variant="outline"
                onclick={() => (confirmingActivation = "recover")}
                >{tr("guard.backup.recover")}</Button
              >
            {/if}
          {/if}
        </div>
      {/if}
      {#if backupBusy && backupJobId}
        <p class="mt-1 text-xs text-muted-foreground">
          {tr("guard.backup.job", { id: backupJobId })}
        </p>
      {/if}
    </section>
  {/if}

  {#if notices.length > 0}
    <div
      class="mt-3 rounded-md border border-warning/40 bg-warning/10 p-3 text-xs text-foreground"
      role="note"
      data-testid="guard-notices"
    >
      <p class="font-semibold">{tr("guard.reduced")}</p>
      <ul class="mt-1 list-disc ps-5">
        {#each notices as notice, index (index)}
          <li>{say(notice)}</li>
        {/each}
      </ul>
    </div>
  {/if}

  {#if findings.length > 0}
    <section
      class="mt-5"
      aria-labelledby="guard-findings-title"
      data-testid="guard-findings"
    >
      <h3
        id="guard-findings-title"
        class="text-sm font-semibold text-foreground"
      >
        {tr("guard.findings.title")}
      </h3>
      <ul class="mt-2 flex flex-col gap-2">
        {#each findings as finding, index (finding.control + index)}
          <li
            class="rounded-md border border-border p-3 text-sm"
            data-testid="guard-finding"
          >
            <div class="flex flex-wrap items-center gap-x-3 gap-y-1">
              <span class="font-mono text-xs text-foreground"
                >{finding.control}</span
              >
              <span
                class="inline-flex items-center gap-1 text-xs font-semibold {finding.state ===
                'drifted'
                  ? 'text-warning'
                  : 'text-muted-foreground'}"
              >
                {#if finding.state === "drifted"}
                  <TriangleAlert class="h-3.5 w-3.5" aria-hidden="true" />
                {/if}
                {labelOr(
                  `guard.findings.state.${finding.state}`,
                  finding.state,
                )}
              </span>
              <span class="text-xs text-muted-foreground">
                {tr("guard.findings.severity", {
                  severity: labelOr(
                    `guard.findings.severity.${finding.severity}`,
                    finding.severity,
                  ),
                })}
              </span>
            </div>
            {#if finding.expected || finding.observed}
              <dl class="mt-2 grid gap-x-4 gap-y-1 text-xs sm:grid-cols-2">
                {#if finding.expected}
                  <div>
                    <dt class="text-muted-foreground">
                      {tr("guard.findings.expected")}
                    </dt>
                    <dd class="font-mono text-foreground">
                      {finding.expected}
                    </dd>
                  </div>
                {/if}
                {#if finding.observed}
                  <div>
                    <dt class="text-muted-foreground">
                      {tr("guard.findings.observed")}
                    </dt>
                    <dd class="font-mono text-foreground">
                      {finding.observed}
                    </dd>
                  </div>
                {/if}
              </dl>
            {/if}
            {#if finding.remediation}
              <p class="mt-2 text-xs text-foreground">
                <span class="font-semibold"
                  >{tr("guard.findings.remediation")}</span
                >
                {finding.remediation}
              </p>
            {/if}
            {#if finding.exception}
              <p class="mt-1 text-xs text-muted-foreground">
                {exceptionText(finding)}
              </p>
            {/if}
          </li>
        {/each}
      </ul>
    </section>
  {/if}

  {#if evidence.worker_id}
    <div class="mt-5">
      <GuardOutcomes
        workerId={evidence.worker_id}
        reportedUnresolved={evidence.worker.guard_operations
          ?.unresolved_outcome_unknown ?? 0}
        onResolved={onChanged}
      />
    </div>
  {/if}

  {#if evidence.worker_id}
    <div class="mt-5">
      <GuardIdentityUpgrade
        workerId={evidence.worker_id}
        required={evidence.worker.identity_binding === "legacy_bearer"}
        {onChanged}
      />
    </div>
  {/if}

  {#if server && evidence.reach}
    <div class="mt-5">
      <GuardReachControl
        serverId={server.id}
        reach={evidence.reach}
        {onChanged}
      />
    </div>
  {/if}

  <div
    class="mt-5 flex flex-wrap items-center gap-2"
    data-testid="guard-actions"
  >
    {#if verifyAvailable}
      <Button
        variant="secondary"
        size="sm"
        testId="guard-verify"
        disabled={verifyState.kind === "running"}
        onclick={() => void verifyNow()}
      >
        <RefreshCw
          class="h-4 w-4 {verifyState.kind === 'running' ? 'animate-spin' : ''}"
        />
        {verifyState.kind === "running"
          ? tr("guard.verify.running")
          : tr("guard.verify.action")}
      </Button>
    {/if}
    {#if rotateListed && rotateStep === "idle"}
      <Button
        variant="secondary"
        size="sm"
        testId="guard-rotate"
        disabled={credentialGeneration === undefined}
        onclick={beginRotate}
      >
        <KeyRound class="h-4 w-4" />
        {tr("guard.rotate.action")}
      </Button>
    {/if}
    {#if canDetach && detachStep === "idle"}
      <Button
        variant="destructive"
        size="sm"
        testId="guard-detach"
        onclick={() => (detachStep = "confirming")}
      >
        {tr("guard.detach.action")}
      </Button>
    {/if}
    <a
      href="/dashboard"
      data-kx="control"
      class="inline-flex items-center gap-2 rounded-lg px-3 py-1.5 text-sm font-medium"
      data-testid="guard-connect-link"
    >
      <Users class="h-4 w-4" aria-hidden="true" />
      {tr("guard.connect.link")}
    </a>
    <Button
      variant="secondary"
      size="sm"
      testId="guard-report-download"
      disabled={reportBusy || !server}
      onclick={() => void downloadReport()}
    >
      <Download class="h-4 w-4" />
      {tr("guard.report.download")}
    </Button>
    <Button
      variant="secondary"
      size="sm"
      testId="guard-report-print"
      disabled={reportBusy || !server}
      onclick={() => void openPrintable()}
    >
      <Printer class="h-4 w-4" />
      {tr("guard.report.print")}
    </Button>
  </div>

  {#if rotateListed && rotateStep === "idle" && credentialGeneration === undefined}
    <p class="mt-2 text-xs text-muted-foreground">
      {tr("guard.rotate.generationUnknown")}
    </p>
  {/if}

  <div aria-live="polite">
    {#if verifyState.kind === "accepted"}
      <p class="mt-3 text-xs text-foreground" data-testid="guard-verify-result">
        {tr("guard.verify.accepted", {
          job: verifyState.job,
          at: formatTime(verifyState.at),
        })}
      </p>
    {:else if verifyState.kind === "error"}
      <p class="mt-3 text-xs text-destructive" role="alert">
        {tr("guard.verify.failed", { message: verifyState.message })}
      </p>
    {/if}
    {#if reportError}
      <p class="mt-3 text-xs text-destructive" role="alert">{reportError}</p>
    {/if}
  </div>

  {#if rotateStep === "confirming" || rotateStep === "running"}
    <div
      class="mt-4 rounded-md border border-warning/50 bg-warning/10 p-3 text-xs text-foreground"
      role="alertdialog"
      aria-label={tr("guard.rotate.confirmTitle")}
      data-testid="guard-rotate-confirm"
    >
      <p class="font-semibold">{tr("guard.rotate.confirmTitle")}</p>
      <p class="mt-1">{tr("guard.rotate.confirmBody")}</p>
      {#if rotateError}
        <p class="mt-2 text-destructive" role="alert">{rotateError}</p>
      {/if}
      <div class="mt-3 flex flex-wrap gap-2">
        <Button
          variant="primary"
          size="sm"
          testId="guard-rotate-confirm-button"
          disabled={rotateStep === "running"}
          onclick={() => void confirmRotate()}
        >
          {rotateStep === "running"
            ? tr("guard.rotate.running")
            : tr("guard.rotate.confirm")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={rotateStep === "running"}
          onclick={() => {
            rotateStep = "idle";
            rotateError = null;
          }}
        >
          {tr("guard.cancel")}
        </Button>
      </div>
    </div>
  {/if}

  {#if rotateStep === "done" && rotation}
    <div
      class="mt-4 rounded-md border border-success/40 bg-success/10 p-3 text-xs text-foreground"
      role="status"
      data-testid="guard-rotate-result"
    >
      <p class="font-semibold">
        {tr("guard.rotate.done", {
          generation: rotation.credential_generation,
        })}
      </p>
      {#if rotation.agent_token}
        <p class="mt-1">{tr("guard.rotate.tokenOnce")}</p>
        <code
          class="mt-2 block break-all rounded bg-background p-2 font-mono text-[11px]"
          data-testid="guard-rotate-token">{rotation.agent_token}</code
        >
        <div class="mt-2 flex flex-wrap gap-2">
          <Button
            variant="secondary"
            size="sm"
            onclick={() => void copyToken()}
          >
            {tokenCopied ? tr("guard.rotate.copied") : tr("guard.rotate.copy")}
          </Button>
          <Button variant="primary" size="sm" onclick={dismissRotation}>
            {tr("guard.rotate.dismiss")}
          </Button>
        </div>
      {:else}
        <div class="mt-2">
          <Button variant="primary" size="sm" onclick={dismissRotation}>
            {tr("guard.rotate.dismiss")}
          </Button>
        </div>
      {/if}
    </div>
  {/if}

  {#if detachStep !== "idle"}
    <div
      class="mt-4 rounded-md border border-destructive/40 bg-destructive/10 p-3 text-xs text-foreground"
      role="alertdialog"
      aria-label={tr("guard.detach.confirmTitle", { name: serverName })}
      data-testid="guard-detach-confirm"
    >
      <p class="font-semibold">
        {tr("guard.detach.confirmTitle", { name: serverName })}
      </p>
      <p class="mt-1">{tr("guard.detach.confirmBody")}</p>
      <p class="mt-1 text-muted-foreground">{tr("guard.detach.reverse")}</p>
      {#if detachError}
        <p class="mt-2 text-destructive" role="alert">{detachError}</p>
      {/if}
      <div class="mt-3 flex flex-wrap gap-2">
        <Button
          variant="destructive"
          size="sm"
          testId="guard-detach-confirm-button"
          disabled={detachStep === "running"}
          onclick={() => void confirmDetach()}
        >
          {detachStep === "running"
            ? tr("guard.detach.running")
            : tr("guard.detach.confirm")}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          disabled={detachStep === "running"}
          onclick={() => {
            detachStep = "idle";
            detachError = null;
          }}
        >
          {tr("guard.cancel")}
        </Button>
      </div>
    </div>
  {/if}
</section>
