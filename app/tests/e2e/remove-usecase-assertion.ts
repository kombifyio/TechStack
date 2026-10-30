import { expect, type Page } from "@playwright/test";
import { authorizedFetch, type RuntimeBearer } from "./runtime-auth";
import type { UseCaseConfigurationView } from "../../src/lib/api/useCaseConfiguration";
import type {
  UseCaseRemovalRequest,
  UseCaseRemovalSelection,
} from "../../src/lib/api/useCaseRemoval";

export function decodeSupersededRemovalFixture(raw: string): {
  originalRequest: UseCaseRemovalRequest;
  originalKey: string;
  originalJobIds: string[];
} {
  function isRecord(value: unknown): value is Record<string, unknown> {
    return value !== null && typeof value === "object" && !Array.isArray(value);
  }
  const value: unknown = JSON.parse(raw);
  if (!isRecord(value) || !isRecord(value.originalRequest))
    throw new Error("Invalid real superseded removal fixture");
  const request = value.originalRequest;
  if (
    (request.selector_kind !== "workload" &&
      request.selector_kind !== "use_case") ||
    typeof request.selector_ref !== "string" ||
    !request.selector_ref ||
    typeof request.expected_spec_hash !== "string" ||
    !/^sha256:[a-f0-9]{64}$/.test(request.expected_spec_hash) ||
    typeof value.originalKey !== "string" ||
    !value.originalKey ||
    value.originalKey.length > 128 ||
    !Array.isArray(value.originalJobIds) ||
    !value.originalJobIds.length ||
    !value.originalJobIds.every(
      (id: unknown): id is string => typeof id === "string" && id.length > 0,
    )
  ) {
    throw new Error(
      "Superseded removal needs the real original request, key and completed jobs",
    );
  }
  return {
    originalRequest: {
      selector_kind: request.selector_kind,
      selector_ref: request.selector_ref,
      expected_spec_hash: request.expected_spec_hash,
    },
    originalKey: value.originalKey,
    originalJobIds: value.originalJobIds,
  };
}

/** Run only against the caller's reserved real Foundation fixture. The caller
 * owns its data sentinel and final cleanup; this step never deletes a server. */
