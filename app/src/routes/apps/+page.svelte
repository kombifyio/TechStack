<script lang="ts">
  /**
   * Applications: everything a user opens through a UI on their servers
   * (Immich, Hermes, a kombify tool), as compact cards or logos only. The
   * Services page keeps every service, system services included, with the
   * larger cards and their settings.
   */
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { LayoutGrid, List, Rows2 } from "@lucide/svelte";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import { tr } from "#lib/i18n.svelte.js";
  import {
    listCanonicalServices,
    type CanonicalService,
  } from "#lib/api/services.js";
  import ApplicationCard from "#lib/components/dashboard/ApplicationCard.svelte";
  import ApplicationDetailSheet from "#lib/components/dashboard/ApplicationDetailSheet.svelte";
  import { createAppQuickActions } from "#lib/services/app-quick-actions.svelte.js";
  import {
    foldApplications,
    serviceDisplayName,
  } from "#lib/dashboard/homelab-model.js";

  type View = "logos" | "list" | "cards";
  const VIEW_KEY = "techstack.apps.view";

  let services = $state<CanonicalService[]>([]);
  let loading = $state(true);
  let failed = $state(false);
  let view = $state<View>("list");

  /** One entry per application and server; the same app on two servers is two. */
  const applications = $derived.by(() => {
    const byServer: Record<string, CanonicalService[]> = {};
    for (const service of services) {
      const key = service.server_id || `workload:${service.id}`;
      (byServer[key] ??= []).push(service);
    }
    const entries: { service: CanonicalService; members: CanonicalService[] }[] =
      [];
    for (const placed of Object.values(byServer)) {
      const { apps, members } = foldApplications(placed);
      for (const app of apps)
        entries.push({ service: app, members: members[app.id] ?? [app] });
    }
    return entries.sort((left, right) =>
      serviceDisplayName(left.service).localeCompare(
        serviceDisplayName(right.service),
      ),
    );
  });

  const { provider: quick, start: startQuickActions } = createAppQuickActions({
    refresh: async () => {
      try {
        services = await listCanonicalServices();
      } catch {
        // Keep the last list; the next action or reload tries again.
      }
    },
    services: () => services,
  });
  onMount(startQuickActions);

  function setView(next: View) {
    view = next;
    try {
      localStorage.setItem(VIEW_KEY, next);
    } catch {
      // Storage is a convenience; the view still switches.
    }
  }

  /** M and L cards open the detail sheet; S goes straight to the app. */
  let sheetServiceId = $state<string | null>(null);
  const sheetEntry = $derived(
    applications.find((entry) => entry.service.id === sheetServiceId),
  );

  function openDetails(serviceId: string) {
    void goto(`/services?service=${encodeURIComponent(serviceId)}`);
  }

  onMount(async () => {
    try {
      const stored = localStorage.getItem(VIEW_KEY);
      if (stored === "logos" || stored === "cards") view = stored;
    } catch {
      // No stored preference.
    }
    try {
      services = await listCanonicalServices();
    } catch {
      failed = true;
    } finally {
      loading = false;
    }
  });
</script>

<div class="p-4 md:p-6" data-testid="apps-page">
  <PageHeader
    title={tr("nav.apps")}
    description={tr("ui.apps.description")}
  >
    {#snippet actions()}
      <div
        role="tablist"
        aria-label={tr("ui.apps.view")}
        class="flex shrink-0 rounded-lg border border-border bg-card/60 p-1"
      >
        {#each [["logos", tr("ui.apps.viewLogos"), LayoutGrid], ["list", tr("ui.apps.viewList"), List], ["cards", tr("ui.apps.viewCards"), Rows2]] as const as [id, label, Icon] (id)}
          <button
            type="button"
            role="tab"
            aria-selected={view === id}
            data-kx={view === id ? "control selection" : "control"}
            class="inline-flex items-center gap-1.5 rounded-md px-3 py-1.5 text-sm"
            data-testid="apps-view-{id}"
            onclick={() => setView(id)}
          >
            <Icon class="h-4 w-4" aria-hidden="true" />
            {label}
          </button>
        {/each}
      </div>
    {/snippet}
  </PageHeader>

  {#if quick.error}
    <p class="mt-3 text-sm text-destructive" role="alert" data-testid="apps-action-error">
      {quick.error}
    </p>
  {/if}

  {#if loading}
    <p role="status" class="mt-4 text-sm text-muted-foreground">
      {tr("ui.apps.loading")}
    </p>
  {:else if failed}
    <p role="alert" class="mt-4 text-sm text-destructive">
      {tr("ui.apps.unavailable")}
    </p>
  {:else if applications.length === 0}
    <p class="mt-4 text-sm text-muted-foreground" data-testid="apps-empty">
      {tr("ui.apps.empty")}
    </p>
  {:else if view === "logos"}
    <div
      class="mt-4 grid grid-cols-[repeat(auto-fill,minmax(4.5rem,1fr))] gap-3 sm:grid-cols-[repeat(auto-fill,minmax(5.5rem,1fr))]"
      data-testid="apps-logos"
    >
      {#each applications as entry (entry.service.id)}
        <ApplicationCard
          service={entry.service}
          members={entry.members}
          size="s"
          onDetails={() => openDetails(entry.service.id)}
        />
      {/each}
    </div>
  {:else if view === "cards"}
    <div
      class="mt-4 grid grid-cols-1 gap-2 sm:grid-cols-[repeat(auto-fill,minmax(19rem,1fr))]"
      data-testid="apps-cards"
    >
      {#each applications as entry (entry.service.id)}
        <ApplicationCard
          service={entry.service}
          members={entry.members}
          size="l"
          actions={quick.actionsFor(entry.members)}
          onDetails={() => (sheetServiceId = entry.service.id)}
        />
      {/each}
    </div>
  {:else}
    <div
      class="mt-4 grid grid-cols-1 gap-1.5 sm:grid-cols-[repeat(auto-fill,minmax(15rem,1fr))]"
      data-testid="apps-list"
    >
      {#each applications as entry (entry.service.id)}
        <ApplicationCard
          service={entry.service}
          members={entry.members}
          actions={quick.actionsFor(entry.members)}
          onDetails={() => (sheetServiceId = entry.service.id)}
        />
      {/each}
    </div>
  {/if}
</div>

{#if sheetEntry}
  <ApplicationDetailSheet
    open
    onclose={() => (sheetServiceId = null)}
    service={sheetEntry.service}
    members={sheetEntry.members}
    actions={quick.actionsFor(sheetEntry.members)}
  />
{/if}
