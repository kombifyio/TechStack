<script lang="ts">
  import { tr, stateLabel } from "#lib/i18n.svelte.js";
  import { ServiceDetailSheet } from "@kombiverselabs/ui/service";

  import Button from "#lib/components/ui/Button.svelte";
  import type { CanonicalService } from "#lib/api/services.js";
  import { openServiceUrl } from "#lib/service-card-adapter.js";
  import { serviceCardStatus } from "#lib/service-card-adapter.js";
  import {
    serviceAccessUrl,
    serviceDisplayName,
    serviceState,
  } from "#lib/dashboard/homelab-model.js";

  interface Props {
    service: CanonicalService | null;
    /** Name of the Node the service runs on. */
    nodeName?: string;
    onclose: () => void;
  }

  let { service, nodeName, onclose }: Props = $props();

  const accessUrl = $derived(service ? serviceAccessUrl(service) : undefined);
  const servicesHref = $derived(
    service ? `/services?service=${encodeURIComponent(service.id)}` : "/services",
  );
</script>

<!-- The same Brand sheet the Services page opens. The dashboard shows the
     overview; governed actions, logs and history stay on the Services page,
     one click away with the same deep link. -->
{#if service}
  {@const state = serviceState(service)}
  <ServiceDetailSheet
    open={true}
    {onclose}
    name={serviceDisplayName(service)}
    meta={[service.service_key, nodeName, service.management_state]
      .filter(Boolean)
      .join(" · ")}
    status={serviceCardStatus({
      status: service.health?.state || service.observed_state,
    })}
    statusLabel={state.label || tr("ui.dashboardServiceSheet.noHealthReported")}
    tabs={["overview"]}
  >
    {#snippet overview()}
      <div class="flex flex-col gap-4" data-testid="dashboard-service-sheet">
        <div class="grid grid-cols-3 gap-2">
          {#each [["Desired", service.desired_state], ["Observed", service.observed_state], ["Health", service.health?.state]] as [label, value] (label)}
            <div class="rounded-lg bg-muted/30 px-2.5 py-2">
              <div
                class="text-[9px] tracking-wider text-muted-foreground uppercase"
              >
                {label}
              </div>
              <div class="mt-0.5 font-mono text-[12.5px] font-semibold">
                {value || stateLabel("unknown")}
              </div>
            </div>
          {/each}
        </div>
        <div
          class="flex flex-wrap gap-x-4 gap-y-1 text-xs text-muted-foreground"
        >
          <span>{tr("ui.serviceSheet.node", { node: nodeName || service.server_id || tr("ui.serviceSheet.notPlaced") })}</span>
          <span>{tr("ui.serviceSheet.ownership", { value: service.management_state })}</span>
          {#if service.stackkit_version}
            <span>{tr("ui.serviceSheet.stackkit", { version: service.stackkit_version })}</span>
          {/if}
          {#if service.source}<span>{tr("ui.common.sourceValue", { value: service.source })}</span>{/if}
        </div>
        {#if accessUrl}
          <p
            class="truncate rounded-lg bg-muted/30 px-2.5 py-2 font-mono text-xs"
          >
            {accessUrl}
          </p>
        {:else}
          <p class="text-xs text-muted-foreground">
            {tr("ui.dashboard.service.noAddress")}
          </p>
        {/if}
      </div>
    {/snippet}
    {#snippet actionBar()}
      <div class="flex flex-wrap gap-2">
        {#if accessUrl}
          <Button
            variant="primary"
            size="sm"
            testId="dashboard-service-open-app"
            onclick={() => openServiceUrl({ url: accessUrl })}
          >
            {tr("ui.dashboard.service.openApp")}
          </Button>
        {/if}
        <a
          class="inline-flex items-center rounded-md px-3 py-1.5 text-sm text-primary hover:underline"
          href={servicesHref}
          data-testid="dashboard-service-open-services"
        >
          {tr("ui.dashboard.service.actionsAndLogs")}
        </a>
      </div>
    {/snippet}
  </ServiceDetailSheet>
{/if}