export async function assertRemoveInstalledUseCase(options: {
  page: Page;
  token: RuntimeBearer;
  stackId: string;
  selection: UseCaseRemovalSelection;
  url: (path: string) => string;
  waitForJob: (id: string) => Promise<Record<string, unknown>>;
  verifyRetainedData: (
    workloadRefs: readonly string[],
    dataRoot: string,
  ) => Promise<void>;
}) {
  const { page, token, stackId, selection, url, waitForJob } = options;
  const stackPath = `/api/v1/stacks/${encodeURIComponent(stackId)}`;
  async function read(path: string) {
    const response = await authorizedFetch(token, url(path), {
      headers: { Accept: "application/json" },
    });
    expect(response.ok, `${path} HTTP ${response.status}`).toBe(true);
    const parsed = await response.json();
    return parsed.data ?? parsed;
  }
  const before: UseCaseConfigurationView = await read(
    `${stackPath}/use-cases/configuration`,
  );
  expect(before.kit_deployment_id).toBe(stackId);
  expect(
    before.retained_removal_available,
    "the actual pinned release must advertise retained removal",
  ).toBe(true);
  const group = before.use_cases.find((entry) =>
    selection.selector_kind === "use_case"
      ? entry.id === selection.selector_ref
      : entry.workloads.some(
          (member) => member.workload_ref === selection.selector_ref,
        ),
  );
  expect(
    group,
    "selection must belong to the actual installed catalog",
  ).toBeTruthy();
  const members = group!.workloads.filter(
    (member) =>
      selection.selector_kind === "use_case" ||
      member.workload_ref === selection.selector_ref,
  );
  expect(members.length).toBeGreaterThan(0);
  expect(members.every((member) => member.removal_eligible === true)).toBe(
    true,
  );
  const refs = members.map((member) => member.workload_ref).sort();
  const exportBefore = await read(`${stackPath}/export`);
  const baseline = exportBefore.stack_spec ?? exportBefore.kombination;
  const stackBefore = await read(stackPath);
  const bindings = (
    value: Record<string, unknown>[] | { servers: Record<string, unknown>[] },
  ) =>
    (Array.isArray(value) ? value : value.servers)
      .map((server) => ({
        id: server.id,
        worker_id: server.worker_id,
        kit_deployment_id: server.kit_deployment_id,
        provider: server.provider,
      }))
      .sort((a, b) => String(a.id).localeCompare(String(b.id)));
  const serversBefore = bindings(await read("/api/v1/servers"));
  await options.verifyRetainedData(refs, baseline.storage.dataRoot);

  await page.goto(`/stacks/${encodeURIComponent(stackId)}/use-cases/remove`);
  await expect(page.getByTestId("remove-use-case-page")).toHaveAttribute(
    "data-stack-id",
    stackId,
  );
  await page.getByTestId("remove-use-case").selectOption(group!.id);
  await page.getByTestId("remove-scope").selectOption(selection.selector_kind);
  if (selection.selector_kind === "workload")
    await page
      .getByTestId("remove-workload")
      .selectOption(selection.selector_ref);
  await expect(page.getByTestId("remove-selected-member")).toHaveCount(
    refs.length,
  );
  await expect(page.locator("textarea")).toHaveCount(0);
  await expect(page.getByTestId("remove-submit")).toBeDisabled();
  await page.getByTestId("remove-confirm").check();
  await expect(page.getByTestId("remove-submit")).toBeEnabled();
  const path = `${stackPath}/use-cases/remove`;
  const acceptedPromise = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === path,
    { timeout: 90_000 },
  );
  await page.getByTestId("remove-submit").click();
  const response = await acceptedPromise;
  expect(response.status()).toBe(202);
  const request = {
    ...selection,
    expected_spec_hash: before.expected_spec_hash,
  };
  expect(response.request().postDataJSON()).toEqual(request);
  const key = response.request().headers()["idempotency-key"];
  expect(key).toBeTruthy();
  const body = await response.json();
  const accepted = body.data ?? body;
  expect(accepted.operation).toBe("use_case_remove");
  expect(accepted.kit_deployment_id).toBe(stackId);
  expect(accepted.selector_kind).toBe(selection.selector_kind);
  expect(accepted.selector_ref).toBe(selection.selector_ref);
  expect(accepted.removed_workload_refs).toEqual(refs);
  expect(accepted.data_disposition).toBe("retain");
  expect(accepted.previous_spec_hash).toBe(before.expected_spec_hash);
  expect(accepted.spec_hash).not.toBe(accepted.previous_spec_hash);
  expect(accepted.job_ids.length).toBeGreaterThan(0);
  const replay = await authorizedFetch(token, url(path), {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": key },
    body: JSON.stringify(request),
  });
  expect(replay.status).toBe(202);
  const replayBody = await replay.json();
  expect((replayBody.data ?? replayBody).job_ids).toEqual(accepted.job_ids);
  expect((replayBody.data ?? replayBody).run_id).toBe(accepted.run_id);
  for (const id of accepted.job_ids as string[]) {
    const job = await waitForJob(id);
    expect(job.state).toBe("completed");
    expect(job.kit_deployment_id ?? job.stack_id).toBe(stackId);
    const result = job.result as Record<string, any>;
    expect(result.operation).toBe("advanced_change_set");
    expect(result.success).toBe(true);
    const receipt = result.wizard_extend_receipt;
    expect(receipt.run_id).toBe(accepted.run_id);
    expect(receipt.stack_id).toBe(stackId);
    expect(receipt.group_job_ids).toBe(accepted.job_ids.join(","));
    expect(receipt.removal_selector_kind).toBe(selection.selector_kind);
    expect(receipt.removal_selector_ref).toBe(selection.selector_ref);
    expect(receipt.removal_workload_refs).toBe(refs.join(","));
    expect(receipt.removal_previous_spec_hash).toBe(
      accepted.previous_spec_hash,
    );
    expect(receipt.removal_spec_hash).toBe(accepted.spec_hash);
    expect(receipt.data_disposition).toBe("retain");
  }
  await expect(page.getByTestId("use-case-run-progress")).toHaveAttribute(
    "data-complete",
    "true",
    { timeout: 180_000 },
  );
  await expect(page.getByTestId("use-case-run-job")).toHaveCount(
    accepted.job_ids.length,
  );
  const exportAfter = await read(`${stackPath}/export`);
  const updated = exportAfter.stack_spec ?? exportAfter.kombination;
  const expected = structuredClone(baseline);
  const modules = new Set(members.map((member) => member.module_ref));
  for (const member of members) {
    expect(expected.workloads[member.workload_ref]).toBeTruthy();
    delete expected.workloads[member.workload_ref];
    if (expected.modules) delete expected.modules[member.module_ref];
  }
  for (const [id, route] of Object.entries(expected.routes ?? {})) {
    if (modules.has((route as { moduleRef: string }).moduleRef))
      delete expected.routes[id];
  }
  expect(updated).toEqual(expected);
  const after: UseCaseConfigurationView = await read(
    `${stackPath}/use-cases/configuration`,
  );
  expect(after.expected_spec_hash).toBe(accepted.spec_hash);
  expect(
    after.use_cases
      .flatMap((entry) => entry.workloads)
      .some((member) => refs.includes(member.workload_ref)),
  ).toBe(false);
  const stackAfter = await read(stackPath);
  expect(stackAfter.id ?? stackAfter.kit_deployment_id).toBe(
    stackBefore.id ?? stackBefore.kit_deployment_id,
  );
  expect(stackAfter.lease_id).toBe(stackBefore.lease_id);
  expect(bindings(await read("/api/v1/servers"))).toEqual(serversBefore);
  await options.verifyRetainedData(refs, baseline.storage.dataRoot);
  await page.getByTestId("remove-review").click();
  if (after.use_cases.length === 0)
    await expect(page.getByTestId("remove-empty")).toBeVisible();
  else await expect(page.getByTestId("remove-submit")).toBeDisabled();
}

