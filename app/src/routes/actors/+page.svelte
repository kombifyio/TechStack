<script lang="ts">
  /**
   * People & agents (route `/actors`): everyone who acts in the homelab,
   * people and agents, and the Offices and Factories the agents work in
   * (entity-cards concept §5: Offices and Factories belong to Actors). It is a working list, not the
   * card-size showcase of UI Lab: one row per actor with what it does, and
   * every Factory with the Offices inside it. Three sources, each with its own
   * honest state; one outage never empties the others. A card opens the one
   * detail sheet of the card family, deep-linked as `?actor=` or
   * `?workplace=` so a WorkplaceCard elsewhere can link straight here.
   */
  import { onMount } from "svelte";
  import { goto } from "$app/navigation";
  import { page } from "$app/state";
  import { PageHeader } from "@kombiverselabs/ui/shell";
  import {
    ActorCard,
    EntityCollection,
    EntityDetailSheet,
    WorkplaceCard,
    DEFAULT_ENTITY_LABELS,
    type EntityCardSize,
    type EntityFact,
    type EntityLabels,
  } from "@kombiverselabs/ui/entity";
  import { Sparkles } from "@lucide/svelte";
  import { tr } from "#lib/i18n.svelte.js";
  import ErrorCallout from "#lib/components/ui/ErrorCallout.svelte";
  import LoadingNote from "#lib/components/ui/LoadingNote.svelte";
  import { authStore } from "#lib/stores/auth.svelte.js";
  import { getHomelab } from "#lib/api/homelab.js";
  import { listAgents, listOffices } from "#lib/api/actors.js";
  import { loadPeople, type SectionState } from "#lib/dashboard/people.js";
  import {
    agentActors,
    agentsAt,
    factoryGroups,
    personActors,
    workplaceHref,
    workplaces,
    type Actor,
    type AgentActor,
    type Workplace,
  } from "#lib/actors/model.js";

  type Section<T> = SectionState<T>;
  /** Catalogue groups in reading order; anything else lands in "other". */
  const AGENT_GROUPS = ["ai", "server", "support"] as const;

  let people = $state<Section<Actor>>({ state: "loading" });
  let agents = $state<Section<AgentActor>>({ state: "loading" });
  let places = $state<Section<Workplace>>({ state: "loading" });
  const view: EntityCardSize = "row";

  const D = DEFAULT_ENTITY_LABELS;
  const labels = $derived<Partial<EntityLabels>>({
    kind: {
      ...D.kind,
      person: tr("actors.kind.person"),
      agent: tr("actors.kind.agent"),
      office: tr("actors.kind.office"),
      factory: tr("actors.kind.factory"),
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
    agentType: {
      ...D.agentType,
      permanent: tr("actors.agentType.permanent"),
      expert: tr("actors.agentType.expert"),
    },
    facts: {
      ...D.facts,
      office: tr("actors.kind.office"),
      factory: tr("actors.fact.factory"),
      model: tr("actors.fact.model"),
      owner: tr("actors.role.owner"),
      agents: tr("actors.fact.agents"),
      members: tr("actors.people"),
      resource: tr("actors.fact.resource"),
    },
    detail: { ...D.detail, facts: tr("actors.detail.facts") },
  });

  const agentGroups = $derived.by(() => {
    if (agents.state !== "ready") return [];
    const list = agents.items;
    const known = new Set<string>(AGENT_GROUPS);
    return [...AGENT_GROUPS, "other"]
      .map((group) => ({
        group,
        items: list.filter((agent) =>
          group === "other"
            ? !agent.group || !known.has(agent.group)
            : agent.group === group,
        ),
      }))
      .filter((entry) => entry.items.length > 0);
  });
  const placeGroups = $derived(
    places.state === "ready" ? factoryGroups(places.items) : [],
  );

  function tierLabel(tier: string | undefined): string | undefined {
    if (!tier) return undefined;
    const key = `actors.tier.${tier}`;
    const label = tr(key);
    return label === key ? tier : label;
  }

  function agentStatus(agent: AgentActor): string | undefined {
    return agent.status ? tr(`actors.status.${agent.status}`) : undefined;
  }

  // --- selection, deep-linked ---------------------------------------------
  const selectedActorKey = $derived(page.url.searchParams.get("actor"));
  const selectedPlaceKey = $derived(page.url.searchParams.get("workplace"));
  const allActors = $derived<Actor[]>([
    ...(people.state === "ready" ? people.items : []),
    ...(agents.state === "ready" ? agents.items : []),
  ]);
  const selectedActor = $derived(
    allActors.find((actor) => actor.key === selectedActorKey) ?? null,
  );
  const selectedPlace = $derived(
    places.state === "ready"
      ? (places.items.find((place) => place.key === selectedPlaceKey) ?? null)
      : null,
  );
  const placeAgents = $derived(
    selectedPlace && agents.state === "ready"
      ? agentsAt(selectedPlace, agents.items)
      : [],
  );
  const placeOffices = $derived(
    selectedPlace?.kind === "factory"
      ? (placeGroups.find((group) => group.factory.key === selectedPlace.key)
          ?.offices ?? [])
      : [],
  );

  function select(param: "actor" | "workplace", key: string | null) {
    const url = new URL(page.url.href);
    url.searchParams.delete("actor");
    url.searchParams.delete("workplace");
    if (key) url.searchParams.set(param, key);
    void goto(`${url.pathname}${url.search}`, {
      reset: false,
    });
  }

  function actorFacts(actor: Actor): EntityFact[] {
    if (actor.kind === "person") return [];
    return [
      actor.factory && { label: tr("actors.fact.factory"), value: actor.factory },
      actor.tier && {
        label: tr("actors.fact.tier"),
        value: tierLabel(actor.tier) ?? actor.tier,
      },
    ].filter((fact): fact is EntityFact => Boolean(fact));
  }

  function placeFacts(place: Workplace): EntityFact[] {
    if (place.kind === "factory")
      return [
        // The catalogue does not always say which agents a Factory has; an
        // unknown count is left out instead of shown as zero.
        ...(place.agentCount
          ? [{ label: tr("actors.fact.agents"), value: String(place.agentCount) }]
          : []),
        { label: tr("actors.fact.offices"), value: String(place.officeCount) },
      ];
    return [
      { label: tr("actors.fact.factory"), value: place.factory },
      ...(place.resource
        ? [{ label: tr("actors.fact.resource"), value: place.resource }]
        : []),
    ];
  }

  async function loadPeopleSection() {
    people = { state: "loading" };
    try {
      const homelab = await getHomelab();
      const ids = (homelab?.kit_deployments ?? []).map((item) => item.id);
      const ownerName =
        authStore.cloudUser?.name ||
        authStore.cloudUser?.email ||
        tr("ui.homelabDashboardPage.you");
      const result = await loadPeople(ids, ownerName);
      people =
        result.state === "ready"
          ? { state: "ready", items: personActors(result.items) }
          : result;
    } catch {
      people = { state: "error", message: tr("ui.people.householdListUnavailableTheHomelab") };
    }
  }

  async function loadAgentSections() {
    agents = { state: "loading" };
    places = { state: "loading" };
    const [agentRead, officeRead] = await Promise.all([
      listAgents(),
      listOffices(),
    ]);
    if (agentRead.state === "ready") {
      agents = { state: "ready", items: agentActors(agentRead.items) };
    } else {
      agents = {
        state: agentRead.state,
        message: tr(
          agentRead.state === "unavailable"
            ? "actors.agentsUnavailable"
            : "actors.agentsError",
        ),
      };
    }
    // Factories need the agent list for their names and staff; Offices alone
    // still name their Factory, so either list is enough to draw places.
    if (agentRead.state !== "ready" && officeRead.state !== "ready") {
      const state = officeRead.state === "error" ? "error" : agentRead.state;
      places = {
        state,
        message: tr(
          state === "unavailable"
            ? "actors.workplacesUnavailable"
            : "actors.workplacesError",
        ),
      };
      return;
    }
    places = {
      state: "ready",
      items: workplaces(
        agentRead.state === "ready" ? agentRead.items : [],
        officeRead.state === "ready" ? officeRead.items : [],
      ),
    };
  }

  onMount(() => {
    void loadPeopleSection();
    void loadAgentSections();
  });
</script>

{#snippet sectionState(
  state: Section<unknown>,
  empty: string,
  onRetry: () => void,
)}
  {#if state.state === "loading"}
    <LoadingNote label={tr("actors.loading")} />
  {:else if state.state === "unavailable"}
    <p class="text-sm text-muted-foreground">{state.message}</p>
  {:else if state.state === "error"}
    <ErrorCallout message={state.message} {onRetry} />
  {:else if state.items.length === 0}
    <p class="text-sm text-muted-foreground">{empty}</p>
  {/if}
{/snippet}

{#snippet actorCard(actor: Actor, size: EntityCardSize)}
  {#if actor.kind === "person"}
    <ActorCard
      kind="person"
      {size}
      name={actor.name}
      role={actor.role}
      handle={actor.handle}
      statusLabel={actor.statusLabel}
      selected={actor.key === selectedActorKey}
      onselect={() => select("actor", actor.key)}
      {labels}
    />
  {:else}
    <ActorCard
      kind="agent"
      {size}
      name={actor.name}
      agentType={actor.agentType}
      factory={actor.factory}
      presence={actor.presence}
      role={actor.description}
      statusLabel={agentStatus(actor)}
      selected={actor.key === selectedActorKey}
      onselect={() => select("actor", actor.key)}
      {labels}
    />
  {/if}
{/snippet}

<div class="space-y-8 p-4 md:p-6" data-testid="actors-page">
  <PageHeader title={tr("nav.actors")} description={tr("actors.description")} />

  <section class="space-y-3" aria-labelledby="actors-people">
    <h2 id="actors-people" class="text-base font-semibold">
      {tr("actors.people")}
    </h2>
    {@render sectionState(people, tr("actors.peopleEmpty"), loadPeopleSection)}
    {#if people.state === "ready" && people.items.length}
      <EntityCollection
        items={people.items}
        key={(actor) => actor.key}
        label={tr("actors.people")}
        {view}
        views={[view]}
        item={actorCard}
        {labels}
      />
    {/if}
  </section>

  <section class="space-y-3" aria-labelledby="actors-agents">
    <h2 id="actors-agents" class="text-base font-semibold">
      {tr("actors.agents")}
    </h2>
    {@render sectionState(agents, tr("actors.agentsEmpty"), loadAgentSections)}
    {#each agentGroups as entry (entry.group)}
      <div class="space-y-2">
        <h3 class="text-sm font-medium text-muted-foreground">
          {tr(`actors.group.${entry.group}`)}
        </h3>
        <EntityCollection
          items={entry.items as Actor[]}
          key={(actor) => actor.key}
          label={tr(`actors.group.${entry.group}`)}
          {view}
          views={[view]}
          item={actorCard}
          {labels}
        />
      </div>
    {/each}
  </section>

  <section class="space-y-3" aria-labelledby="actors-workplaces">
    <h2 id="actors-workplaces" class="text-base font-semibold">
      {tr("actors.workplaces")}
    </h2>
    {@render sectionState(places, tr("actors.workplacesEmpty"), loadAgentSections)}
    {#if placeGroups.length}
      <div class="grid gap-4 lg:grid-cols-2 2xl:grid-cols-3">
        {#each placeGroups as group (group.factory.key)}
          <div class="space-y-2" data-testid="actors-factory">
            <WorkplaceCard
              kind="factory"
              size="row"
              name={group.factory.name}
              meta={tr("actors.officeCount", { count: group.factory.officeCount })}
              agentCount={group.factory.agentCount || undefined}
              members={group.factory.members}
              onselect={() => select("workplace", group.factory.key)}
              selected={group.factory.key === selectedPlaceKey}
              {labels}
            />
            {#if group.offices.length}
              <ul
                class="space-y-2 border-l border-border pl-4 ml-6"
                aria-label={tr("actors.officesIn", { factory: group.factory.name })}
              >
                {#each group.offices as office (office.key)}
                  <li>
                    <WorkplaceCard
                      kind="office"
                      size="row"
                      name={office.name}
                      meta={office.resource}
                      onselect={() => select("workplace", office.key)}
                      selected={office.key === selectedPlaceKey}
                      {labels}
                    />
                  </li>
                {/each}
              </ul>
            {/if}
          </div>
        {/each}
      </div>
    {/if}
  </section>
</div>

{#if selectedActor}
  <EntityDetailSheet
    open
    onclose={() => select("actor", null)}
    kind={selectedActor.kind}
    name={selectedActor.name}
    meta={selectedActor.kind === "person"
      ? selectedActor.handle
      : selectedActor.description}
    badge={selectedActor.kind === "agent" ? Sparkles : undefined}
    presence={selectedActor.kind === "agent" ? selectedActor.presence : undefined}
    statusLabel={selectedActor.kind === "person"
      ? selectedActor.statusLabel
      : agentStatus(selectedActor)}
    tags={selectedActor.kind === "person"
      ? [tr(`actors.role.${selectedActor.role}`)]
      : [
          selectedActor.agentType &&
            tr(`actors.agentType.${selectedActor.agentType}`),
          selectedActor.group &&
            tr(
              `actors.group.${(AGENT_GROUPS as readonly string[]).includes(selectedActor.group) ? selectedActor.group : "other"}`,
            ),
        ].filter((tag): tag is string => Boolean(tag))}
    facts={actorFacts(selectedActor)}
    {labels}
  />
{:else if selectedPlace}
  <EntityDetailSheet
    open
    onclose={() => select("workplace", null)}
    kind={selectedPlace.kind}
    name={selectedPlace.name}
    meta={selectedPlace.kind === "office" ? selectedPlace.factory : undefined}
    facts={placeFacts(selectedPlace)}
    {labels}
  >
    {#if selectedPlace.kind === "factory"}
      {#if placeAgents.length}
        <div class="space-y-2" data-testid="actors-workplace-agents">
          <h3 class="text-sm font-semibold">{tr("actors.agentsHere")}</h3>
          {#each placeAgents as agent (agent.key)}
            {@render actorCard(agent, "row")}
          {/each}
        </div>
      {/if}
      {#if placeOffices.length}
        <div class="space-y-2">
          <h3 class="text-sm font-semibold">{tr("actors.officesHere")}</h3>
          {#each placeOffices as office (office.key)}
            <WorkplaceCard
              kind="office"
              size="row"
              name={office.name}
              meta={office.resource}
              onselect={() => select("workplace", office.key)}
              {labels}
            />
          {/each}
        </div>
      {/if}
    {:else}
      <div class="space-y-2">
        <p class="text-sm text-muted-foreground">
          {tr("actors.officeAgentsNote")}
        </p>
        <a
          class="text-sm font-medium underline"
          href={workplaceHref({
            key: `factory:${selectedPlace.factoryId}`,
          } as Workplace)}
        >
          {tr("actors.openFactory")}
        </a>
      </div>
    {/if}
  </EntityDetailSheet>
{/if}
