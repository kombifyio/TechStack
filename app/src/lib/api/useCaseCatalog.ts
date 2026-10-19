/**
 * StackKits use-case catalog client.
 *
 * The authority is StackKits (foundation/use_case_catalog.cue), projected onto
 * a release asset that the image pins and verifies. Techstack reads it and
 * never authors a second answer to "what is this use case built from".
 */
import {
  catalogViewsFromResponse,
  type UseCaseCatalogResponse,
  type UseCaseCatalogView,
} from "@kombiverselabs/ui/usecase";
import { fetchApi } from "./client";

// The catalog vocabulary lives in the shared UI package, so Cloud and
// Techstack read the same release catalog the same way.
export {
  catalogViewsFromResponse,
  installsService,
  maturityLabelled,
} from "@kombiverselabs/ui/usecase";
export type {
  UseCaseComponentRole,
  UseCaseCatalogComponent,
  UseCaseMaturity,
  UseCaseComputeTierId,
  UseCaseComputeTier,
  UseCaseResources,
  UseCaseHostFloor,
  UseCaseProfileFacts,
  UseCaseModule,
  UseCaseAlternative,
  UseCaseAddOn,
  UseCaseSettingOption,
  UseCaseSettingKind,
  UseCaseSettingGroup,
  UseCaseSetting,
  UseCaseCapabilityOption,
  UseCaseCapability,
  UseCaseBundle,
  UseCaseCatalogEntry,
  UseCaseCatalogResponse,
  UseCaseCatalogView,
} from "@kombiverselabs/ui/usecase";

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

/**
 * The pinned release's catalog as the runtime serves it.
 *
 * A catalog that could not be read, or that lists no use case at all, is a
 * failure and rejects. It must never come back as an empty Map: every use case
 * card would then read "Coming soon" because of an outage, and a homelab
 * without services could be created silently. Callers model the loading,
 * error and ready states and offer a reload. The one empty answer that is
 * not a failure is a deployment that says it ships no catalog
 * (`configured: false`).
 */
export async function loadUseCaseCatalog(): Promise<
  Map<string, UseCaseCatalogView>
> {
  const res = await fetchApi<UseCaseCatalogResponse>(
    "/api/v1/stackkits/use-cases",
  );
  if (res.data?.configured === false) return new Map();
  const views = catalogViewsFromResponse(res.data);
  if (views.size === 0) {
    throw new Error("The StackKits release catalog lists no use cases.");
  }
  return views;
}
