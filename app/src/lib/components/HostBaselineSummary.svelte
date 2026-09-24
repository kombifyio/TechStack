<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import {
    getServerPortInventory,
    type ServerPortInventory,
  } from "#lib/api/port-inventory.js";
  import { parseApiError } from "#lib/api/errors.js";

  let { serverId, inventoryHref }: { serverId: string; inventoryHref: string } =
    $props();
  let inventory = $state<ServerPortInventory | null>(null);
  let error = $state<string | null>(null);
  let refresh = $state(0);
  let clock = $state(Date.now());
  let loading = $state(false);
  const current = $derived(
    Boolean(
      inventory?.observed_at &&
      inventory?.expires_at &&
      Date.parse(inventory.observed_at) <= clock &&
      Date.parse(inventory.expires_at) > clock,
    ),
  );
  const complete = $derived(current && inventory?.listeners_complete);
  const observed = $derived(
    inventory?.allocations.filter(
      (entry) => entry.observed_state === "present",
    ) ?? [],
  );

  $effect(() => {
    const id = serverId;
    refresh;
    let cancelled = false;
    inventory = null;
    error = null;
    loading = true;
    getServerPortInventory(id)
      .then((result) => {
        if (!cancelled) inventory = result;
      })
      .catch((reason) => {
        if (!cancelled) error = parseApiError(reason).message;
      })
      .finally(() => {
        if (!cancelled) loading = false;
      });
    const timer = setInterval(() => {
      clock = Date.now();
    }, 5_000);
    return () => {
      cancelled = true;
      clearInterval(timer);
    };
  });
</script>

<section
  class="mb-6 rounded-xl border border-border bg-background p-5"
  aria-label={tr("hostBaseline.title")}
>
  <h3 class="text-lg font-semibold text-foreground">
    {tr("hostBaseline.title")}
  </h3>
  <p class="mt-2 text-sm text-muted-foreground">
    {tr("hostBaseline.preserve")}
  </p>
  <p class="mt-3 text-sm font-medium" role="status">
    {loading
      ? tr("hostBaseline.loading")
      : complete
        ? tr("hostBaseline.current")
        : tr("hostBaseline.incomplete")}
  </p>
  {#if error}<p class="mt-2 text-sm text-destructive">{error}</p>{/if}
  {#if inventory?.observed_at}
    <p class="mt-1 text-xs text-muted-foreground">
      {tr("hostBaseline.observed")}: {new Date(
        inventory.observed_at,
      ).toLocaleString()}
    </p>
  {/if}
  {#if observed.length > 0}
    <ul class="mt-3 space-y-1 text-sm">
      {#each observed.slice(0, 8) as listener (listener.id)}
        <li>
          <code
            >{listener.transport.toUpperCase()}
            {listener.bind_address}:{listener.port}</code
          >
          — {tr(
            listener.desired ? "hostBaseline.claimed" : "hostBaseline.existing",
          )}
        </li>
      {/each}
    </ul>
  {:else if complete}
    <p class="mt-2 text-sm text-muted-foreground">
      {tr("hostBaseline.noListeners")}
    </p>
  {/if}
  <p class="mt-3 text-sm text-muted-foreground">{tr("hostBaseline.recheck")}</p>
  <div class="mt-4 flex flex-wrap gap-4 text-sm">
    <button
      class="underline disabled:opacity-50"
      disabled={loading}
      onclick={() => {
        refresh += 1;
      }}>{tr("hostBaseline.refresh")}</button
    >
    <a class="underline" href={inventoryHref}>{tr("hostBaseline.review")}</a>
  </div>
</section>
