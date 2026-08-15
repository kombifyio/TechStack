/**
 * Homelab umbrella API (ADR-0036): one active homelab per owner groups every
 * kit deployment the owner operates. `kit_deployments` reuses the exact wire
 * shape of GET /api/v1/stacks list items.
 */
import { ApiRequestError, fetchApi } from "./client";
import type { Stack } from "./stacks";

export interface HomelabSummary {
  id: string;
  name: string;
  /** True once the owner renamed it; false while the name is still generated. */
  named?: boolean;
  named_at?: string;
  intent: Record<string, unknown>;
  created: string;
  updated: string;
}

export interface HomelabView {
  /** Null when deployments exist but no umbrella row does yet (legacy lanes). */
  homelab: HomelabSummary | null;
  kit_deployments: Stack[];
}

/**
 * Resolve the caller's homelab. Returns null when nothing is provisioned yet
 * (the API answers 404 with reason_code "homelab_not_found"); every other
 * failure propagates as {@link ApiRequestError}.
 */
export async function getHomelab(): Promise<HomelabView | null> {
  try {
    const res = await fetchApi<HomelabView>("/api/v1/homelab", {
      method: "GET",
    });
    return res.data;
  } catch (error) {
    if (error instanceof ApiRequestError && error.status === 404) {
      return null;
    }
    throw error;
  }
}

/**
 * The name every homelab starts with when nothing renamed it: migration 044's
 * backfill and `defaultCreateStackName` on the wizard lane both emit exactly
 * this. Only a fallback - see {@link chosenHomelabName}.
 */
export const GENERATED_HOMELAB_NAME = "homelab";

/**
 * The owner's own name for their homelab, or null while it is still the
 * generated placeholder.
 *
 * `named` is the authority: it is a fact the server records when the rename
 * happens, so renaming to exactly "homelab" still counts as chosen. The string
 * compare is only the fallback for lanes that predate the flag - without it,
 * every un-renamed homelab on an older payload would outrank the kombify Cloud
 * Stack Identity in the dashboard title.
 */
export function chosenHomelabName(
  homelab: HomelabSummary | null | undefined,
): string | null {
  const name = homelab?.name?.trim();
  if (!name) return null;
  if (homelab?.named === true) return name;
  if (homelab?.named === false) return null;
  return name.toLowerCase() === GENERATED_HOMELAB_NAME ? null : name;
}

/**
 * Rename the caller's homelab. Every homelab starts with a generated name
 * ("homelab"); this is how the operator gives it the name they actually use.
 */
export async function renameHomelab(name: string): Promise<HomelabSummary> {
  const res = await fetchApi<{ homelab: HomelabSummary }>("/api/v1/homelab", {
    method: "PATCH",
    body: JSON.stringify({ name }),
  });
  return res.data.homelab;
}
