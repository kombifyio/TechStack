<script lang="ts">
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { NotificationCenter } from "#lib/components/open-core/index.js";
  import { getLocale, tr } from "#lib/i18n.svelte.js";
  import { ownerChannelOptions } from "#lib/notifications/owner-channel.js";

  const selectedId = $derived(page.url.searchParams.get("item"));

  function selectNotification(id: string | null) {
    const url = new URL(page.url.href);
    if (id) url.searchParams.set("item", id);
    else url.searchParams.delete("item");
    void goto(url, { replace: true, reset: false, shallow: true });
  }
</script>

<svelte:head>
  <title>{tr("nav.notifications")} – kombify Techstack</title>
</svelte:head>

<div class="min-h-0 p-4 md:p-6">
  <NotificationCenter
    apiBase="/api/v1/notifications"
    settingsHref="/settings#notification-preferences"
    locale={getLocale()}
    {selectedId}
    onSelect={selectNotification}
    {...ownerChannelOptions()}
  />
</div>
