/**
 * Guard reinstall journey (platform-wkig2). A Guard below the signer move
 * (0.45.17) pins the retired release signer, refuses every update the control
 * plane serves and can never update itself. The owner sees reinstall_required,
 * copies the reinstall command from the server page and runs it as root; the
 * current Guard then runs with the same enrollment and identity key, and no
 * second server or enrollment exists.
 *
 * The old Guard is the archived guard-v0.45.6 release binary. It replaces the
 * Guard of a freshly enrolled server, as a host enrolled before the move runs it.
 */
import { expect, type Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { readFile } from "node:fs/promises";

export const OLD_GUARD_VERSION = "0.45.6";
/** sha256 of the archived guard-v0.45.6 techstack-linux-amd64. */
export const OLD_GUARD_SHA256 =
  "ea54cf72ea3091155a65c2d4f22fdd07339038fb2a104ec8a20cde54988fd6bc";
const SIGNER_MOVE = [0, 45, 17];
const CUSTODY =
  "/etc/techstack/agent-enrollment.json /var/lib/techstack/guard/identity.key";

type Json = Record<string, any>;

export interface GuardReinstallDeps {
  page: Page;
  serverPage: string;
  /** Runs a command on the server as root. */
  root: (command: string, timeout?: number) => Promise<string>;
  /** Copies a local file to the server. */
  upload: (localPath: string, remotePath: string) => Promise<void>;
  readServer: () => Promise<Json>;
  api: (method: "GET" | "POST" | "DELETE", path: string, body?: unknown) => Promise<Json>;
  delay: (ms: number) => Promise<unknown>;
}

function atLeastSignerMove(version: string): boolean {
  const parts = version.replace(/^v/, "").split(".").map(Number);
  for (let i = 0; i < 3; i += 1) {
    if (parts[i] !== SIGNER_MOVE[i]) return parts[i] > SIGNER_MOVE[i];
  }
  return true;
}

async function waitForGuard(
  deps: GuardReinstallDeps,
  accept: (guard: Json) => boolean,
  what: string,
  limitMs: number,
): Promise<Json> {
  const deadline = Date.now() + limitMs;
  let last: Json = {};
  while (Date.now() < deadline) {
    last = (await deps.readServer()).updates?.guard ?? {};
    if (accept(last)) return last;
    await deps.delay(10_000);
  }
  throw new Error(`The server did not show ${what} within ${limitMs / 1000}s; last=${JSON.stringify(last)}`);
}

export async function runGuardReinstallJourney(
  deps: GuardReinstallDeps,
  oldGuardPath: string,
): Promise<Json> {
  const oldGuard = await readFile(oldGuardPath);
  expect(createHash("sha256").update(oldGuard).digest("hex"), "the archived guard-v0.45.6 binary").toBe(OLD_GUARD_SHA256);

  // The server runs the old Guard on its existing enrollment.
  await deps.upload(oldGuardPath, "/tmp/techstack-guard-v0.45.6");
  await deps.root(
    "install -m 0755 /tmp/techstack-guard-v0.45.6 /usr/local/bin/.techstack.old && rm -f /tmp/techstack-guard-v0.45.6 && " +
      "systemctl stop techstack-agent.service && mv -f /usr/local/bin/.techstack.old /usr/local/bin/techstack && " +
      "install -m 0755 /usr/local/bin/techstack /usr/local/libexec/techstack-stackkit-operations && " +
      "systemctl start techstack-agent.service",
  );
  expect(await deps.root("/usr/local/bin/techstack version")).toContain(`v${OLD_GUARD_VERSION}`);
  const custodyBefore = await deps.root(`sha256sum ${CUSTODY}`);

  // API and UI: the Guard needs a reinstall.
  const required = await waitForGuard(deps, (g) => g.state === "reinstall_required", "reinstall_required", 10 * 60_000);
  expect(required.version).toBe(OLD_GUARD_VERSION);
  await deps.page.goto(deps.serverPage, { waitUntil: "domcontentloaded" });
  const updatedFact = deps.page.locator('[data-testid="guard-fact"][data-fact="updated"]');
  await expect(updatedFact).toHaveAttribute("data-state", "attention", { timeout: 60_000 });
  const section = deps.page.getByTestId("guard-reinstall");
  await expect(section).toBeVisible();

  // The owner copies the command the server page mints and runs it as root.
  await deps.page.getByTestId("guard-reinstall-prepare").click();
  const command = (await deps.page.getByTestId("guard-reinstall-command").textContent())?.trim() ?? "";
  expect(command).toContain("bash -s -- --reinstall-guard");
  expect(command, "the capability never reaches sudo's arguments").toContain("sudo --preserve-env=KOMBI_SERVER,KOMBI_TOKEN");
  const output = await deps.root(command, 10 * 60_000);
  const receiptLine = output.split("\n").find((line) => line.startsWith("GUARD_REINSTALL ")) ?? "";
  const reinstalled = /result=reinstalled from=(\S+) to=(\S+) sha256=([0-9a-f]{64}) enrollment=unchanged identity_key=unchanged/.exec(receiptLine);
  expect(reinstalled, `the reinstall receipt: ${receiptLine}`).not.toBeNull();
  const [, from, to, sha256] = reinstalled!;
  expect(from).toBe(OLD_GUARD_VERSION);
  expect(atLeastSignerMove(to), `${to} is at or past the signer move`).toBe(true);
  expect(await deps.root(`sha256sum ${CUSTODY}`), "enrollment and identity key are byte for byte unchanged").toBe(custodyBefore);
  expect(await deps.root("systemctl is-active techstack-agent.service")).toBe("active");

  // API and UI: the Guard is current again.
  const current = await waitForGuard(deps, (g) => g.state !== "reinstall_required" && g.version === to, `Guard ${to}`, 10 * 60_000);
  await deps.page.reload({ waitUntil: "domcontentloaded" });
  await expect(section).toBeHidden({ timeout: 60_000 });

  // Agent parity: the agent route answers the same command for the same mode.
  const agent = await deps.api("POST", "/api/v1/tunnel/install-command", { mode: "reinstall-guard" });
  expect(String(agent.linux ?? "")).toContain("bash -s -- --reinstall-guard");
  expect(String(agent.linux).replace(/KOMBI_TOKEN='[^']*'/, "")).toBe(command.replace(/KOMBI_TOKEN='[^']*'/, ""));

  // The download capabilities were never redeemed; revoke them.
  const tokens = await deps.api("GET", "/api/v1/trust/pairing-tokens");
  const reinstallTokens = (Array.isArray(tokens) ? tokens : (tokens.tokens ?? [])).filter((t: Json) => t.name === "Guard reinstall");
  for (const token of reinstallTokens) {
    await deps.api("DELETE", `/api/v1/trust/pairing-tokens/${encodeURIComponent(String(token.id))}`);
  }

  return {
    schemaVersion: "techstack.guard-reinstall/v1",
    oldGuard: { version: OLD_GUARD_VERSION, sha256: OLD_GUARD_SHA256 },
    required: { api: required.state, ui: "attention" },
    reinstall: { from, to, sha256, enrollment: "unchanged", identityKey: "unchanged" },
    current: { api: current.state, version: current.version },
    agentParity: true,
    revokedCapabilities: reinstallTokens.length,
    status: "passed",
  };
}
