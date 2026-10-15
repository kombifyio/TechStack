<script lang="ts">
  import { tr } from "#lib/i18n.svelte.js";
  import { Laptop, Smartphone, Monitor } from "@lucide/svelte";
  import { SegmentedControl } from "@kombiverselabs/ui/primitives";
  import {
    ActorCard,
    DEFAULT_ENTITY_LABELS,
    DeviceCard,
    EntityDetailSheet,
    deviceFormFactorFromPlatform,
    type EntityFact,
    type EntityIcon,
    type EntityLabels,
    type EntityPresence,
  } from "@kombiverselabs/ui/entity";

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

  function presence(online: boolean | null): EntityPresence {
    if (online === null) return "unknown";
    return online ? "online" : "offline";
  }

  const audienceLabel = (entry: PeopleEntry) =>
    entry.audience === "owner"
      ? tr("ui.stacksCreatingCreationCompletion.owner")
      : tr("ui.peoplePanel.household");

  const D = DEFAULT_ENTITY_LABELS;
  const labels = $derived<Partial<EntityLabels>>({
    kind: {
      ...D.kind,
      person: tr("ui.peoplePanel.person"),
      device: tr("ui.peoplePanel.device"),
    },
    presence: {
      ...D.presence,
      online: tr("actors.presence.online"),
      offline: tr("actors.presence.offline"),
      unknown: tr("actors.presence.unknown"),
    },
    personRole: {
      ...D.personRole,
      owner: tr("actors.role.owner"),
      member: tr("actors.role.member"),
    },
    facts: {
      ...D.facts,
      lastSeen: tr("ui.stacksIdServersServerId.lastSeen"),
      clients: "kombify clients",
    },
    detail: { ...D.detail, facts: tr("actors.detail.facts") },
  });

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
  <li data-testid="people-row" data-audience={entry.audience}>
    {#if entry.kind === "person"}
      <ActorCard
        kind="person"
        size="row"
        name={entry.name}
        role={entry.audience === "owner" ? "owner" : "member"}
        handle={entry.detail || undefined}
        statusLabel={entry.status}
        selected={selected?.id === entry.id}
        onselect={() => (selected = entry)}
        {labels}
      />
    {:else}
      <DeviceCard
        size="row"
        name={entry.name}
        formFactor={deviceFormFactorFromPlatform(entry.platform)}
        model={platformLabel(entry.platform) || undefined}
        lastSeen={entry.lastSeen ? formatRelative(entry.lastSeen) : undefined}
        clients={entry.clients}
        owner={audienceLabel(entry)}
        presence={presence(entry.online)}
        selected={selected?.id === entry.id}
        onselect={() => (selected = entry)}
        {labels}
      />
    {/if}
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
      <a
        href="/actors"
        class="text-sm font-medium text-muted-foreground hover:text-foreground"
        data-testid="dashboard-people-actors"
      >
        {tr("actors.seeAll")}
      </a>
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
        <ul class="flex flex-col gap-2 p-2">
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
        <ul class="flex flex-col gap-2 p-2">
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

{#if selected}
  <EntityDetailSheet
    open
    onclose={() => (selected = null)}
    kind={selected.kind}
    name={selected.name}
    meta={selected.kind === "device"
      ? platformLabel(selected.platform) || undefined
      : selected.detail || undefined}
    icon={selected.kind === "device"
      ? (deviceIcon(selected) as unknown as EntityIcon)
      : undefined}
    shape={selected.kind === "person" ? "circle" : undefined}
    presence={selected.kind === "device" ? presence(selected.online) : undefined}
    statusLabel={selected.kind === "person" ? selected.status : undefined}
    tags={[audienceLabel(selected)]}
    facts={facts.map<EntityFact>((fact) => ({ label: fact.k, value: fact.v }))}
    {labels}
  />
{/if}
