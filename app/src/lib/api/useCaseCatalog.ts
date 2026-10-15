/**
 * StackKits use-case catalog client.
 *
 * The authority is StackKits (foundation/use_case_catalog.cue), projected onto
 * a release asset that the image pins and verifies. Techstack reads it and
 * never authors a second answer to "what is this use case built from".
 */
import { fetchApi } from "./client";

/** StackKits' own role vocabulary, passed through without reinterpretation. */
export type UseCaseComponentRole =
  "primary" | "alternative" | "supporting" | "connector" | "bridge";

export interface UseCaseCatalogComponent {
  id: string;
  name: string;
  role: UseCaseComponentRole;
  kind: string;
  /**
   * Set on alternatives: "install" when the pinned release installs it,
   * "recorded" when it only keeps the choice for a later release.
   */
  realization?: "install" | "recorded";
  /**
   * The pinned release's maturity of this application's own alternative.
   * Absent when the release has no executable alternative for it.
   */
  maturity?: UseCaseMaturity;
}

/** StackKits' maturity vocabulary for modules and their applications. */
export type UseCaseMaturity =
  "experimental" | "beta" | "supported" | "deprecated";

/** The maturity a surface labels; "supported" needs no label. */
export function maturityLabelled(
  maturity?: string,
): maturity is Exclude<UseCaseMaturity, "supported"> {
  return (
    maturity === "beta" ||
    maturity === "experimental" ||
    maturity === "deprecated"
  );
}

/** Whether choosing this service installs it with the pinned release. */
export function installsService(component: UseCaseCatalogComponent): boolean {
  return component.role === "primary" || component.realization === "install";
}

/** StackKits' compute tiers as the catalog spells them. */
export type UseCaseComputeTierId = "low" | "standard" | "high";

/**
 * StackKits' verdict on one compute tier for one use case, with its own
 * reason when the pinned release does not include it there. The reason is
 * engineering prose, shown only at the technical depth level.
 */
export interface UseCaseComputeTier {
  included: boolean;
  reason?: string;
}

/** Quantities and their support floor come from the exact StackKits release. */
export interface UseCaseResources {
  cpuCores?: number;
  ramGB?: number;
  storageGB?: number;
}

export interface UseCaseHostFloor {
  minCpuCores?: number;
  minRamGB?: number;
  minStorageGB?: number;
  minAMD64MicroarchitectureLevel?: number;
}

export interface UseCaseProfileFacts {
  maturity?: UseCaseMaturity;
  executable: boolean;
  realization?: string;
  hostFloor?: UseCaseHostFloor;
  reservation?: UseCaseResources;
  recommended?: UseCaseResources;
  provenance?: Record<string, { source: string; ref: string }>;
}

export interface UseCaseModule {
  id: string;
  computeProfiles?: string[];
  computeProfileFacts?: Record<string, UseCaseProfileFacts>;
  acceleratorProfileFacts?: Record<
    string,
    {
      vendor: string;
      access?: string;
      maturity?: UseCaseMaturity;
      minDriverMajor?: number;
      reservation?: UseCaseResources;
    }
  >;
}

export interface UseCaseAlternative {
  id: string;
  modules?: UseCaseModule[];
}

export interface UseCaseAddOn {
  id: string;
  defaultAlternative: string;
  alternatives?: UseCaseAlternative[];
}

/** One choice of a choice-kind setting. */
export interface UseCaseSettingOption {
  id: string;
  name: string;
  /** One-line consequence of choosing it. */
  note?: string;
  /** Overrides the setting's realization for this option. */
  realization?: "install" | "recorded";
  /** Server memory in GB this option needs to run well. */
  minMemoryGb?: number;
  /** The option needs a GPU in practice. */
  needsAccelerator?: boolean;
}

export type UseCaseSettingKind = "choice" | "toggle" | "text";
export type UseCaseSettingGroup =
  "backend" | "profile" | "storage" | "hardware" | "access" | "features";

