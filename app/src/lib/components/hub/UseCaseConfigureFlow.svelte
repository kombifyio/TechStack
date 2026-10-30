<script lang="ts">
  import { onMount } from "svelte";
  import { ArrowLeft } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    configureInstalledWorkload,
    getUseCaseConfiguration,
    type ConfigureInstalledWorkloadAccepted,
    type ConfigureInstalledWorkloadRequest,
    type PublicSettingValue,
    type UseCaseConfigurationView,
  } from "#lib/api/useCaseConfiguration.js";
  import {
    clearExtendWizardIdempotencyKey,
    mintExtendWizardIdempotencyKey,
  } from "#lib/wizard/idempotency-keys.js";
  import Button from "#lib/components/ui/Button.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import UseCaseRunProgress from "./UseCaseRunProgress.svelte";

  let {
    stackId,
    preferredWorkload = "",
  }: { stackId: string; preferredWorkload?: string } = $props();
  type Profile = "compute" | "storage" | "accelerator";
  const profileKeys: Profile[] = ["compute", "storage", "accelerator"];
  let view = $state<UseCaseConfigurationView | null>(null);
  let loading = $state(true);
  let loadFailed = $state(false);
  let useCaseId = $state("");
  let workloadRef = $state("");
  let profiles = $state<Partial<Record<Profile, string>>>({});
  let settings = $state<Record<string, PublicSettingValue>>({});
  let secretRefs = $state<Record<string, string>>({});
  let invalidNumberKeys = $state<string[]>([]);
  let submitting = $state(false);
  let result = $state<ConfigureInstalledWorkloadAccepted | null>(null);
  let failure = $state<string | null>(null);
  let failureDetails = $state<string | null>(null);
  let stale = $state(false);
  let controller: AbortController | undefined;
  const useCase = $derived(
    view?.use_cases.find((entry) => entry.id === useCaseId),
  );
  const workload = $derived(
    useCase?.workloads.find((entry) => entry.workload_ref === workloadRef),
  );
  const backHref = $derived(`/stacks/${encodeURIComponent(stackId)}`);
  const request = $derived.by((): ConfigureInstalledWorkloadRequest | null => {
    if (!view?.expected_spec_hash || !workload) return null;
    const changed: ConfigureInstalledWorkloadRequest = {
      expected_spec_hash: view.expected_spec_hash,
    };
    for (const key of profileKeys) {
      const value = profiles[key];
      if (
        value &&
        value !== workload.profiles[key]?.current &&
        workload.profiles[key]?.options.includes(value)
      )
        changed[`${key}_profile`] = value;
    }
    const changedSettings: Record<string, PublicSettingValue> = {};
    for (const field of workload.settings) {
      const value = settings[field.key];
      if (
        value !== undefined &&
        typeof value === field.type &&
        (typeof value !== "number" || Number.isFinite(value)) &&
        (!field.options?.length ||
          field.options.some((option) => Object.is(option, value))) &&
        !Object.is(value, field.current)
      )
        changedSettings[field.key] = value;
    }
    if (Object.keys(changedSettings).length) changed.settings = changedSettings;
    const changedRefs: Record<string, string> = {};
    for (const field of workload.secret_refs) {
      const value = secretRefs[field.key];
      if (
        value &&
        value !== field.current &&
        field.options.includes(value) &&
        value.startsWith("secret://")
      )
        changedRefs[field.key] = value;
    }
    if (Object.keys(changedRefs).length) changed.secret_refs = changedRefs;
    return changed;
  });
  const hasChanges = $derived(
    request !== null && Object.keys(request).length > 1,
  );
  const editableSettings = $derived(
    workload?.settings.filter((field) =>
      ["string", "number", "boolean"].includes(field.type),
    ) ?? [],
  );
  const hasFields = $derived(
    Boolean(
      workload &&
      (profileKeys.some((key) => workload.profiles[key]?.options.length) ||
        editableSettings.length ||
        workload.secret_refs.some((field) =>
          field.options.some((value) => value.startsWith("secret://")),
        )),
    ),
  );

  $effect(() => {
    const current = workload;
    profiles = current
      ? Object.fromEntries(
          profileKeys.flatMap((key) =>
            current.profiles[key]?.current
              ? [[key, current.profiles[key].current]]
              : [],
          ),
        )
      : {};
    settings = Object.fromEntries(
      (current?.settings ?? []).flatMap((field) =>
        field.current !== undefined ? [[field.key, field.current]] : [],
      ),
    );
    secretRefs = Object.fromEntries(
      (current?.secret_refs ?? []).flatMap((field) =>
        field.current ? [[field.key, field.current]] : [],
      ),
    );
    invalidNumberKeys = [];
    failure = null;
    failureDetails = null;
    stale = false;
  });

  onMount(() => {
    void load();
    return () => controller?.abort();
  });

  async function load() {
    controller?.abort();
    const attempt = new AbortController();
    controller = attempt;
    loading = true;
    loadFailed = false;
    try {
      const next = await getUseCaseConfiguration(stackId, attempt.signal);
      if (attempt.signal.aborted) return;
      const preferred = next.use_cases.find((entry) =>
        entry.workloads.some(
          (member) =>
            member.workload_ref === (workloadRef || preferredWorkload),
        ),
      );
      const first = preferred ?? next.use_cases[0];
      const member =
        first?.workloads.find(
          (entry) => entry.workload_ref === (workloadRef || preferredWorkload),
        ) ?? first?.workloads[0];
      view = next;
      useCaseId = first?.id ?? "";
      workloadRef = member?.workload_ref ?? "";
      result = null;
    } catch {
      if (!attempt.signal.aborted) loadFailed = true;
    } finally {
      if (!attempt.signal.aborted) loading = false;
    }
  }

  function chooseUseCase(id: string) {
    useCaseId = id;
    workloadRef =
      view?.use_cases.find((entry) => entry.id === id)?.workloads[0]
        ?.workload_ref ?? "";
  }

  function numberChanged(key: string, input: HTMLInputElement) {
    const invalid = input.value === "" || !Number.isFinite(input.valueAsNumber);
    invalidNumberKeys = invalid
      ? [...new Set([...invalidNumberKeys, key])]
      : invalidNumberKeys.filter((existing) => existing !== key);
    if (invalid) delete settings[key];
    else settings[key] = input.valueAsNumber;
  }

  const scalarLabel = (value: PublicSettingValue) =>
    typeof value === "boolean"
      ? tr(value ? "switch.on" : "switch.off")
      : String(value);

  async function apply() {
    if (
      !request ||
      !workload?.eligible ||
      !hasChanges ||
      invalidNumberKeys.length > 0 ||
      submitting ||
      stale
    )
      return;
    const submitted = request;
    const selected = workload.workload_ref;
    const scope = `configure:${stackId}:${selected}`;
    const key = mintExtendWizardIdempotencyKey(
      scope,
      JSON.stringify(submitted),
    );
    submitting = true;
    failure = null;
    failureDetails = null;
    try {
      result = await configureInstalledWorkload(
        stackId,
        selected,
        submitted,
        key,
      );
      clearExtendWizardIdempotencyKey(scope);
    } catch (cause) {
      const error = parseApiError(cause);
      const details =
        error.details && typeof error.details === "object"
          ? (error.details as { reason_code?: unknown })
          : undefined;
      stale =
        error.status === 409 &&
        details?.reason_code === "configuration_baseline_changed";
      failure = tr(stale ? "configure.stale" : "configure.failed");
      failureDetails = error.message;
    } finally {
      submitting = false;
    }
  }
