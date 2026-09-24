/**
 * StackKits use-case catalog client.
 *
 * The authority is StackKits (foundation/use_case_catalog.cue), projected onto
 * a release asset that the image pins and verifies. Techstack reads it and
 * never authors a second answer to "what is this use case built from": the
 * bundle's goalServiceMap maps goals onto payload service FLAGS and is not the
 * same question (it would claim Media Library is built from monitoring).
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

/** One choice of a choice-kind setting. */
export interface UseCaseSettingOption {
  id: string;
  name: string;
  /** One-line consequence of choosing it. */
  note?: string;
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
}

export interface UseCaseCatalogEntry {
  /** The use-case slug, identical to the Wizard's goal key. No mapping table. */
  id: string;
  title: string;
  description: string;
  components: UseCaseCatalogComponent[];
  compute_tiers?: Partial<Record<UseCaseComputeTierId, UseCaseComputeTier>>;
  settings?: UseCaseSetting[];
  /** Path of the guide on the docs site, when the catalog names one. */
  docs?: string;
  /**
   * Whether the pinned release can actually deliver this use case through the
   * wizard's runtime adapter. Absent when the server could not establish it.
   * Undeliverable is NOT a reason to hide or disable the card: the backend
   * stores a selected undeliverable goal and activates it in a later release.
   */
  deliverable?: boolean;
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
  /** Guide path on the docs site, when one exists. */
  docsPath?: string;
}

export async function loadUseCaseCatalog(): Promise<
  Map<string, UseCaseCatalogView>
> {
  try {
    const res = await fetchApi<UseCaseCatalogResponse>(
      "/api/v1/stackkits/use-cases",
    );
    const known = res.data?.deliverability_known === true;
    const byGoal = new Map<string, UseCaseCatalogView>();
    for (const entry of res.data?.use_cases ?? []) {
      if (!entry?.id) continue;
      byGoal.set(entry.id, {
        components: entry.components ?? [],
        deliverable: known ? entry.deliverable === true : undefined,
        computeTiers: entry.compute_tiers ?? {},
        releaseTag: res.data?.release?.tag,
        settings: entry.settings ?? [],
        docsPath: entry.docs || undefined,
      });
    }
    return byGoal;
  } catch {
    return new Map();
  }
}
