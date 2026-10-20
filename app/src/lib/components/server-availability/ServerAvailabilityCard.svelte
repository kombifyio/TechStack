<script lang="ts">
  import { onDestroy, untrack } from "svelte";
  import { Clock } from "@lucide/svelte";
  import { tr, formatDateTime } from "#lib/i18n.svelte.js";
  import Button from "#lib/components/ui/Button.svelte";
  import { parseApiError } from "#lib/api/errors.js";
  import {
    confirmServerAvailability,
    getServerAvailability,
    type NodeAvailabilityChoice,
    type NodeAvailabilityClass,
    type ServerAvailability,
  } from "#lib/api/server-availability.js";

  interface Props {
    serverId: string;
  }

  let { serverId }: Props = $props();

  let availability = $state<ServerAvailability | null>(null);
  let loadFailed = $state(false);
  let busy = $state<NodeAvailabilityChoice | null>(null);
  let notice = $state<string | null>(null);
  let error = $state<string | null>(null);
  let generation = 0;

  const hasHistory = $derived(
    availability !== null && availability.uptime_ratio !== null,
  );
  const percent = $derived(
    availability?.uptime_ratio == null
      ? ""
      : (Math.round(availability.uptime_ratio * 1000) / 10).toLocaleString(
          undefined,
          { maximumFractionDigits: 1 },
        ),
  );

  function classLabel(value: NodeAvailabilityClass): string {
    return tr(`nodeAvailability.class.${value}`);
  }

  async function load() {
    const mine = ++generation;
    try {
      const next = await getServerAvailability(serverId);
      if (mine !== generation) return;
      availability = next;
      loadFailed = false;
    } catch {
      if (mine !== generation) return;
      loadFailed = availability === null;
    }
  }

  async function choose(choice: NodeAvailabilityChoice) {
    busy = choice;
    error = null;
    notice = null;
    try {
      const next = await confirmServerAvailability(serverId, choice);
      generation++;
      availability = next;
      loadFailed = false;
      notice = tr("nodeAvailability.saved");
    } catch (cause) {
      error = tr("nodeAvailability.saveFailed", {
        reason: parseApiError(cause).message,
      });
    } finally {
      busy = null;
    }
  }

  $effect(() => {
    void serverId;
    untrack(() => void load());
  });

  onDestroy(() => {
    generation++;
  });
</script>

<section data-kx="plate" class="p-5" data-testid="server-availability-card">
  <div class="mb-1 flex items-center gap-2">
    <Clock class="h-5 w-5 text-primary" />
    <h2 class="text-lg font-semibold text-foreground">
      {tr("nodeAvailability.title")}
    </h2>
  </div>
  <p class="mb-4 text-sm text-muted-foreground">
    {tr("nodeAvailability.intro")}
  </p>

  {#if loadFailed}
    <p class="text-sm text-muted-foreground">
      {tr("nodeAvailability.loadFailed")}
    </p>
  {:else if availability}
    <dl class="grid gap-x-6 gap-y-3 text-sm sm:grid-cols-2">
      <div>
        <dt class="text-muted-foreground">
          {tr("nodeAvailability.observed", { days: availability.window_days })}
        </dt>
        <dd
          class="font-medium text-foreground"
          data-testid="server-availability-observed"
          data-class={availability.observed_class}
        >
          {classLabel(availability.observed_class)}
        </dd>
        <dd class="text-xs text-muted-foreground">
          {#if hasHistory}
            {tr("nodeAvailability.uptime", { percent })}
          {:else}
            {tr("nodeAvailability.noEvidence")}
          {/if}
        </dd>
      </div>
      <div>
        <dt class="text-muted-foreground">
          {tr("nodeAvailability.planning")}
        </dt>
        <dd
          class="font-medium text-foreground"
          data-testid="server-availability-effective"
          data-class={availability.class}
          data-source={availability.source}
        >
          {classLabel(availability.class)}
        </dd>
        <dd class="text-xs text-muted-foreground">
          {#if availability.confirmed && availability.confirmed_at}
            {tr("nodeAvailability.confirmedBy", {
              when: formatDateTime(availability.confirmed_at),
            })}
          {:else if availability.confirmed}
            {tr("nodeAvailability.confirmed")}
          {:else}
            {tr("nodeAvailability.fromHistory")}
          {/if}
        </dd>
      </div>
    </dl>

    <p class="mt-5 text-sm font-medium text-foreground">
      {tr("nodeAvailability.ask")}
    </p>
    <div class="mt-2 flex flex-wrap items-center gap-2">
      <Button
        variant={availability.confirmed && availability.class === "always-on"
          ? "primary"
          : "secondary"}
        size="sm"
        disabled={busy !== null}
        onclick={() => void choose("always-on")}
        testId="server-availability-always-on"
      >
        {busy === "always-on"
          ? tr("nodeAvailability.saving")
          : tr("nodeAvailability.alwaysOn")}
      </Button>
      <Button
        variant={availability.confirmed && availability.class === "part-time"
          ? "primary"
          : "secondary"}
        size="sm"
        disabled={busy !== null}
        onclick={() => void choose("part-time")}
        testId="server-availability-part-time"
      >
        {busy === "part-time"
          ? tr("nodeAvailability.saving")
          : tr("nodeAvailability.partTime")}
      </Button>
      {#if availability.confirmed}
        <Button
          variant="ghost"
          size="sm"
          disabled={busy !== null}
          onclick={() => void choose("unknown")}
          testId="server-availability-use-observed"
        >
          {busy === "unknown"
            ? tr("nodeAvailability.saving")
            : tr("nodeAvailability.useObserved")}
        </Button>
      {/if}
    </div>
  {/if}

  {#if notice}
    <p class="mt-3 text-sm text-foreground" role="status">{notice}</p>
  {/if}
  {#if error}
    <p class="mt-3 text-sm text-destructive" role="alert">{error}</p>
  {/if}
</section>