/** The real fault harness first loses the final acceptance-ledger write after
 * jobs exist, waits for those jobs, then re-adds the selection through the
 * product. The old checkpoint must not overwrite that later desired intent. */
export async function assertRemoveSupersededCheckpointRetry(options: {
  token: RuntimeBearer;
  stackId: string;
  originalRequest: UseCaseRemovalRequest;
  originalKey: string;
  originalJobIds: string[];
  url: (path: string) => string;
}) {
  const { token, stackId, url } = options;
  const stackPath = `/api/v1/stacks/${encodeURIComponent(stackId)}`;
  async function read(path: string) {
    const response = await authorizedFetch(token, url(path));
    expect(response.ok, `${path} HTTP ${response.status}`).toBe(true);
    const body = await response.json();
    return body.data ?? body;
  }
  expect(options.originalJobIds.length).toBeGreaterThan(0);
  for (const id of options.originalJobIds) {
    const job = await read(`/api/v1/jobs/${encodeURIComponent(id)}`);
    expect(job.state).toBe("completed");
  }
  const before = await read(`${stackPath}/export`);
  const current: UseCaseConfigurationView = await read(
    `${stackPath}/use-cases/configuration`,
  );
  expect(current.expected_spec_hash).toBe(
    options.originalRequest.expected_spec_hash,
  );
  const jobsPath = `/api/v1/jobs?stack_id=${encodeURIComponent(stackId)}&per_page=100`;
  const beforeJobs = await read(jobsPath);
  const response = await authorizedFetch(
    token,
    url(`${stackPath}/use-cases/remove`),
    {
      method: "POST",
      headers: {
        "Content-Type": "application/json",
        "Idempotency-Key": options.originalKey,
      },
      body: JSON.stringify(options.originalRequest),
    },
  );
  expect(response.status).toBe(409);
  const denial = await response.json();
  expect(denial.error.details.reason_code).toBe("removal_baseline_changed");
  expect(await read(`${stackPath}/export`)).toEqual(before);
  expect(await read(jobsPath)).toEqual(beforeJobs);
}
