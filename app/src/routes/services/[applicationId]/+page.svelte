<script lang="ts">
  import { onMount } from "svelte";
  import { page } from "$app/state";
  import { goto } from "$app/navigation";
  import { ArrowLeft, ExternalLink, Server } from "@lucide/svelte";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import { ServiceCard } from "@kombiverselabs/ui/service";
  import Button from "#lib/components/ui/Button.svelte";
  import {
    getServiceApplication,
    runManagedServiceAction,
    type ManagedServiceAction,
    type ServiceApplication,
  } from "#lib/api/services.js";
  import {
    openServiceUrl,
    serviceCardStatus,
  } from "#lib/service-card-adapter.js";

  const applicationId = $derived(page.params.applicationId);
  let application = $state<ServiceApplication | null>(null);
  let loading = $state(true);
  let error = $state<string | null>(null);
  let actionState = $state<string | null>(null);

  onMount(() => void load());

  async function load() {
    loading = true;
    error = null;
    try {
      application = await getServiceApplication(applicationId || "");
    } catch (cause) {
      error = cause instanceof Error ? cause.message : "Application details could not be loaded.";
    } finally {
      loading = false;
    }
  }

  async function runAction(
    serviceId: string,
    revision: number,
    action: ManagedServiceAction,
  ) {
    actionState = `${serviceId}:${action}:running`;
    try {
      const result = await runManagedServiceAction(
        serviceId,
        action,
        revision,
        crypto.randomUUID(),
      );
      actionState = `${serviceId}:${action}:${result.status}`;
    } catch (cause) {
      actionState = `${serviceId}:${action}:${cause instanceof Error ? cause.message : "failed"}`;
    }
  }
</script>

<div class="p-4 md:p-6" data-testid="service-application-detail">
  <PageHeader title={application?.display_name || "Application details"}>
    {#snippet actions()}
      <Button variant="secondary" onclick={() => goto("/services")}>
        <ArrowLeft class="h-4 w-4" /> Back to applications
      </Button>
    {/snippet}
  </PageHeader>

  {#if loading}
    <div class="h-40 animate-pulse rounded-lg border border-border bg-muted/20"></div>
  {:else if error}
    <div class="rounded-lg border border-destructive/30 bg-destructive/10 p-4 text-destructive" role="alert">
      {error}
    </div>
  {:else if application}
    <div class="grid gap-6 xl:grid-cols-[minmax(0,1fr)_22rem]">
      <div class="space-y-6">
        <ServiceCard
          name={application.display_name}
          description={`${application.components.length} component${application.components.length === 1 ? "" : "s"} on ${application.server_id}`}
          address={application.access.address}
          addressUnavailableReason={application.access.address ? undefined : (application.access.reason || "No reachable address reported")}
          placement="unknown"
          status={serviceCardStatus({ status: application.status })}
          statusLabel={application.status}
          onOpen={application.access.open_url
            ? () => openServiceUrl({ url: application?.access.open_url })
            : undefined}
        />

        <section aria-labelledby="components-title">
          <h2 id="components-title" class="mb-3 text-lg font-semibold">Components</h2>
          <div class="space-y-3">
            {#each application.components as component (component.id)}
              <article class="rounded-lg border border-border bg-card/60 p-4">
                <div class="flex flex-wrap items-start justify-between gap-3">
                  <div>
                    <h3 class="font-semibold">{component.name}</h3>
                    <p class="mt-1 font-mono text-xs text-muted-foreground">
                      {component.service_key} · {component.id}
                    </p>
                  </div>
                  <span class="rounded-full border border-border px-2 py-1 text-xs">
                    {component.health.state || component.observed_state}
                  </span>
                </div>
                <dl class="mt-4 grid gap-3 text-sm sm:grid-cols-2 lg:grid-cols-3">
                  <div><dt class="text-muted-foreground">Role</dt><dd>{component.role || "unclassified"}</dd></div>
                  <div><dt class="text-muted-foreground">Lifecycle</dt><dd>{component.lifecycle || "unknown"}</dd></div>
                  <div><dt class="text-muted-foreground">Impact</dt><dd>{component.operational_impact || "unknown"}</dd></div>
                  <div><dt class="text-muted-foreground">Observed</dt><dd>{component.health.observed_at || "not available"}</dd></div>
                  <div><dt class="text-muted-foreground">Source</dt><dd>{component.source}</dd></div>
                  <div><dt class="text-muted-foreground">Management</dt><dd>{component.management_state}</dd></div>
                </dl>
                {#if component.management_state === "managed" && component.allowed_actions.length > 0}
                  <div class="mt-4 flex flex-wrap gap-2">
                    {#each component.allowed_actions.filter((action) => ["start", "stop", "restart"].includes(action)) as action}
                      <Button
                        variant="secondary"
                        size="sm"
                        disabled={actionState?.startsWith(`${component.id}:`) === true}
                        onclick={() => void runAction(component.id, component.inventory_revision, action as ManagedServiceAction)}
                      >{action}</Button>
                    {/each}
                  </div>
                {/if}
                {#if actionState?.startsWith(`${component.id}:`) === true}
                  <p class="mt-3 text-xs text-muted-foreground" role="status">{actionState}</p>
                {/if}
              </article>
            {/each}
          </div>
        </section>
      </div>

      <aside class="h-fit rounded-lg border border-border bg-card/60 p-4">
        <div class="flex items-center gap-2"><Server class="h-4 w-4 text-primary" /><h2 class="font-semibold">Runtime assignment</h2></div>
        <dl class="mt-4 space-y-3 text-sm">
          <div><dt class="text-muted-foreground">Server</dt><dd class="break-all font-mono text-xs">{application.server_id}</dd></div>
          <div><dt class="text-muted-foreground">Kit deployment</dt><dd class="break-all font-mono text-xs">{application.kit_deployment_id}</dd></div>
          <div><dt class="text-muted-foreground">Application key</dt><dd class="font-mono text-xs">{application.application_key}</dd></div>
          <div><dt class="text-muted-foreground">Freshness</dt><dd>{application.observed_at || "not observed"}</dd></div>
          <div><dt class="text-muted-foreground">Address class</dt><dd>{application.access.kind}</dd></div>
        </dl>
        {#if application.access.open_url}
          <Button class="mt-4 w-full" onclick={() => openServiceUrl({ url: application?.access.open_url })}>
            <ExternalLink class="h-4 w-4" /> Open service
          </Button>
        {/if}
      </aside>
    </div>
  {/if}
</div>