/**
 * A decision an operator makes about a use case before it is installed -
 * StackKits' #UseCaseSetting plus Techstack's recorded service/profile
 * preferences derived from catalog components and included tiers.
 * `realization` says whether the pinned
 * release applies it at install or only records it for a later release; a
 * surface shows a recorded setting as such and never hides it, because the
 * creation wizard shows the target state (owner decision 2026-09-05).
 */
export interface UseCaseSetting {
  id: string;
  name: string;
  kind: UseCaseSettingKind;
  group: UseCaseSettingGroup;
  depth: "summary" | "advanced";
  help?: string;
  options?: UseCaseSettingOption[];
  default: string | boolean;
  placeholder?: string;
  realization: "install" | "recorded";
  /** The application workload an installing toggle adds. */
  workloadRef?: string;
}

/** One realization of a capability module: its default or its alternative. */
export interface UseCaseCapabilityOption {
  id: string;
  name: string;
  note?: string;
  /** "install" when the pinned release installs it, "recorded" when it only keeps the choice. */
  realization: "install" | "recorded";
  pending?: string;
  workloadRef?: string;
  alternativeRef?: string;
  alternativeRefs?: string[];
}

/** An independently selectable capability module of a use case. */
export interface UseCaseCapability {
  id: string;
  name: string;
  help?: string;
  /** A required capability cannot be turned off. */
  required: boolean;
  enabledByDefault: boolean;
  requires?: string[];
  requiresAccelerator: boolean;
  default: UseCaseCapabilityOption;
  alternative?: UseCaseCapabilityOption;
}

/**
 * A customer goal that groups capability modules (StackKits #UseCaseBundle).
 * Catalog grouping only: turning a bundle on selects its defaults through the
 * same capability selection. A module several bundles need appears in each
 * and installs once; a module in no bundle is not offered by the setup.
 */
export interface UseCaseBundle {
  id: string;
  name: string;
  help?: string;
  /** Concrete things a person does with the bundle, in customer language. */
  examples?: string[];
  /** The capability modules the bundle groups, in display order. */
  capabilities: string[];
  /** The modules turning the bundle on selects. */
  defaults: string[];
  required: boolean;
  enabledByDefault: boolean;
  /** For experienced owners only; never preselected. */
  advanced: boolean;
  /** Recommended when the node has a GPU; it still runs without one. */
  recommendsAccelerator: boolean;
  requiresSandbox: boolean;
}

export interface UseCaseCatalogEntry {
  /** The use-case slug, identical to the Wizard's goal key. No mapping table. */
  id: string;
  title: string;
  description: string;
  components: UseCaseCatalogComponent[];
  compute_tiers?: Partial<Record<UseCaseComputeTierId, UseCaseComputeTier>>;
  settings?: UseCaseSetting[];
  capabilities?: UseCaseCapability[];
  bundles?: UseCaseBundle[];
  /** Path of the guide on the docs site, when the catalog names one. */
  docs?: string;
  /**
   * Whether the pinned release can actually deliver this use case through the
   * wizard's runtime adapter. Absent when the server could not establish it.
   * Undeliverable is NOT a reason to hide or disable the card: the backend
   * stores a selected undeliverable goal and activates it in a later release.
   */
  deliverable?: boolean;
  /** The use case's maturity: that of its default application. */
  maturity?: UseCaseMaturity;
  /** The release's delivery status on the wizard's runtime adapter. */
  delivery_status?: string;
  /** The application the release installs by default. */
  default_alternative?: string;
  alternatives?: UseCaseAlternative[];
  add_ons?: UseCaseAddOn[];
  /** Opaque, non-secret MCP/API/skill facts from the same pinned SDK source. */
  agentSurface?: Record<string, unknown>;
}

export interface UseCaseCatalogResponse {
  /** False on a deployment that ships no catalog; the cards then omit the strip. */
  configured: boolean;
  /** False when deliverability could not be established; say nothing then. */
  deliverability_known: boolean;
  release?: { tag: string; version: string };
  use_cases: UseCaseCatalogEntry[];
}

