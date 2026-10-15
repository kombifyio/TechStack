<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { RefreshCw, Wrench } from "@lucide/svelte";
  import { tr, formatDateTime, stateLabel } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { denialMessage, parseApiError } from "#lib/api/errors.js";
  import {
    getMaintenanceImpact,
    requestMaintenance,
    getMaintenanceJob,
    cancelMaintenanceJob,
    type MaintenanceAction,
    type MaintenanceImpact,
    type MaintenanceJob,
    type MaintenanceRequest,
  } from "#lib/api/server-maintenance.js";

  interface Props {
    serverId: string;
    serverName: string;
    allowedActions: string[];
    disabled?: boolean;
    onChanged?: () => void;
  }
  interface PendingRequest {
    request: MaintenanceRequest;
    key: string;
  }
  interface Receipt {
    pending: PendingRequest | null;
    jobId: string | null;
  }
  let {
    serverId,
    serverName,
    allowedActions,
    disabled = false,
    onChanged,
  }: Props = $props();
  let action = $state<MaintenanceAction | null>(null);
  let impact = $state<MaintenanceImpact | null>(null);
  let plan = $state<MaintenanceJob | null>(null);
  let job = $state<MaintenanceJob | null>(null);
  let jobId = $state<string | null>(null);
  let pending = $state<PendingRequest | null>(null);
  let busy = $state(false);
  let error = $state<string | null>(null);
  let notice = $state<string | null>(null);
  let generation = 0;
  let jobReadSequence = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  const inFlight = $derived(
    Boolean(
      jobId &&
      (!job || !["completed", "failed", "cancelled"].includes(job.state)),
    ),
  );
  const blocked = $derived(disabled || busy || inFlight || Boolean(pending));
  const planDigest = $derived(plan?.result.plan_digest || plan?.plan_digest);
  const confirmable = $derived(
    Boolean(
      action &&
      allowedActions.includes(action) &&
      impact?.confirmable &&
      impact.inventory.status === "current" &&
      impact.server_id === serverId &&
      impact.action === action &&
      (action !== "os_update" || planDigest),
    ),
  );

  function persist() {
    try {
      sessionStorage.setItem(
        `maintenance:${serverId}`,
        JSON.stringify({ pending, jobId }),
      );
    } catch {
      /* Continue with the current receipt when storage is unavailable. */
    }
  }

  function restore() {
    try {
      const saved: Receipt | null = JSON.parse(
        sessionStorage.getItem(`maintenance:${serverId}`) || "null",
      );
      if (saved && typeof saved.jobId === "string") jobId = saved.jobId;
      if (
        saved?.pending &&
        typeof saved.pending.key === "string" &&
        saved.pending.request?.expected_inventory_revision > 0 &&
        ["os_update_plan", "os_update", "reboot"].includes(
          saved.pending.request.action,
        )
      )
        pending = saved.pending;
    } catch {
      /* A broken saved receipt never grants permission to act. */
    }
    if (pending) notice = tr("maintenance.uncertain");
    if (jobId) void followJob();
  }

  async function followJob() {
    clearTimeout(timer);
    const mine = generation;
    const read = ++jobReadSequence;
    const id = jobId;
    if (!id) return;
    try {
      const next = await getMaintenanceJob(serverId, id);
      if (mine !== generation || id !== jobId || read !== jobReadSequence)
        return;
      // Settled receipts remain settled even if a read returns older state.
      if (
        job &&
        ["completed", "failed", "cancelled"].includes(job.state) &&
        !["completed", "failed", "cancelled"].includes(next.state)
      )
        return;
      job = next;
      error = null;
      if (next.action === "os_update_plan" && next.state === "completed") {
        plan = next;
        if (action === "os_update") await refreshImpact();
      }
      if (
        ["impact_changed", "impact_unavailable"].includes(
          next.reason_code || "",
        ) &&
        (next.action === "reboot" || next.action === "os_update")
      ) {
        action = next.action;
        notice = tr("maintenance.changed");
        await refreshImpact();
      }
      if (["completed", "failed", "cancelled"].includes(next.state))
        if (read === jobReadSequence && mine === generation) onChanged?.();
    } catch (cause) {
      if (mine !== generation || read !== jobReadSequence) return;
      error = denialMessage(parseApiError(cause));
    }
    if (
      mine === generation &&
      read === jobReadSequence &&
      id === jobId &&
      (!job || !["completed", "failed", "cancelled"].includes(job.state))
    )
      timer = setTimeout(() => void followJob(), 5000);
  }

  async function refreshImpact(): Promise<MaintenanceImpact | null> {
    const selected = action;
    const mine = generation;
    if (!selected) return null;
    try {
      const next = await getMaintenanceImpact(serverId, selected);
      if (mine !== generation || action !== selected) return null;
      impact = next;
      return next;
    } catch (cause) {
      if (mine !== generation) return null;
      impact = null;
      error = denialMessage(parseApiError(cause));
      return null;
    }
  }

  async function preview(selected: MaintenanceAction) {
    if (blocked) return;
    const mine = generation;
    action = selected;
    if (selected === "os_update") plan = null;
    impact = null;
    error = null;
    notice = null;
    busy = true;
    try {
      const next = await refreshImpact();
      if (mine !== generation || selected !== action) return;
      if (selected === "os_update" && next?.confirmable) {
        pending = {
          key: crypto.randomUUID(),
          request: {
            action: "os_update_plan",
            expected_inventory_revision: next.inventory_revision,
            owner_approved: false,
          },
        };
        persist();
        await submitPending(mine);
      }
    } finally {
      if (mine === generation) busy = false;
    }
  }

  function cancelPreview() {
    if (busy || pending || inFlight) return;
    action = null;
    impact = null;
    notice = null;
    error = null;
  }

  async function submitPending(mine = generation) {
    if (!pending || mine !== generation) return;
    const attempt = pending;
    try {
      const accepted = await requestMaintenance(
        serverId,
        attempt.request,
        attempt.key,
      );
      if (mine !== generation) return;
      jobId = accepted.job_id;
      job = null;
      pending = null;
      if (attempt.request.action !== "os_update_plan") {
        action = null;
        impact = null;
      }
      notice = null;
      persist();
      await followJob();
    } catch (cause) {
      if (mine !== generation) return;
      const parsed = parseApiError(cause);
      error = denialMessage(parsed);
      // Timeouts, disconnected transport and server errors may follow admission.
      // Keep both the body and key; the next click replays this exact attempt.
      if (parsed.status === 0 || parsed.status >= 500) {
        notice = tr("maintenance.uncertain");
      } else {
        pending = null;
        persist();
        if (
          attempt.request.action !== "os_update_plan" &&
          parsed.status === 409
        ) {
          notice = tr("maintenance.changed");
          const reason =
            parsed.outcome?.reasonCode ||
            (typeof parsed.details === "object" &&
            parsed.details !== null &&
            "reason" in parsed.details
              ? parsed.details.reason
              : parsed.code);
          if (reason === "plan_stale") plan = null;
          await refreshImpact();
        }
      }
    }
  }

  async function retry() {
    if (busy || !pending) return;
    const mine = generation;
    busy = true;
    error = null;
    try {
      await submitPending(mine);
    } finally {
      if (mine === generation) busy = false;
    }
  }

  async function confirm() {
    if (blocked || !confirmable || !action || !impact) return;
    const mine = generation;
    const selected = action;
    busy = true;
    error = null;
    try {
      const previous = impact;
      const next = await getMaintenanceImpact(serverId, selected);
      if (mine !== generation || selected !== action) return;
      impact = next;
      if (
        !next.confirmable ||
        next.server_id !== serverId ||
        next.action !== selected ||
        next.inventory.status !== "current" ||
        next.snapshot_digest !== previous.snapshot_digest
      ) {
        notice = tr("maintenance.changed");
        return;
      }
      pending = {
        key: crypto.randomUUID(),
        request: {
          action: selected,
          expected_inventory_revision: next.inventory_revision,
          expected_impact_digest: next.snapshot_digest,
          owner_approved: true,
          ...(selected === "os_update"
            ? { expected_plan_digest: planDigest }
            : {}),
        },
      };
      persist();
      await submitPending(mine);
    } catch (cause) {
      if (mine === generation) error = denialMessage(parseApiError(cause));
    } finally {
      if (mine === generation) busy = false;
    }
  }

  async function cancelJob() {
    if (busy || !jobId || job?.state !== "queued") return;
    const mine = generation;
    jobReadSequence++;
    clearTimeout(timer);
    busy = true;
    try {
      await cancelMaintenanceJob(serverId, jobId);
    } catch (cause) {
      if (mine === generation) error = denialMessage(parseApiError(cause));
    } finally {
      if (mine === generation) {
        await followJob();
        if (mine === generation) busy = false;
      }
    }
  }

  $effect(() => {
    void serverId;
    untrack(() => {
      generation++;
      clearTimeout(timer);
      action = null;
      impact = null;
      plan = null;
      job = null;
      jobId = null;
      pending = null;
      busy = false;
      error = null;
      notice = null;
      restore();
    });
  });
  onDestroy(() => {
    generation++;
    clearTimeout(timer);
  });
