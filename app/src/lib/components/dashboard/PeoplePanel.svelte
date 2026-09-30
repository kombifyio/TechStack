<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { Laptop, Smartphone, UserRound, Monitor } from "@lucide/svelte";
  import { SegmentedControl, Sheet } from "@kombiverselabs/ui/primitives";

  import {
    platformLabel,
    type Audience,
    type DeviceEntry,
    type PeopleEntry,
    type PersonEntry,
    type SectionState,
  } from "#lib/dashboard/people.js";
  import { formatRelative } from "#lib/dashboard/time.js";

  interface Props {
    people: SectionState<PersonEntry>;
    devices: SectionState<DeviceEntry>;
    /** Hide the heading when a surrounding tab already names the section. */
    showHeading?: boolean;
  }

  let { people, devices, showHeading = true }: Props = $props();

  type Filter = "all" | Audience;
  let filter = $state<Filter>("all");
  let selected = $state<PeopleEntry | null>(null);

  const readyPeople = $derived(people.state === "ready" ? people.items : []);
  const readyDevices = $derived(devices.state === "ready" ? devices.items : []);
  const everyone = $derived<PeopleEntry[]>([...readyPeople, ...readyDevices]);
  const count = (audience: Filter) =>
    audience === "all"
      ? everyone.length
      : everyone.filter((entry) => entry.audience === audience).length;
  const filterItems = $derived([
    { value: "all", label: `All · ${count("all")}` },
    { value: "owner", label: `Owner · ${count("owner")}` },
    { value: "household", label: `Household · ${count("household")}` },
  ]);
  const matches = (entry: PeopleEntry) =>
    filter === "all" || entry.audience === filter;
  const shownPeople = $derived(readyPeople.filter(matches));
  const shownDevices = $derived(readyDevices.filter(matches));

  function deviceIcon(entry: DeviceEntry) {
    if (entry.platform === "android" || entry.platform === "ios") {
      return Smartphone;
    }
    if (entry.platform === "windows" || entry.platform === "linux") {
      return Monitor;
    }
    return Laptop;
  }

  function dotClass(online: boolean | null): string {
    if (online === null) return "bg-muted-foreground/40";
    return online ? "bg-success" : "bg-warning";
  }

  const facts = $derived.by<Array<{ k: string; v: string }>>(() => {
    const entry = selected;
    if (!entry) return [];
    if (entry.kind === "person") {
      return [
        {
          k: tr("ui.servicesApplicationId.role"),
          v: entry.audience === "owner" ? "Owner" : tr("ui.peoplePanel.householdMember"),
        },
        { k: tr("ui.settings.account"), v: entry.detail },
        { k: tr("ui.stacksId.status"), v: entry.status },
        {
          k: tr("ui.stacksCreatingCreationLease.access"),
          v:
            entry.audience === "owner"
              ? tr("ui.peoplePanel.canManageTheHomelab")
              : tr("ui.peoplePanel.usesTheHomelabSApps"),
        },
      ];
    }
    return [
      { k: tr("ui.peoplePanel.platform"), v: platformLabel(entry.platform) || "not reported" },
      { k: "kombify clients", v: entry.clients.join(" · ") || "none active" },
      {
        k: tr("ui.stacksIdServersServerId.lastSeen"),
        v: entry.lastSeen ? formatRelative(entry.lastSeen) : "not reported",
      },
      ...entry.source.installations.map((installation) => ({
        k: installation.display_name,
        v: [
          installation.status,
          installation.app_version && `v${installation.app_version}`,
          installation.environments.length > 0
            ? installation.environments.map((env) => env.label).join(", ")
            : "",
        ]
          .filter(Boolean)
          .join(" · "),
      })),
    ];
  });
</script>

