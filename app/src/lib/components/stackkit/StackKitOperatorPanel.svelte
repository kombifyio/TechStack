<script lang="ts">
  import { onDestroy } from "svelte";
  import Button from "#lib/components/ui/Button.svelte";
  import type { CanonicalServer } from "#lib/api/registry.js";
  import {
    runStackKitLifecycleOperation,
    type StackKitLifecycleRequest,
  } from "#lib/api/stacks.js";
  import { getJob, streamJobProgress, type JobDetail } from "#lib/api/jobs.js";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    serverLifecycleActions,
    type ServerLifecycleAction,
  } from "#lib/server-lifecycle-actions.js";
  import { operatorMessage as m } from "#lib/stackkit-operator-messages.js";
  import { stateLabel } from "#lib/i18n.svelte.js";
  let {
    server,
    stackId,
    agentId,
    disabled = false,
    onChanged,
    onBusy,
  }: {
    server: CanonicalServer | null;
    stackId: string;
    agentId: string;
    disabled?: boolean;
    onChanged?: () => void | Promise<void>;
    onBusy?: (busy: boolean) => void;
  } = $props();
  const actions = $derived(serverLifecycleActions(server ?? undefined));
  let selected = $state<ServerLifecycleAction | null>(null);
  let candidate = $state("");
  let reference = $state("");
  let submitting = $state(false);
  let error = $state("");
  let jobId = $state("");
  let operation = $state<StackKitLifecycleRequest["operation"] | null>(null);
  let job = $state<JobDetail | null>(null);
  let observing = $state(false);
  let closeStream = () => {};
  let generation = 0;
  const terminal = $derived(
    Boolean(job && ["completed", "failed", "cancelled"].includes(job.state)),
  );
  const busy = $derived(submitting || Boolean(jobId && !terminal));
  function record(value: unknown): Record<string, unknown> | undefined {
    if (typeof value === "string") {
      try {
        return record(JSON.parse(value));
      } catch {
        return;
      }
    }
    return value && typeof value === "object" && !Array.isArray(value)
      ? (value as Record<string, unknown>)
      : undefined;
  }
  const result = $derived(record(job?.result));
  const matching = $derived(
    result?.schema_version === "techstack.stackkit-lifecycle-result/v1" &&
      result.techstack_id === stackId &&
      result.agent_id === agentId &&
      result.operation === operation,
  );
  const confirmed = $derived(
    job?.state === "completed" && matching && result?.success === true,
  );
  $effect(() => {
    onBusy?.(busy);
  });
  const scopeKey = $derived(
    JSON.stringify([server?.id ?? "", stackId, agentId]),
  );
  let observedScope: string | null = null;
  $effect(() => {
    const scope = scopeKey;
    if (scope === observedScope) return;
    observedScope = scope;
    generation++;
    closeStream();
    selected = null;
    jobId = "";
    job = null;
    error = "";
    submitting = false;
    observing = false;
  });
  onDestroy(() => {
    generation++;
    closeStream();
    onBusy?.(false);
  });
  function choose(action: ServerLifecycleAction) {
    if (busy || disabled) return;
    error = "";
    selected = action;
    candidate = "";
    reference = "";
    if (!action.mutates) void submit(action);
  }
  async function observe(id = jobId, attempt = generation) {
    if (!id || observing) return;
    observing = true;
    try {
      const next = await getJob(id);
      if (attempt !== generation) return;
      if (next.id !== id || next.kit_deployment_id !== stackId)
        throw new Error(m("scopeMismatch"));
      job = next;
      if (["completed", "failed", "cancelled"].includes(next.state)) {
        closeStream();
        await onChanged?.();
      }
    } catch (cause) {
      if (attempt === generation) error = parseApiError(cause).message;
    } finally {
      if (attempt === generation) observing = false;
    }
  }
  async function submit(action: ServerLifecycleAction) {
    if (
      !server ||
      !stackId ||
      !agentId ||
      busy ||
      disabled ||
      !actions.some((a) => a.operation === action.operation)
    )
      return;
    const request: StackKitLifecycleRequest = {
      operation: action.operation,
      agent_id: agentId,
      ...(action.mutates ? { owner_approved: true } : {}),
      ...(action.operation === "upgrade" ? { target_release: "latest" } : {}),
    };
    try {
      if (action.operation === "advanced_change_set") {
        const parsed: unknown = JSON.parse(candidate);
        if (!parsed || typeof parsed !== "object" || Array.isArray(parsed))
          throw new Error(m("invalid"));
        request.candidate_spec = parsed as Record<string, unknown>;
      }
      const digest = reference.trim();
      if (
        action.operation === "rollback" ||
        (action.operation === "restore_drill" && digest)
      ) {
        if (!/^sha256:[a-f0-9]{64}$/.test(digest))
          throw new Error(m("invalid"));
        if (action.operation === "rollback")
          request.rollback_target_ref = digest;
        else request.snapshot_anchor_id = digest;
      }
    } catch {
      error = m("invalid");
      return;
    }
    submitting = true;
    error = "";
    const attempt = generation;
    try {
      const accepted = await runStackKitLifecycleOperation(stackId, request);
      if (attempt !== generation) return;
      if (
        accepted.kit_deployment_id !== stackId ||
        accepted.agent_id !== agentId ||
        accepted.operation !== action.operation
      )
        throw new Error(m("scopeMismatch"));
      selected = null;
      job = null;
      jobId = accepted.job_id;
      operation = action.operation;
      closeStream();
      closeStream = streamJobProgress(jobId, {
        onEvent: () => void observe(accepted.job_id, attempt),
        onFallback: () => void observe(accepted.job_id, attempt),
      });
      await observe(jobId, attempt);
    } catch (cause) {
      if (attempt === generation) error = parseApiError(cause).message;
    } finally {
      if (attempt === generation) submitting = false;
    }
  }
