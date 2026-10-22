/**
 * The data protection profile that applies to the signed-in user
 * (GET /api/v1/data-profile; workspace DATA-ARCHITECTURE.md §5). A hosted
 * account's profile is the one the Gateway signed for the request; a
 * self-hosted install reports its boot profile. A profile that cannot be
 * confirmed is shown as such, never as Standard.
 */
import { writable } from "svelte/store";
import { get } from "#lib/api/client.js";
import { parseApiError } from "#lib/api/errors.js";

export interface DataProfile {
  res: "global" | "eu";
  ret: "standard" | "zero";
  loc: "hosted" | "local";
  rev: number;
  source: "edge" | "boot";
}

export type DataProfileState =
  | { status: "hidden" }
  | { status: "resolved"; profile: DataProfile }
  | { status: "unresolved" };

export const DATA_PROFILE_ENDPOINT = "/api/v1/data-profile";

export const dataProfileState = writable<DataProfileState>({
  status: "hidden",
});

function parseProfile(value: unknown): DataProfile | null {
  if (!value || typeof value !== "object") return null;
  const p = value as Record<string, unknown>;
  if (p.res !== "global" && p.res !== "eu") return null;
  if (p.ret !== "standard" && p.ret !== "zero") return null;
  if (p.loc !== "hosted" && p.loc !== "local") return null;
  if (p.source !== "edge" && p.source !== "boot") return null;
  return {
    res: p.res,
    ret: p.ret,
    loc: p.loc,
    rev: typeof p.rev === "number" ? p.rev : 0,
    source: p.source,
  };
}

/** Message keys of the active profile, strictest axes first. */
export function dataProfileLabelKeys(profile: DataProfile): string[] {
  if (profile.loc === "local") return ["dataProfile.local"];
  const keys: string[] = [];
  if (profile.res === "eu") keys.push("dataProfile.eu");
  if (profile.ret === "zero") keys.push("dataProfile.zero");
  return keys.length > 0 ? keys : ["dataProfile.standard"];
}

let loading: Promise<void> | null = null;

/**
 * Loads the profile once per page load. 404 means hosted verification is not
 * rolled out on this deployment and nothing is shown; 403 is the
 * data_profile_unresolvable denial; any other failure leaves the line hidden
 * rather than guessing.
 */
export function loadDataProfile(): Promise<void> {
  loading ??= (async () => {
    try {
      const profile = parseProfile(await get<unknown>(DATA_PROFILE_ENDPOINT));
      dataProfileState.set(
        profile ? { status: "resolved", profile } : { status: "unresolved" },
      );
    } catch (err) {
      const parsed = parseApiError(err);
      dataProfileState.set(
        parsed.isForbidden ? { status: "unresolved" } : { status: "hidden" },
      );
    }
  })();
  return loading;
}