</script>

<section
  class="mx-auto w-full max-w-4xl space-y-6 p-6 md:p-8"
  data-testid="configure-use-case-page"
  data-stack-id={stackId}
>
  <a
    href={backHref}
    class="inline-flex items-center gap-2 text-sm text-primary hover:underline"
    ><ArrowLeft size={16} aria-hidden="true" />{tr("configure.back")}</a
  >
  <header class="space-y-2">
    <h1 class="text-2xl font-semibold tracking-tight text-foreground">
      {tr("configure.title")}
    </h1>
    <p class="max-w-prose text-sm text-muted-foreground">
      {tr("configure.body")}
    </p>
  </header>
  {#if loading}
    <LoadingNote label={tr("common.loading")} />
  {:else if loadFailed}
    <ErrorCallout
      message={tr("configure.loadFailed")}
      onRetry={load}
      testId="configure-load-error"
    />
  {:else if !view?.use_cases.length}
    <p
      class="rounded-xl border border-dashed border-border p-5 text-sm text-muted-foreground"
      data-testid="configure-empty"
    >
      {tr("configure.empty")}
    </p>
  {:else if result}
    <section
      class="space-y-4 rounded-xl border border-border bg-card p-5"
      data-testid="configure-result"
      data-run-id={result.run_id}
    >
      <h2 class="font-semibold text-foreground">
        {useCase?.name} · {workloadRef}
      </h2>
      <p class="text-sm text-muted-foreground">{tr("configure.accepted")}</p>
      <UseCaseRunProgress {stackId} jobIds={result.job_ids} />
      <Button variant="secondary" onclick={load} testId="configure-review"
        >{tr("configure.review")}</Button
      >
    </section>
  {:else}
    <form
      onsubmit={(event) => {
        event.preventDefault();
        void apply();
      }}
      class="space-y-6"
      data-testid="configure-form"
    >
      <fieldset disabled={submitting} class="grid gap-4 sm:grid-cols-2">
        <label class="space-y-1.5 text-sm font-medium text-foreground">
          <span class="block">{tr("configure.useCase")}</span>
          <select
            class="w-full rounded-lg border border-border bg-background px-3 py-2"
            value={useCaseId}
            onchange={(event) => chooseUseCase(event.currentTarget.value)}
            data-testid="configure-use-case"
          >
            {#each view.use_cases as entry (entry.id)}<option value={entry.id}
                >{entry.name}</option
              >{/each}
          </select>
        </label>
        {#if useCase}
          <label class="space-y-1.5 text-sm font-medium text-foreground">
            <span class="block">{tr("configure.workload")}</span>
            <select
              class="w-full rounded-lg border border-border bg-background px-3 py-2"
              bind:value={workloadRef}
              data-testid="configure-workload"
            >
              {#each useCase.workloads as member (member.workload_ref)}<option
                  value={member.workload_ref}
                  >{member.workload_ref} · {member.alternative}</option
                >{/each}
            </select>
          </label>
        {/if}
      </fieldset>
      {#if workload}
        {#if !workload.eligible}
          <ErrorCallout
            tone="info"
            message={tr("configure.unavailable")}
            details={workload.reason_code ?? null}
            testId="configure-unavailable"
          />
        {/if}
        {#if !hasFields}
          <p
            class="text-sm text-muted-foreground"
            data-testid="configure-no-fields"
          >
            {tr("configure.noFields")}
          </p>
        {:else}
          <fieldset
            disabled={submitting || !workload.eligible || stale}
            class="space-y-6"
          >
            {#if profileKeys.some((key) => workload.profiles[key]?.options.length)}
              <div
                class="grid gap-4 rounded-xl border border-border bg-card p-5 sm:grid-cols-3"
              >
                {#each profileKeys as key (key)}
                  {#if workload.profiles[key]?.options.length}
                    <label
                      class="space-y-1.5 text-sm font-medium text-foreground"
                    >
                      <span class="block">{tr(`configure.${key}`)}</span>
                      <select
                        class="w-full rounded-lg border border-border bg-background px-3 py-2"
                        value={profiles[key] ?? ""}
                        onchange={(event) => {
                          profiles[key] = event.currentTarget.value;
                        }}
                        data-testid={`configure-profile-${key}`}
                      >
                        <option value={workload.profiles[key].current ?? ""}
                          >{tr("configure.keep")}{workload.profiles[key].current
                            ? ` (${workload.profiles[key].current})`
                            : ""}</option
                        >
                        {#each workload.profiles[key].options.filter((value) => value !== workload.profiles[key].current) as option (option)}<option
                            value={option}>{option}</option
                          >{/each}
                      </select>
                    </label>
                  {/if}
                {/each}
              </div>
            {/if}
            {#if editableSettings.length}
              <section
                class="space-y-4 rounded-xl border border-border bg-card p-5"
                aria-labelledby="configure-settings-title"
              >
                <h2
                  id="configure-settings-title"
                  class="font-semibold text-foreground"
                >
                  {tr("configure.settings")}
                </h2>
                {#each editableSettings as field (field.key)}
                  <label
                    class="block space-y-1.5 text-sm font-medium text-foreground"
                  >
                    <span class="block">{field.label || field.key}</span>
                    {#if field.options?.length}
                      <select
                        class="w-full rounded-lg border border-border bg-background px-3 py-2"
                        value={field.options.findIndex((option) =>
                          Object.is(option, settings[field.key]),
                        )}
                        onchange={(event) => {
                          const value =
                            field.options?.[Number(event.currentTarget.value)];
                          if (value !== undefined) settings[field.key] = value;
                          else if (field.current !== undefined)
                            settings[field.key] = field.current;
                          else delete settings[field.key];
                        }}
                        data-testid={`configure-setting-${field.key}`}
                      >
                        <option value="-1" disabled
                          >{tr("configure.keep")}</option
                        >
                        {#each field.options as option, index (index)}<option
                            value={index}>{scalarLabel(option)}</option
                          >{/each}
                      </select>
                    {:else if field.type === "boolean"}
                      <input
                        type="checkbox"
                        checked={settings[field.key] === true}
                        onchange={(event) => {
                          settings[field.key] = event.currentTarget.checked;
                        }}
                        data-testid={`configure-setting-${field.key}`}
                      />
                    {:else if field.type === "number"}
                      <input
                        type="number"
                        step="any"
                        required={invalidNumberKeys.includes(field.key)}
                        aria-invalid={invalidNumberKeys.includes(field.key)}
                        class="w-full rounded-lg border border-border bg-background px-3 py-2"
                        value={settings[field.key] ?? ""}
                        oninput={(event) =>
                          numberChanged(field.key, event.currentTarget)}
                        data-testid={`configure-setting-${field.key}`}
                      />
                      {#if invalidNumberKeys.includes(field.key)}<span
                          class="block text-sm text-destructive"
                          role="status">{tr("configure.invalidNumber")}</span
                        >{/if}
                    {:else}
                      <input
                        type="text"
                        class="w-full rounded-lg border border-border bg-background px-3 py-2"
                        value={settings[field.key] ?? ""}
                        oninput={(event) => {
                          settings[field.key] = event.currentTarget.value;
                        }}
                        data-testid={`configure-setting-${field.key}`}
                      />
                    {/if}
                  </label>
                {/each}
              </section>
            {/if}
            {#if workload.secret_refs.some( (field) => field.options.some( (value) => value.startsWith("secret://") ) )}
              <section
                class="space-y-4 rounded-xl border border-border bg-card p-5"
                aria-labelledby="configure-secrets-title"
              >
                <h2
                  id="configure-secrets-title"
                  class="font-semibold text-foreground"
                >
                  {tr("configure.secrets")}
                </h2>
                {#each workload.secret_refs as field (field.key)}
                  {#if field.options.some( (value) => value.startsWith("secret://") )}
                    <label
                      class="block space-y-1.5 text-sm font-medium text-foreground"
                      ><span class="block">{field.key}</span>
                      <select
                        class="w-full rounded-lg border border-border bg-background px-3 py-2"
                        value={secretRefs[field.key] ?? ""}
                        onchange={(event) => {
                          secretRefs[field.key] = event.currentTarget.value;
                        }}
                        data-testid={`configure-secret-${field.key}`}
                      >
                        <option value={field.current ?? ""}
                          >{tr("configure.keep")}</option
                        >
                        {#each field.options.filter((value) => value !== field.current && value.startsWith("secret://")) as option (option)}<option
                            value={option}>{option}</option
                          >{/each}
                      </select>
                    </label>
                  {/if}
                {/each}
              </section>
            {/if}
          </fieldset>
        {/if}
        <p class="text-sm text-muted-foreground">{tr("configure.retain")}</p>
        {#if failure}<ErrorCallout
            message={failure}
            details={failureDetails}
            onRetry={stale ? load : null}
            testId="configure-error"
          />{/if}
        <div class="flex flex-wrap items-center gap-3">
          <Button
            type="submit"
            variant="primary"
            disabled={!hasChanges ||
              invalidNumberKeys.length > 0 ||
              !workload.eligible ||
              submitting ||
              stale}
            testId="configure-submit"
            >{tr(
              submitting ? "configure.submitting" : "configure.submit",
            )}</Button
          >
          {#if !hasChanges}<span class="text-sm text-muted-foreground"
              >{tr("configure.noChanges")}</span
            >{/if}
        </div>
      {/if}
    </form>
  {/if}
</section>
