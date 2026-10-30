<script lang="ts">
  import { onMount } from "svelte";
  import { ArrowLeft } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    getUseCaseConfiguration,
    type UseCaseConfigurationView,
  } from "#lib/api/useCaseConfiguration.js";
  import {
    removeInstalledUseCase,
    type UseCaseRemovalAccepted,
    type UseCaseRemovalRequest,
    type UseCaseRemovalSelection,
  } from "#lib/api/useCaseRemoval.js";
  import {
    mintExtendWizardIdempotencyKey,
    clearExtendWizardIdempotencyKey,
  } from "#lib/wizard/idempotency-keys.js";
  import Button from "#lib/components/ui/Button.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import UseCaseRunProgress from "./UseCaseRunProgress.svelte";

  let { stackId }: { stackId: string } = $props();
  type Failure = { stale: boolean; details: string };
  type RemovalState =
    | { kind: "loading" }
    | { kind: "load_failed" }
    | { kind: "ready"; view: UseCaseConfigurationView; failure?: Failure }
    | { kind: "submitting"; view: UseCaseConfigurationView }
    | { kind: "accepted"; result: UseCaseRemovalAccepted; label: string };
  let removalState = $state<RemovalState>({ kind: "loading" });
  let scope = $state<UseCaseRemovalSelection["selector_kind"]>("use_case");
  let useCaseId = $state("");
  let workloadRef = $state("");
  let confirmed = $state(false);
  let controller: AbortController | undefined;
  const view = $derived(
    removalState.kind === "ready" || removalState.kind === "submitting"
      ? removalState.view
      : null,
  );
  const group = $derived(
    view?.use_cases.find((entry) => entry.id === useCaseId),
  );
  const members = $derived(
    scope === "use_case"
      ? (group?.workloads ?? [])
      : (group?.workloads.filter(
          (member) => member.workload_ref === workloadRef,
        ) ?? []),
  );
  const eligible = $derived(
    Boolean(
      view?.retained_removal_available &&
      view.expected_spec_hash &&
      members.length &&
      members.every((member) => member.removal_eligible === true),
    ),
  );
  const stale = $derived(
    removalState.kind === "ready" && Boolean(removalState.failure?.stale),
  );
  const canSubmit = $derived(
    removalState.kind === "ready" && eligible && confirmed && !stale,
  );

  onMount(() => {
    void load();
    return () => controller?.abort();
  });

  async function load() {
    controller?.abort();
    const attempt = new AbortController();
    controller = attempt;
    removalState = { kind: "loading" };
    confirmed = false;
    try {
      const next = await getUseCaseConfiguration(stackId, attempt.signal);
      if (attempt.signal.aborted) return;
      const selected =
        next.use_cases.find((entry) => entry.id === useCaseId) ??
        next.use_cases[0];
      useCaseId = selected?.id ?? "";
      workloadRef =
        selected?.workloads.find(
          (member) => member.workload_ref === workloadRef,
        )?.workload_ref ??
        selected?.workloads[0]?.workload_ref ??
        "";
      removalState = { kind: "ready", view: next };
    } catch {
      if (!attempt.signal.aborted) removalState = { kind: "load_failed" };
    }
  }

  function selectGroup(id: string) {
    useCaseId = id;
    workloadRef =
      view?.use_cases.find((entry) => entry.id === id)?.workloads[0]
        ?.workload_ref ?? "";
    confirmed = false;
  }
  function selectScope(value: string) {
    if (value !== "workload" && value !== "use_case") return;
    scope = value;
    confirmed = false;
  }

  async function remove() {
    if (!canSubmit || !view || !group) return;
    const currentView = view;
    const request: UseCaseRemovalRequest = {
      selector_kind: scope,
      selector_ref: scope === "use_case" ? group.id : workloadRef,
      expected_spec_hash: currentView.expected_spec_hash,
    };
    const expectedWorkloadRefs = members.map((member) => member.workload_ref);
    const label = scope === "use_case" ? group.name : workloadRef;
    const keyScope = `remove:${stackId}:${request.selector_kind}:${request.selector_ref}`;
    const key = mintExtendWizardIdempotencyKey(
      keyScope,
      JSON.stringify(request),
    );
    removalState = { kind: "submitting", view: currentView };
    try {
      const result = await removeInstalledUseCase({
        stackId,
        request,
        expectedWorkloadRefs,
        idempotencyKey: key,
      });
      clearExtendWizardIdempotencyKey(keyScope);
      removalState = { kind: "accepted", result, label };
    } catch (cause) {
      const error = parseApiError(cause);
      const details = error.details;
      const baselineChanged =
        error.status === 409 &&
        typeof details === "object" &&
        details !== null &&
        "reason_code" in details &&
        details.reason_code === "removal_baseline_changed";
      removalState = {
        kind: "ready",
        view: currentView,
        failure: { stale: baselineChanged, details: error.message },
      };
    }
  }
</script>

<section
  class="mx-auto w-full max-w-3xl space-y-6 p-6 md:p-8"
  data-testid="remove-use-case-page"
  data-stack-id={stackId}
