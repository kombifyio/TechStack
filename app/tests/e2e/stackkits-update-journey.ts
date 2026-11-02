/**
 * StackKits update journey (kombify-StackKits-9r4e, -l38u; D-2026-11-02-03).
 * Techstack manages StackKits updates: the server's Guard section shows the
 * update setting (default: approve each update) and, for a server behind the
 * published release, the proposal with highlights for its activated use cases
 * and the public changelog link. Nothing installs until Approve, unless the
 * release carries a security patch for an app on the server; Approve runs the
 * governed upgrade, which the Guard rolls back when it fails.
 */
import { expect, type Page } from "@playwright/test";

type Json = Record<string, any>;

export interface StackKitsUpdateJourneyDeps {
  page: Page;
  serverPage: string;
  workerId: string;
  api: (method: "GET" | "PUT", path: string, body?: unknown) => Promise<Json>;
  waitForJob: (id: string) => Promise<Json>;
  delay: (ms: number) => Promise<unknown>;
}

export interface StackKitsUpdateJourneyEvidence {
  worker_id: string;
  installed?: string;
  available?: string;
  changelog_url?: string;
  highlights: string[];
  security: boolean;
  upgrade_job_id?: string;
  upgrade_trigger?: "approve" | "security";
  outcome?: string;
  behavior_round_trip: string[];
}

export async function runStackKitsUpdateJourney(
  deps: StackKitsUpdateJourneyDeps,
): Promise<StackKitsUpdateJourneyEvidence> {
  const path = `/api/v1/workers/${encodeURIComponent(deps.workerId)}/stackkits-update`;
  const read = () => deps.api("GET", path);
  let view = await read();
  expect(view.behavior, "a server approves each update by default").toBe("approve");
  const evidence: StackKitsUpdateJourneyEvidence = {
    worker_id: deps.workerId,
    installed: view.installed,
    available: view.available,
    changelog_url: view.changelog_url,
    highlights: [],
    security: false,
    behavior_round_trip: [],
  };
  expect(String(view.changelog_url ?? ""), "the public StackKits changelog").toMatch(/^https:\/\//);

  await deps.page.goto(deps.serverPage, { waitUntil: "domcontentloaded" });
  const section = deps.page.getByTestId("guard-stackkits-update");
  await expect(section).toBeVisible({ timeout: 60_000 });
  await expect(deps.page.getByTestId("guard-updates-behavior-approve")).toHaveAttribute("aria-checked", "true");

  if (view.available) {
    const proposal = view.proposal as Json;
    evidence.highlights = (proposal.highlights ?? []).map((entry: Json) => String(entry.use_case_id));
    evidence.security = proposal.security?.affects_node === true;
    await expect(deps.page.getByTestId("guard-updates-proposal")).toBeVisible();
    await expect(deps.page.getByTestId("guard-updates-changelog")).toHaveAttribute("href", proposal.changelog_url);
    if (evidence.highlights.length > 0) {
      await expect(deps.page.getByTestId("guard-updates-highlight").first()).toHaveAttribute("data-use-case", evidence.highlights[0]);
    }
    let jobId: string;
    if (evidence.security) {
      // l38u: a security patch for an app on this server starts without a click.
      await expect
        .poll(async () => (view = await read()).started?.job_id ?? "", { timeout: 300_000, intervals: [10_000] })
        .not.toBe("");
      expect(view.started.trigger).toBe("security");
      jobId = String(view.started.job_id);
      evidence.upgrade_trigger = "security";
    } else {
      // 9r4e: with the default setting nothing installs until Approve.
      await deps.delay(90_000);
      view = await read();
      expect(view.started?.version, "no update starts while the server asks for approval").not.toBe(view.available);
      await deps.page.reload({ waitUntil: "domcontentloaded" });
      await deps.page.getByTestId("guard-updates-approve").click();
      const progress = deps.page.getByTestId("guard-updates-progress");
      await expect(progress).toBeVisible({ timeout: 60_000 });
      jobId = String(await progress.getAttribute("data-job-id"));
      evidence.upgrade_trigger = "approve";
    }
    evidence.upgrade_job_id = jobId;
    const job = await deps.waitForJob(jobId);
    view = await read();
    evidence.outcome = view.last_upgrade?.outcome;
    expect(view.last_upgrade?.version, "the governed upgrade recorded its target").toBe(proposal.available);
    if (job.state === "completed") {
      expect(view.last_upgrade?.outcome).toBe("succeeded");
    } else {
      // The Guard rolled the failed upgrade back: the server keeps its release.
      expect(view.last_upgrade?.outcome).toBe("failed");
      expect(view.installed).toBe(evidence.installed);
    }
    await deps.page.reload({ waitUntil: "domcontentloaded" });
    await expect(deps.page.getByTestId("guard-updates-outcome")).toHaveAttribute("data-outcome", String(view.last_upgrade?.outcome));
    if (evidence.security) {
      await expect(deps.page.getByTestId("guard-updates-security-notice")).toBeVisible();
      await expect(deps.page.getByTestId("guard-updates-security-changelog")).toHaveAttribute("href", proposal.changelog_url);
    }
  }

  // The setting persists per server, in the UI and through the API.
  await deps.page.getByTestId("guard-updates-behavior-automatic").click();
  await expect(deps.page.getByTestId("guard-stackkits-update")).toHaveAttribute("data-behavior", "automatic");
  view = await read();
  evidence.behavior_round_trip.push(view.behavior);
  expect(view.behavior).toBe("automatic");
  await deps.page.reload({ waitUntil: "domcontentloaded" });
  await expect(deps.page.getByTestId("guard-updates-behavior-automatic")).toHaveAttribute("aria-checked", "true", { timeout: 60_000 });
  view = await deps.api("PUT", `${path}/behavior`, { behavior: "approve" });
  evidence.behavior_round_trip.push(view.behavior);
  expect(view.behavior).toBe("approve");
  return evidence;
}
