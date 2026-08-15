/**
 * kombify-TechStack API - Instance identity
 *
 * Exposes the stable local instance identity served at GET /api/v1/instance.
 * The identity is the anchor for enrollment, cloud-linking, and tenant scoping
 * under Pillar 1 (Unifier). Architecture detail: docs/ARCHITECTURE.md and
 * docs/pillars/01-unifier.md.
 */

import { fetchApi } from "./client";

export interface InstanceIdentity {
  id: string;
  created_at: string;
  hostname?: string;
  version: string;
}

/**
 * Fetch the stable local instance identity.
 *
 * Returns null on failure rather than throwing so UI shells can render a
 * placeholder when running against an older backend without the endpoint.
 */
export async function getInstance(): Promise<InstanceIdentity | null> {
  try {
    const res = await fetchApi<InstanceIdentity>("/api/v1/instance");
    return res.data;
  } catch {
    return null;
  }
}

/** Short, human-friendly form of an instance id (first 8 hex chars). */
export function shortInstanceId(id: string | undefined | null): string {
  if (!id) return "";
  return id.replace(/-/g, "").slice(0, 8);
}
