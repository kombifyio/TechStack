/**
 * The Actors page model: everyone who acts (people and agents) and the places
 * agents work (Offices and Factories), shaped for the Brand entity cards.
 * Pure functions over the typed reads, so the page only renders.
 *
 * Factories come from two sources at once: an agent's harness is its Factory,
 * and every Office names the Factory it belongs to. A Factory without a
 * harness label takes the name of its orchestrator agent (`<id>-orchestrator`)
 * and otherwise shows its id as words, never an invented name.
 */
import type { EntityMember } from "@kombiverselabs/ui/entity";
import type { ActorAgent, ActorOffice } from "#lib/api/actors.js";
import type { PersonEntry } from "#lib/dashboard/people.js";

export interface PersonActor {
  key: string;
  kind: "person";
  name: string;
  role: "owner" | "member";
  handle?: string;
  statusLabel: string;
}

export interface AgentActor {
  key: string;
  kind: "agent";
  name: string;
  description?: string;
  agentType?: "permanent" | "expert";
  factoryId?: string;
  factory?: string;
  tier?: string;
  group?: string;
  presence: "online" | "offline" | "unknown";
  status?: ActorAgent["status"];
}

export type Actor = PersonActor | AgentActor;

export interface FactoryPlace {
  key: string;
  kind: "factory";
  id: string;
  name: string;
  agentCount: number;
  officeCount: number;
  members: EntityMember[];
}

export interface OfficePlace {
  key: string;
  kind: "office";
  id: string;
  name: string;
  factoryId: string;
  factory: string;
  resource?: string;
}

export type Workplace = FactoryPlace | OfficePlace;

export function personActors(people: readonly PersonEntry[]): PersonActor[] {
  return people.map((person) => ({
    key: person.id,
    kind: "person",
    name: person.name,
    role: person.audience === "owner" ? "owner" : "member",
    handle: person.detail || undefined,
    statusLabel: person.status,
  }));
}

/** An available agent can take work now; a disabled one cannot. Missing is unknown. */
function agentPresence(status: ActorAgent["status"]): AgentActor["presence"] {
  if (status === "available") return "online";
  if (status === "unavailable" || status === "disabled") return "offline";
  return "unknown";
}

export function agentActors(agents: readonly ActorAgent[]): AgentActor[] {
  return agents
    .map((agent): AgentActor => ({
      key: `agent:${agent.id}`,
      kind: "agent",
      name: agent.name,
      description: agent.description,
      agentType:
        agent.role === "orchestrator"
          ? "permanent"
          : agent.role === "expert"
            ? "expert"
            : undefined,
      factoryId: agent.factoryId,
      factory: agent.factoryName ?? agent.factoryId,
      tier: agent.tier,
      group: agent.group,
      presence: agentPresence(agent.status),
      status: agent.status,
    }))
    .sort((left, right) => left.name.localeCompare(right.name));
}

/** Factories first, each followed by its Offices, both by name. */
export function workplaces(
  agents: readonly ActorAgent[],
  offices: readonly ActorOffice[],
): Workplace[] {
  const names = new Map<string, string>();
  for (const agent of agents) {
    if (agent.factoryId && agent.factoryName && !names.has(agent.factoryId))
      names.set(agent.factoryId, agent.factoryName);
  }
  const ids = new Set<string>([
    ...agents.flatMap((agent) => (agent.factoryId ? [agent.factoryId] : [])),
    ...offices.map((office) => office.factoryId),
  ]);
  for (const agent of agents) {
    const id = agent.id.endsWith("-orchestrator")
      ? agent.id.slice(0, -"-orchestrator".length)
      : "";
    if (id && !names.has(id)) names.set(id, agent.name);
  }
  const factoryName = (id: string) => names.get(id) ?? idAsWords(id);
  const factories: FactoryPlace[] = [...ids]
    .map((id) => {
      const staff = agents.filter((agent) => agent.factoryId === id);
      return {
        key: `factory:${id}`,
        kind: "factory" as const,
        id,
        name: factoryName(id),
        agentCount: staff.length,
        officeCount: offices.filter((office) => office.factoryId === id).length,
        members: staff.map((agent) => ({
          id: agent.id,
          name: agent.name,
          kind: "agent" as const,
        })),
      };
    })
    .sort((left, right) => left.name.localeCompare(right.name));
  const result: Workplace[] = [];
  for (const factory of factories) {
    result.push(factory);
    for (const office of offices
      .filter((entry) => entry.factoryId === factory.id)
      .sort((left, right) => left.name.localeCompare(right.name))) {
      result.push({
        key: `office:${office.id}`,
        kind: "office",
        id: office.id,
        name: office.name,
        factoryId: office.factoryId,
        factory: factory.name,
        resource:
          office.contextKey && office.contextKey !== "default"
            ? office.contextKey
            : undefined,
      });
    }
  }
  return result;
}

/** "ril-ops" reads as "Ril ops": a slug shown as words, nothing more. */
function idAsWords(id: string): string {
  const words = id
    .split(/[-_\s]+/)
    .filter(Boolean)
    .join(" ");
  return words ? words[0].toUpperCase() + words.slice(1) : id;
}

export interface FactoryGroup {
  factory: FactoryPlace;
  offices: OfficePlace[];
}

/** The flat workplace list as Factories, each with the Offices inside it. */
export function factoryGroups(places: readonly Workplace[]): FactoryGroup[] {
  const groups: FactoryGroup[] = [];
  for (const place of places) {
    if (place.kind === "factory") groups.push({ factory: place, offices: [] });
    else groups.at(-1)?.offices.push(place);
  }
  return groups;
}

/**
 * The agents of a Factory. An Office lists none: the agent catalogue does not
 * say which Office equips an agent, and guessing would misplace them.
 */
export function agentsAt(
  place: Workplace,
  agents: readonly AgentActor[],
): AgentActor[] {
  if (place.kind !== "factory") return [];
  return agents.filter((agent) => agent.factoryId === place.id);
}

/** Address of a place on the Actors page; the card links here. */
export function workplaceHref(place: Workplace): string {
  return `/actors?workplace=${encodeURIComponent(place.key)}`;
}