/**
 * Read the catalog. Never throws for the caller's benefit: a Goals step that
 * cannot reach the catalog renders without component strips, which is a
 * degraded card, not a broken step.
 */
/** What the Goals step needs per use case, keyed by slug. */
export interface UseCaseCatalogView {
  components: UseCaseCatalogComponent[];
  /** Undefined when the server could not establish deliverability. */
  deliverable?: boolean;
  /** Per-tier inclusion with StackKits' own reasons. Empty when unknown. */
  computeTiers: Partial<Record<UseCaseComputeTierId, UseCaseComputeTier>>;
  /** Which StackKits release these facts come from, e.g. "v0.24.27". */
  releaseTag?: string;
  /** What the operator decides about this use case. Empty when none declared. */
  settings: UseCaseSetting[];
  /** Selectable capability modules of this use case. Empty when none declared. */
  capabilities?: UseCaseCapability[];
  /** Customer goals over the capability modules. Empty when none declared. */
  bundles?: UseCaseBundle[];
  /** Guide path on the docs site, when one exists. */
  docsPath?: string;
  /** The use case's maturity, labelled on the card unless supported. */
  maturity?: UseCaseMaturity;
  /** The application the release installs by default. */
  defaultAlternative?: string;
  alternatives?: UseCaseAlternative[];
  addOns?: UseCaseAddOn[];
  agentSurface?: Record<string, unknown>;
}

/** Project one catalog response onto the Goals step's views. The runtime
 * Wizard and the per-release preview share it, so both show the same facts. */
export function catalogViewsFromResponse(
  data: UseCaseCatalogResponse | undefined,
): Map<string, UseCaseCatalogView> {
  const known = data?.deliverability_known === true;
  const byGoal = new Map<string, UseCaseCatalogView>();
  for (const entry of data?.use_cases ?? []) {
    if (!entry?.id) continue;
    byGoal.set(entry.id, {
      components: entry.components ?? [],
      deliverable: known ? entry.deliverable === true : undefined,
      computeTiers: entry.compute_tiers ?? {},
      releaseTag: data?.release?.tag,
      settings: entry.settings ?? [],
      capabilities: entry.capabilities ?? [],
      bundles: entry.bundles ?? [],
      docsPath: entry.docs || undefined,
      maturity: entry.maturity,
      defaultAlternative: entry.default_alternative,
      alternatives: entry.alternatives ?? [],
      addOns: entry.add_ons ?? [],
      agentSurface: entry.agentSurface,
    });
  }
  return byGoal;
}

/** The release tag a preview may name: a StackKits release tag or "latest". */
const PREVIEW_CATALOG_TAG = /^(latest|v\d+\.\d+\.\d+)$/;

/**
 * Read a release's catalog as the public preview shows it. The StackKits
 * repin renders the same response the runtime API serves into
 * static/preview-catalogs/<tag>.json, so the preview shows exactly what a
 * Wizard on that release offers.
 */
export async function loadPreviewUseCaseCatalog(tag = "latest"): Promise<{
  views: Map<string, UseCaseCatalogView>;
  release?: { tag: string; version: string };
}> {
  if (!PREVIEW_CATALOG_TAG.test(tag)) return { views: new Map() };
  try {
    const response = await fetch(`/preview-catalogs/${tag}.json`);
    if (!response.ok) return { views: new Map() };
    const data = (await response.json()) as UseCaseCatalogResponse;
    return { views: catalogViewsFromResponse(data), release: data.release };
  } catch {
    return { views: new Map() };
  }
}

export async function loadUseCaseCatalog(): Promise<
  Map<string, UseCaseCatalogView>
> {
  try {
    const res = await fetchApi<UseCaseCatalogResponse>(
      "/api/v1/stackkits/use-cases",
    );
    return catalogViewsFromResponse(res.data);
  } catch {
    return new Map();
  }
}
