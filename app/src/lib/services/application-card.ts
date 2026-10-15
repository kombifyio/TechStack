/**
 * Application-level reading of the canonical service model.
 *
 * An application groups components (containers, units). The card and the
 * detail page act on the components that grant an action, so every
 * application-level affordance resolves to concrete component targets here -
 * a button is offered only when something can actually execute it.
 */
import type { ServicePlacement } from "@kombiverselabs/ui/service";
import type {
  RegistryKitDeployment,
  RegistryServer,
  RegistryService,
} from "../api/registry";
import {
  parsePlacementClass,
  type CanonicalService,
  type ServiceApplication,
  type ServiceVersionInfo,
} from "../api/services";
import { serviceCardPlacement, serviceIsLocked } from "../service-card-adapter";

/** Actions that run on every component that grants them, behind one approval. */
export const FANOUT_ACTIONS = ["start", "stop", "restart"] as const;
export type FanoutAction = (typeof FANOUT_ACTIONS)[number];

export function componentsGranting(
  application: Pick<ServiceApplication, "components">,
  action: string,
): CanonicalService[] {
  return application.components.filter((component) =>
    component.allowed_actions.includes(action),
  );
}

/**
 * The component the application's version and update belong to: the one whose
 * measured version matches the application's, else the first measured one,
 * else the first component.
 */
export function primaryComponent(
  application: Pick<ServiceApplication, "components" | "version">,
): CanonicalService | undefined {
  const { components } = application;
  const current = application.version?.current;
  return (
    (current
      ? components.find((component) => component.version?.current === current)
      : undefined) ??
    components.find((component) => component.version?.current) ??
    components.find((component) => component.catalog) ??
    components[0]
  );
}

export function applicationVersion(
  application: Pick<ServiceApplication, "components" | "version">,
): ServiceVersionInfo | undefined {
  return (
    application.version ?? primaryComponent(application)?.version ?? undefined
  );
}

/** The component an Update runs on: the one granting it, primary first. */
export function updateTarget(
  application: Pick<ServiceApplication, "components" | "version">,
): CanonicalService | undefined {
  const granting = componentsGranting(application, "update");
  const primary = primaryComponent(application);
  return granting.find((c) => c.id === primary?.id) ?? granting[0];
}

/** The observed component "Stack it" adopts. */
export function adoptTarget(
  application: Pick<ServiceApplication, "components">,
): CanonicalService | undefined {
  return componentsGranting(application, "adopt").find(
    (component) => component.management_state === "observed",
  );
}

/** The component whose logs the card and the sheet open first. */
export function logsTarget(
  application: Pick<ServiceApplication, "components" | "version">,
): CanonicalService | undefined {
  const granting = componentsGranting(application, "logs");
  const primary = primaryComponent(application);
  return granting.find((c) => c.id === primary?.id) ?? granting[0];
}

export type ApplicationOwnership = "managed" | "observed" | "mixed" | "unknown";

export function applicationOwnership(
  application: Pick<ServiceApplication, "components">,
): ApplicationOwnership {
  const states = new Set(
    application.components.map((component) => component.management_state),
  );
  if (states.size === 0) return "unknown";
  if (states.size > 1) return "mixed";
  return states.has("managed") ? "managed" : "observed";
}

/** Every component carries the owner lock; a partial lock does not lock the app. */
export function applicationLocked(
  application: Pick<ServiceApplication, "components">,
): boolean {
  return (
    application.components.length > 0 &&
    application.components.every((component) =>
      serviceIsLocked(component.mutation_lock),
    )
  );
}

/** The measured lifecycle word of a component (health first, then observed). */
export function componentState(component: CanonicalService): string {
  return component.health.state || component.observed_state || "";
}

/** Registry facts the placement heuristics read when the backend has no answer. */
export interface PlacementContext {
  servers: Array<Pick<RegistryServer, "id" | "name">>;
  kitDeployments: RegistryKitDeployment[];
}

/**
 * Where a runtime row runs. The backend's `placement_class` answers first; the
 * deployment-evidence heuristics only run when it is absent or unknown, so an
 * older backend keeps today's behaviour.
 */
export function runtimePlacement(
  runtime: CanonicalService,
  registryService: RegistryService | undefined,
  context: PlacementContext,
): ServicePlacement {
  const measured = parsePlacementClass(runtime.placement_class);
  if (measured && measured !== "unknown") return measured;
  if (runtime.target_kind === "managed_workload") return "managed";
  const server = context.servers.find(
    (candidate) => candidate.id === runtime.server_id,
  );
  const deployment = context.kitDeployments.find(
    (candidate) => candidate.id === runtime.kit_deployment_id,
  );
  const resolved = serviceCardPlacement(
    {
      ...registryService,
      server_id: runtime.server_id,
      server_name: server?.name || registryService?.server_name,
      provider:
        deployment?.provider_id ||
        deployment?.lease_provider ||
        deployment?.runtime_lane,
    },
    [
      deployment?.server_provisioning_mode,
      deployment?.runtime_offering_id,
      deployment?.provider_id,
      deployment?.lease_provider,
    ]
      .filter(Boolean)
      .join(" "),
  );
  const evidence = [
    deployment?.server_mode,
    deployment?.server_provisioning_mode,
    deployment?.runtime_offering_id,
    deployment?.provider_id,
    deployment?.lease_provider,
  ]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  if (resolved === "cloud" || /cloud|hostinger|vps|provider/.test(evidence)) {
    return "cloud";
  }
  if (resolved === "local" && /local|device|homelab/.test(evidence)) {
    return "local";
  }
  return "unknown";
}

/** The backend's application placement class, then the first known component. */
export function applicationPlacement(
  application: Pick<ServiceApplication, "components" | "placement_class">,
  context: PlacementContext,
  registryServiceFor?: (
    runtime: CanonicalService,
  ) => RegistryService | undefined,
): ServicePlacement {
  const measured = parsePlacementClass(application.placement_class);
  if (measured && measured !== "unknown") return measured;
  for (const component of application.components) {
    const placement = runtimePlacement(
      component,
      registryServiceFor?.(component),
      context,
    );
    if (placement !== "unknown") return placement;
  }
  return "unknown";
}
