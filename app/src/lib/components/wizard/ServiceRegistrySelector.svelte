<script lang="ts">
  import type {
    ServicesConfig,
    StandardBundleServiceKey,
  } from "$lib/wizard";
  import { getTechieServiceQuestions } from "$lib/wizard";

  interface Props {
    services: ServicesConfig;
    compact?: boolean;
    onchange?: (key: StandardBundleServiceKey, enabled: boolean) => void;
  }

  let { services, compact = false, onchange }: Props = $props();

  const catalog = getTechieServiceQuestions();
  const selectedCount = $derived(
    catalog.filter((service) => services[service.key]).length,
  );

  function setService(key: StandardBundleServiceKey, enabled: boolean) {
    services[key] = enabled;
    onchange?.(key, enabled);
  }
</script>

<div
  class="space-y-4"
  data-testid="service-registry-selector"
  data-selected-count={selectedCount}
>
  <div class={compact ? "grid gap-3" : "grid gap-5 md:grid-cols-2"}>
    {#each catalog as service (service.key)}
      <label
        class="rounded-lg border border-border bg-card/80 p-4 transition-colors hover:border-primary/60 {services[
          service.key
        ]
          ? 'border-primary bg-primary/5 ring-1 ring-primary/20'
          : ''}"
        data-testid={`service-registry-option-${service.key}`}
      >
        <div class="flex items-start gap-3">
          <input
            type="checkbox"
            checked={services[service.key]}
            data-testid={service.testId}
            class="mt-1 rounded border-border text-primary"
            onchange={(event) =>
              setService(
                service.key,
                (event.currentTarget as HTMLInputElement).checked,
              )}
          />
          <div class="min-w-0">
            <p class="font-medium text-foreground">{service.label}</p>
            <p class="mt-1 text-sm text-muted-foreground">
              {service.description}
            </p>
            {#if !compact}
              <p class="mt-2 text-xs text-muted-foreground">
                {service.helpText}
              </p>
            {/if}
          </div>
        </div>
      </label>
    {/each}
  </div>

  <p class="text-center text-sm text-muted-foreground">
    {selectedCount} service{selectedCount !== 1 ? "s" : ""} selected
  </p>
</div>