</script>

<section data-kx="plate" class="p-5" data-testid="server-lifecycle-actions">
  <h2 class="text-lg font-semibold">{m("title")}</h2>
  {#if actions.length === 0}<p class="mt-3 text-sm text-muted-foreground">
      {m("unavailable")}
    </p>{/if}
  <div class="mt-4 grid gap-3 sm:grid-cols-2">
    {#each actions as action (action.operation)}
      <button
        type="button"
        data-kx="control"
        class="rounded-lg p-3 text-start disabled:opacity-50"
        data-testid={`server-lifecycle-${action.operation}`}
        disabled={busy || disabled}
        onclick={() => choose(action)}
      >
        <span class="block text-sm font-medium">{action.label}</span><span
          class="mt-1 block text-xs text-muted-foreground"
          >{action.description}</span
        >
      </button>
    {/each}
  </div>
  {#if selected?.mutates}
    <div
      class="mt-4 rounded-lg border border-warning/40 p-4"
      data-testid="server-lifecycle-confirmation"
    >
      <h3 class="font-medium">{selected.label} · {server?.name}</h3>
      <p class="mt-2 text-sm">{m("approval")}</p>
      {#if selected.operation === "advanced_change_set"}
        <label class="mt-3 block text-sm"
          >{m("candidate")}<textarea
            bind:value={candidate}
            rows="9"
            class="mt-1 w-full rounded border border-input bg-background p-2 font-mono text-xs"
            disabled={submitting}
            data-testid="operator-candidate-spec"
          ></textarea></label
        >
      {:else if selected.operation === "rollback" || selected.operation === "restore_drill"}
        <label class="mt-3 block text-sm"
          >{m(selected.operation === "rollback" ? "target" : "anchor")}<input
            bind:value={reference}
            class="mt-1 w-full rounded border border-input bg-background p-2 font-mono text-xs"
            disabled={submitting}
            data-testid="operator-target-reference"
          /></label
        >
      {/if}
      <div class="mt-3 flex gap-2">
        <Button
          disabled={busy || disabled}
          onclick={() => selected && submit(selected)}
          testId="server-lifecycle-confirm-button"
          >{submitting ? m("running") : m("confirm")}</Button
        ><Button
          variant="secondary"
          disabled={submitting}
          onclick={() => (selected = null)}>{m("cancel")}</Button
        >
      </div>
    </div>
  {/if}
  {#if error}<p class="mt-3 text-sm text-destructive" role="alert">
      {error}
    </p>{/if}
  {#if jobId}
    <div
      class="mt-4 text-sm"
      role="status"
      data-testid="server-lifecycle-outcome"
    >
      <p>
        {confirmed ? m("recorded") : terminal ? m("unverified") : m("accepted")}
      </p>
      <p class="mt-1 font-mono text-xs">
        {jobId} · {stateLabel(job?.state ?? "queued")}
      </p>
      {#if job?.error}<p class="mt-2 text-destructive">{job.error}</p>{/if}
      {#if job?.user_guidance?.body}<p class="mt-2">
          {job.user_guidance.body}
        </p>{/if}
      {#if matching && typeof result?.change_set_id === "string"}<p
          class="mt-2 break-all font-mono text-xs"
        >
          {result.change_set_id}
        </p>{/if}
      <Button
        variant="secondary"
        size="sm"
        class="mt-2"
        disabled={observing}
        onclick={() => void observe()}>{m("observe")}</Button
      >
    </div>
  {/if}
</section>
