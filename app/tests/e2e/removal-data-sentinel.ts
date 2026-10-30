import { expect } from "@playwright/test";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);
interface Sentinel {
  workload_ref: string;
  host: string;
  user: string;
  private_key_path: string;
  known_hosts_path: string;
  data_path: string;
  sha256: string;
}
function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function isSentinel(value: unknown): value is Sentinel {
  return (
    isRecord(value) &&
    typeof value.workload_ref === "string" &&
    typeof value.host === "string" &&
    /^[A-Za-z0-9][A-Za-z0-9.:-]*$/.test(value.host) &&
    typeof value.user === "string" &&
    /^[A-Za-z_][A-Za-z0-9_-]*$/.test(value.user) &&
    typeof value.private_key_path === "string" &&
    value.private_key_path.length > 0 &&
    typeof value.known_hosts_path === "string" &&
    value.known_hosts_path.length > 0 &&
    typeof value.data_path === "string" &&
    /^\/[A-Za-z0-9_./-]+$/.test(value.data_path) &&
    !value.data_path.split("/").includes("..") &&
    typeof value.sha256 === "string" &&
    /^[a-f0-9]{64}$/.test(value.sha256)
  );
}

/** The reserved real fixture supplies existing non-secret sentinels. Read only,
 * pinned host keys, fixed checksum command; no remote setup or cleanup here. */
export function removalDataSentinelsFromEnv(): (
  workloadRefs: readonly string[],
  dataRoot: string,
) => Promise<void> {
  const raw = process.env.TECHSTACK_RUNTIME_E2E_REMOVE_SENTINELS_JSON;
  if (!raw)
    throw new Error(
      "Remove requires real retained-data sentinels in TECHSTACK_RUNTIME_E2E_REMOVE_SENTINELS_JSON",
    );
  const parsed: unknown = JSON.parse(raw);
  if (!Array.isArray(parsed) || !parsed.length || !parsed.every(isSentinel))
    throw new Error("Remove sentinel fixture is invalid");
  const sentinels: Sentinel[] = parsed;
  return async (workloadRefs, dataRoot) => {
    expect(dataRoot).toMatch(/^\/[A-Za-z0-9_./-]+$/);
    const prefix = dataRoot.replace(/\/+$/, "") + "/";
    expect(
      [...new Set(sentinels.map((entry) => entry.workload_ref))].sort(),
    ).toEqual([...workloadRefs].sort());
    for (const entry of sentinels) {
      expect(
        entry.data_path.startsWith(prefix),
        "sentinel must be inside the actual native application data root",
      ).toBe(true);
      const { stdout } = await execFileAsync(
        "ssh",
        [
          "-o",
          "BatchMode=yes",
          "-o",
          "IdentitiesOnly=yes",
          "-o",
          "StrictHostKeyChecking=yes",
          "-o",
          `UserKnownHostsFile=${entry.known_hosts_path}`,
          "-i",
          entry.private_key_path,
          "-l",
          entry.user,
          entry.host,
          `sha256sum -- '${entry.data_path}'`,
        ],
        { timeout: 30_000, maxBuffer: 4096 },
      );
      expect(
        stdout.trim().split(/\s+/)[0],
        `retained data for ${entry.workload_ref}`,
      ).toBe(entry.sha256);
    }
  };
}