>
  <a
    href={`/stacks/${encodeURIComponent(stackId)}`}
    class="inline-flex items-center gap-2 text-sm text-primary hover:underline"
    ><ArrowLeft size={16} aria-hidden="true" />{tr("removeUseCase.back")}</a
  >
  <header class="space-y-2">
    <h1 class="text-2xl font-semibold tracking-tight text-foreground">
      {tr("removeUseCase.title")}
    </h1>
    <p class="max-w-prose text-sm text-muted-foreground">
      {tr("removeUseCase.body")}
    </p>
  </header>
  {#if removalState.kind === "loading"}
    <LoadingNote label={tr("common.loading")} />
  {:else if removalState.kind === "load_failed"}
    <ErrorCallout
      message={tr("removeUseCase.loadFailed")}
      onRetry={load}
      testId="remove-load-error"
    />
  {:else if removalState.kind === "accepted"}
    <section
      class="space-y-4 rounded-xl border border-border bg-card p-5"
      data-testid="remove-result"
      data-run-id={removalState.result.run_id}
    >
      <h2 class="font-semibold text-foreground">{removalState.label}</h2>
      <p class="text-sm text-muted-foreground">
        {tr("removeUseCase.accepted")}
      </p>
      <p class="text-sm text-muted-foreground">
        {tr("removeUseCase.keepData")}
      </p>
      <UseCaseRunProgress {stackId} jobIds={removalState.result.job_ids} />
      <Button variant="secondary" onclick={load} testId="remove-review"
        >{tr("removeUseCase.review")}</Button
      >
    </section>
  {:else if view && !view.use_cases.length}
    <p
      class="rounded-xl border border-dashed border-border p-5 text-sm text-muted-foreground"
      data-testid="remove-empty"
    >
      {tr("removeUseCase.empty")}
    </p>
  {:else if view}
    {#if !view.retained_removal_available}
      <ErrorCallout
        tone="info"
        message={tr("removeUseCase.unsupported")}
        testId="remove-unsupported"
      />
    {/if}
    <form
      class="space-y-6"
      data-testid="remove-form"
      onsubmit={(event) => {
        event.preventDefault();
        void remove();
      }}
    >
      <fieldset disabled={removalState.kind === "submitting"} class="space-y-4">
        <label class="block space-y-1.5 text-sm font-medium text-foreground">
          <span class="block">{tr("removeUseCase.useCase")}</span>
          <select
            class="w-full rounded-lg border border-border bg-background px-3 py-2"
            value={useCaseId}
            onchange={(event) => selectGroup(event.currentTarget.value)}
            data-testid="remove-use-case"
          >
            {#each view.use_cases as entry (entry.id)}<option value={entry.id}
                >{entry.name}</option
              >{/each}
          </select>
        </label>
        <label class="block space-y-1.5 text-sm font-medium text-foreground">
          <span class="block">{tr("removeUseCase.scope")}</span>
          <select
            class="w-full rounded-lg border border-border bg-background px-3 py-2"
            value={scope}
            onchange={(event) => selectScope(event.currentTarget.value)}
            data-testid="remove-scope"
          >
            <option value="use_case">{tr("removeUseCase.scopeGroup")}</option>
            <option value="workload">{tr("removeUseCase.scopeWorkload")}</option
            >
          </select>
        </label>
        {#if scope === "workload" && group}
          <label class="block space-y-1.5 text-sm font-medium text-foreground">
            <span class="block">{tr("removeUseCase.workload")}</span>
            <select
              class="w-full rounded-lg border border-border bg-background px-3 py-2"
              value={workloadRef}
              onchange={(event) => {
                workloadRef = event.currentTarget.value;
                confirmed = false;
              }}
              data-testid="remove-workload"
            >
              {#each group.workloads as member (member.workload_ref)}<option
                  value={member.workload_ref}>{member.workload_ref}</option
                >{/each}
            </select>
          </label>
        {/if}
      </fieldset>
      <section class="space-y-3 rounded-xl border border-border bg-card p-5">
        <h2 class="font-semibold text-foreground">
          {tr("removeUseCase.selected")}
        </h2>
        <ul class="list-disc space-y-1 pl-5 text-sm text-foreground">
          {#each members as member (member.workload_ref)}<li
              data-testid="remove-selected-member"
              data-workload-ref={member.workload_ref}
            >
              {member.workload_ref}
            </li>{/each}
        </ul>
        <p class="text-sm text-muted-foreground">
          {tr("removeUseCase.keepData")}
        </p>
      </section>
      {#if view.retained_removal_available && !eligible}
        <ErrorCallout
          tone="info"
          message={tr("removeUseCase.unavailable")}
          testId="remove-unavailable"
        />
      {/if}
      <label class="flex items-start gap-3 text-sm text-foreground">
        <input
          type="checkbox"
          bind:checked={confirmed}
          disabled={!eligible || removalState.kind === "submitting" || stale}
          data-testid="remove-confirm"
        />
        <span>{tr("removeUseCase.confirm")}</span>
      </label>
      {#if removalState.kind === "ready" && removalState.failure}
        <ErrorCallout
          message={tr(
            removalState.failure.stale
              ? "removeUseCase.stale"
              : "removeUseCase.failed",
          )}
          details={removalState.failure.details}
          onRetry={removalState.failure.stale ? load : null}
          testId="remove-error"
        />
      {/if}
      <Button
        type="submit"
        variant="destructive"
        disabled={!canSubmit}
        testId="remove-submit"
        >{tr(
          removalState.kind === "submitting"
            ? "removeUseCase.submitting"
            : "removeUseCase.submit",
        )}</Button
      >
    </form>
  {/if}
</section>