{#snippet sectionNote(state: SectionState<unknown>, testId: string)}
  {#if state.state === "loading"}
    <p class="px-4 py-3 text-sm text-muted-foreground" aria-busy="true">
      {tr("common.loadingShort")}
    </p>
  {:else if state.state === "unavailable" || state.state === "error"}
    <p
      class="px-4 py-3 text-sm {state.state === 'error'
        ? 'text-warning'
        : 'text-muted-foreground'}"
      role="status"
      data-testid={testId}
      data-state={state.state}
    >
      {state.message}
    </p>
  {/if}
{/snippet}

{#snippet row(entry: PeopleEntry)}
  {@const Icon = entry.kind === "device" ? deviceIcon(entry) : UserRound}
  <li>
    <button
      type="button"
      class="flex w-full items-center gap-3 border-b border-border/60 px-4 py-2.5 text-left transition-colors last:border-b-0 hover:bg-muted/30"
      data-testid="people-row"
      data-audience={entry.audience}
      onclick={() => (selected = entry)}
    >
      <span
        class="flex h-9 w-9 shrink-0 items-center justify-center rounded-[10px] {entry.audience ===
        'owner'
          ? 'bg-primary/12 text-primary'
          : 'bg-info/12 text-info'}"
      >
        <Icon class="h-[18px] w-[18px]" />
      </span>
      <span class="flex min-w-0 flex-1 flex-col">
        <span class="truncate text-sm font-semibold text-foreground"
          >{entry.name}</span
        >
        <span class="truncate text-xs text-muted-foreground">
          {entry.kind === "person" ? entry.status : entry.detail}
        </span>
      </span>
      <span class="flex flex-col items-end gap-1">
        <span
          class="rounded px-1.5 py-px text-[10px] font-semibold {entry.audience ===
          'owner'
            ? 'bg-primary/12 text-primary'
            : 'bg-info/12 text-info'}"
        >
          {entry.audience === "owner" ? tr("ui.stacksCreatingCreationCompletion.owner") : tr("ui.peoplePanel.household")}
        </span>
        <span class="h-[7px] w-[7px] rounded-full {dotClass(entry.online)}"
        ></span>
      </span>
    </button>
  </li>
{/snippet}

<section
  class="flex min-w-0 flex-col gap-2.5"
  aria-labelledby="dashboard-people-title"
  data-testid="dashboard-people"
>
  {#if showHeading}
    <div class="flex items-baseline justify-between gap-2">
      <h2
        id="dashboard-people-title"
        class="text-lg font-semibold text-foreground"
      >
        {tr("ui.dashboard.devicesPeople")}
      </h2>
    </div>
  {:else}
    <h2 id="dashboard-people-title" class="sr-only">{tr("ui.dashboard.devicesPeople")}</h2>
  {/if}
  <SegmentedControl
    items={filterItems}
    value={filter}
    label={tr("ui.dashboard.filterDevicesPeople")}
    onchange={(value) => (filter = value as Filter)}
  />

  <div data-kx="plate" class="overflow-hidden">
    <p
      class="border-b border-border/60 px-4 pt-3 pb-1 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase"
    >
      {tr("ui.dashboard.people")}
    </p>
    {@render sectionNote(people, "people-state")}
    {#if people.state === "ready"}
      {#if shownPeople.length > 0}
        <ul>
          {#each shownPeople as entry (entry.id)}
            {@render row(entry)}
          {/each}
        </ul>
      {:else}
        <p class="px-4 py-3 text-sm text-muted-foreground">
          {filter === "household"
            ? tr("ui.peoplePanel.noHouseholdMembersYetInvite")
            : tr("ui.peoplePanel.nobodyInThisFilter")}
        </p>
      {/if}
    {/if}

    <p
      class="border-t border-b border-border/60 px-4 pt-3 pb-1 text-[11px] font-semibold tracking-wider text-muted-foreground uppercase"
    >
      {tr("ui.peoplePanel.kombifyClients")}
    </p>
    {@render sectionNote(devices, "devices-state")}
    {#if devices.state === "ready"}
      {#if shownDevices.length > 0}
        <ul>
          {#each shownDevices as entry (entry.id)}
            {@render row(entry)}
          {/each}
        </ul>
      {:else}
        <p class="px-4 py-3 text-sm text-muted-foreground">
          {filter === "household"
            ? tr("ui.peoplePanel.householdDevicesAreNotTracked")
            : tr("ui.peoplePanel.noKombifyClientIsLinked")}
        </p>
      {/if}
    {/if}
  </div>
  <p class="text-xs leading-relaxed text-muted-foreground">
    {tr("ui.peoplePanel.ownerDevicesCanManageThe")}
  </p>
</section>

<Sheet
  open={selected !== null}
  title={selected?.name ?? ""}
  description={selected?.kind === "device" ? tr("ui.peoplePanel.device") : tr("ui.peoplePanel.person")}
  onclose={() => (selected = null)}
>
  <dl
    class="grid grid-cols-[8rem_minmax(0,1fr)] gap-x-3 gap-y-2.5 text-sm"
    data-testid="people-sheet"
  >
    {#each facts as fact, index (`${fact.k}:${index}`)}
      <dt class="text-muted-foreground">{fact.k}</dt>
      <dd class="min-w-0 break-words text-foreground">{fact.v}</dd>
    {/each}
  </dl>
</Sheet>