</script>

<section data-kx="plate" class="p-5" data-testid="server-maintenance-card">
  <div class="flex items-center gap-2">
    <Wrench class="h-5 w-5 text-primary" />
    <h2 class="text-lg font-semibold">{tr("maintenance.title")}</h2>
  </div>
  <p class="mt-2 text-sm text-muted-foreground">{tr("maintenance.intro")}</p>
  <div class="mt-4 flex flex-wrap gap-2">
    {#if allowedActions.includes("os_update") && allowedActions.includes("os_update_plan")}
      <Button
        variant="secondary"
        onclick={() => preview("os_update")}
        disabled={blocked}
        testId="maintenance-preview-update">{tr("maintenance.update")}</Button
      >
    {/if}
    {#if allowedActions.includes("reboot")}
      <Button
        variant="secondary"
        onclick={() => preview("reboot")}
        disabled={blocked}
        testId="maintenance-preview-reboot">{tr("maintenance.reboot")}</Button
      >
    {/if}
    {#if !allowedActions.includes("os_update") && !allowedActions.includes("reboot")}<p
        class="text-sm text-muted-foreground"
      >
        {tr("maintenance.unavailable")}
      </p>{/if}
  </div>
  {#if notice}<p class="mt-4 text-sm text-foreground" role="status">
      {notice}
    </p>{/if}
  {#if error}<p class="mt-4 text-sm text-destructive" role="alert">
      {error}
    </p>{/if}
  {#if pending}<Button
      class="mt-3"
      variant="secondary"
      onclick={retry}
      disabled={busy}
      testId="maintenance-retry">{tr("maintenance.retry")}</Button
    >{/if}
  {#if jobId && !job && error}
    <Button
      class="mt-3"
      variant="secondary"
      onclick={() => void followJob()}
      disabled={busy}
    >
      {tr("maintenance.retry")}
    </Button>
  {/if}
  {#if busy}<p class="mt-3 text-sm text-muted-foreground" role="status">
      {tr("maintenance.loading")}
    </p>{/if}
  {#if action && impact}
    <div
      class="mt-4 space-y-4 rounded-lg border border-border p-4"
      data-testid="maintenance-impact"
    >
      <p class="font-medium">
        {tr("maintenance.target", {
          server: serverName,
          action: tr(
            `maintenance.${action === "reboot" ? "reboot" : "update"}`,
          ),
        })}
      </p>
      <p class="text-sm text-muted-foreground">
        {tr(
          action === "reboot"
            ? "maintenance.rebootImpact"
            : "maintenance.updateImpact",
        )}
      </p>
      <p class="text-sm" data-testid="maintenance-inventory-status">
        {tr(`maintenance.inventory.${impact.inventory.status}`)}
      </p>
      {#if impact.inventory.status !== "current"}
        <p
          class="rounded-lg border border-warning/40 bg-warning/10 p-3 text-sm"
          role="alert"
          data-testid="maintenance-incomplete"
        >
          {tr("maintenance.incomplete")}
        </p>
      {:else if impact.services.length === 0}
        <p class="text-sm" data-testid="maintenance-no-impact">
          {tr(
            impact.stacks.length === 0
              ? "maintenance.noImpact"
              : "maintenance.noServices",
          )}
        </p>
      {/if}
      {#if impact.inventory.observed_at}<p
          class="text-xs text-muted-foreground"
        >
          {tr("maintenance.observed", {
            when: formatDateTime(impact.inventory.observed_at),
          })}
        </p>{/if}
      {#if impact.stacks.length}
        <div>
          <h3 class="text-sm font-medium">{tr("maintenance.stacks")}</h3>
          <p class="text-sm text-muted-foreground">
            {tr("maintenance.stacksPlacement")}
          </p>
          <ul class="mt-2 space-y-1">
            {#each impact.stacks as stack (stack.id)}<li class="text-sm">
                {stack.name || stack.id}
              </li>{/each}
          </ul>
        </div>
      {/if}
      {#if impact.services.length}
        <div>
          <h3 class="text-sm font-medium">{tr("maintenance.services")}</h3>
          <p class="text-sm text-muted-foreground">
            {tr("maintenance.servicesPlacement")}
          </p>
          <ul class="mt-2 space-y-2">
            {#each impact.services as service (service.id)}<li class="text-sm">
                <span class="font-medium">{service.name || service.id}</span
                ><span class="ml-2 text-muted-foreground"
                  >{stateLabel(service.state)}</span
                >{#if service.locked}<span class="ml-2 text-destructive"
                    >{tr("maintenance.locked")}</span
                  >{/if}
              </li>{/each}
          </ul>
        </div>
      {/if}
      {#if action === "os_update" && plan?.state === "completed"}
        <p class="text-sm">
          {tr("maintenance.plan", {
            pending: String(plan.result.pending_count ?? 0),
            security: String(plan.result.security_count ?? 0),
          })}
        </p>
        {#if plan.result.reboot_likely}<p class="text-sm text-muted-foreground">
            {tr("maintenance.rebootSeparate")}
          </p>{/if}
        {#if plan.result.packages?.length}<details>
            <summary class="cursor-pointer text-sm"
              >{tr("maintenance.packages")}</summary
            >
            <ul class="mt-2 space-y-1 text-xs">
              {#each plan.result.packages as pkg (pkg.name)}<li>
                  {pkg.name}: {pkg.from} → {pkg.to}
                </li>{/each}
            </ul>
            {#if plan.result.packages_truncated}<p
                class="mt-2 text-xs text-muted-foreground"
              >
                {tr("maintenance.packagesTruncated")}
              </p>{/if}
          </details>{/if}
      {/if}
      {#if !impact.confirmable && impact.inventory.status === "current"}<p
          class="text-sm text-muted-foreground"
        >
          {tr("maintenance.blocked")}
        </p>{/if}
      <p class="text-sm text-muted-foreground">
        {tr("maintenance.confirmScope")}
      </p>
      <div class="flex flex-wrap gap-2">
        <Button
          onclick={confirm}
          disabled={blocked || !confirmable}
          testId="maintenance-confirm">{tr("maintenance.confirm")}</Button
        >
        <Button
          variant="secondary"
          onclick={cancelPreview}
          disabled={busy || Boolean(pending) || inFlight}
          testId="maintenance-cancel-preview">{tr("maintenance.cancel")}</Button
        >
        <Button
          variant="ghost"
          onclick={() => void refreshImpact()}
          disabled={busy || Boolean(pending) || inFlight}
          testId="maintenance-refresh"
          ><RefreshCw class="h-4 w-4" />{tr("maintenance.refresh")}</Button
        >
      </div>
    </div>
  {/if}
  {#if job}
    <div
      class="mt-4 rounded-lg border border-border p-4"
      role="status"
      data-testid="maintenance-job"
      data-state={job.state}
    >
      <p class="text-sm font-medium">{tr(`maintenance.state.${job.state}`)}</p>
      {#if job.failure}<p class="mt-2 text-sm">
          {job.failure.user_guidance.body}
        </p>{/if}
      {#if job.result.reboot_required}<p class="mt-2 text-sm">
          {tr("maintenance.rebootSeparate")}
        </p>{/if}
      {#if job.state === "queued"}<Button
          class="mt-3"
          variant="secondary"
          onclick={cancelJob}
          disabled={busy}
          testId="maintenance-cancel-job">{tr("maintenance.cancelJob")}</Button
        >{/if}
      {#if inFlight && error}<Button
          class="mt-3"
          variant="secondary"
          onclick={() => void followJob()}
          disabled={busy}>{tr("maintenance.refresh")}</Button
        >{/if}
    </div>
  {/if}
</section>
