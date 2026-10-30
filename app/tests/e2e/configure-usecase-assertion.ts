import { expect, type Page } from "@playwright/test";
import { authorizedFetch, type RuntimeBearer } from "./runtime-auth";
import type {
  ConfigureInstalledWorkloadRequest,
  UseCaseConfigurationView,
} from "../../src/lib/api/useCaseConfiguration";

/** Extend the existing real-server journey; caller owns the synthetic fixture and its cleanup. */
export async function assertConfigureInstalledWorkload(options: {
  page: Page;
  token: RuntimeBearer;
  stackId: string;
  workloadRef: string;
  changes: Omit<ConfigureInstalledWorkloadRequest, "expected_spec_hash">;
  url: (path: string) => string;
  waitForJob: (id: string) => Promise<Record<string, unknown>>;
}) {
  const { page, token, stackId, workloadRef, changes, url, waitForJob } =
    options;
  const stackPath = `/api/v1/stacks/${encodeURIComponent(stackId)}`;
  const configurationPath = `${stackPath}/use-cases/configuration`;
  const configurePath = `${stackPath}/use-cases/${encodeURIComponent(workloadRef)}/configure`;
  async function read(path: string, accept = "application/json") {
    const response = await authorizedFetch(token, url(path), {
      headers: { Accept: accept },
    });
    expect(response.ok, `${path} HTTP ${response.status}`).toBe(true);
    const parsed = await response.json();
    return parsed.data ?? parsed;
  }
  const before = (await read(configurationPath)) as UseCaseConfigurationView;
  expect(before.kit_deployment_id).toBe(stackId);
  // Membership comes from the real catalog projection, never a slug mapping.
  const group = before.use_cases.find((entry) =>
    entry.workloads.some((member) => member.workload_ref === workloadRef),
  );
  expect(
    group,
    "the fixture workload must belong to an actual installed catalog use case",
  ).toBeTruthy();
  const member = group!.workloads.find(
    (entry) => entry.workload_ref === workloadRef,
  )!;
  expect(member.eligible).toBe(true);
  const specBefore = await read(`${stackPath}/export`);
  const baseline = specBefore.stack_spec ?? specBefore.kombination;
  expect(baseline?.workloads?.[workloadRef]).toBeTruthy();
  if (process.env.TECHSTACK_RUNTIME_E2E_CONFIGURE_MISSING_MODULE === "1") {
    expect(baseline.modules?.[member.module_ref], "this regression fixture must omit the selected module entry").toBeUndefined();
  }
  const stackBefore = await read(stackPath);
  const serversBefore = await read("/api/v1/servers");
  const serverBindings = (servers: Array<Record<string, unknown>>) =>
    servers
      .map((server) => ({
        id: server.id,
        worker_id: server.worker_id,
        kit_deployment_id: server.kit_deployment_id,
        provider: server.provider,
      }))
      .sort((a, b) => String(a.id).localeCompare(String(b.id)));
  const initialBindings = serverBindings(
    Array.isArray(serversBefore) ? serversBefore : serversBefore.servers,
  );

  await page.goto(
    `/stacks/${encodeURIComponent(stackId)}/use-cases/configure?workload=${encodeURIComponent(workloadRef)}`,
  );
  await expect(page.getByTestId("configure-use-case-page")).toHaveAttribute(
    "data-stack-id",
    stackId,
  );
  await expect(page.getByTestId("configure-use-case")).toHaveValue(group!.id);
  await expect(page.getByTestId("configure-workload")).toHaveValue(workloadRef);
  await expect(page.getByTestId("configure-submit")).toBeDisabled();
  await expect(page.getByTestId("easy-step-2")).toHaveCount(0);
  await expect(page.locator("textarea")).toHaveCount(0);
  for (const profile of ["compute", "storage", "accelerator"] as const) {
    const value = changes[`${profile}_profile`];
    if (value) {
      expect(member.profiles[profile].options).toContain(value);
      expect(member.profiles[profile].current).not.toBe(value);
      await page
        .getByTestId(`configure-profile-${profile}`)
        .selectOption(value);
    }
  }
  for (const [key, value] of Object.entries(changes.settings ?? {})) {
    const field = member.settings.find((setting) => setting.key === key);
    expect(
      field,
      "the edited public setting must be described by the native catalog",
    ).toBeTruthy();
    expect(typeof value).toBe(field!.type);
    expect(field!.current).not.toEqual(value);
    const input = page.getByTestId(`configure-setting-${key}`);
    if (field!.options?.length) {
      const index = field!.options.findIndex((option) =>
        Object.is(option, value),
      );
      expect(index).toBeGreaterThanOrEqual(0);
      await input.selectOption(String(index));
    } else if (field!.type === "boolean")
      await input.setChecked(value === true);
    else {
      if (field!.type === "number") {
        await input.fill("");
        await expect(page.getByTestId("configure-submit")).toBeDisabled();
        expect(
          await input.evaluate((element) =>
            (element as HTMLInputElement).checkValidity(),
          ),
        ).toBe(false);
      }
      await input.fill(String(value));
    }
  }
  for (const [key, value] of Object.entries(changes.secret_refs ?? {})) {
    const field = member.secret_refs.find((entry) => entry.key === key);
    expect(field?.options).toContain(value);
    expect(value).toMatch(/^secret:\/\//);
    await page.getByTestId(`configure-secret-${key}`).selectOption(value);
  }
  await expect(page.getByTestId("configure-submit")).toBeEnabled();
  const responsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "POST" &&
      new URL(response.url()).pathname === configurePath,
    { timeout: 90_000 },
  );
  await page.getByTestId("configure-submit").click();
  const response = await responsePromise;
  expect(response.status()).toBe(202);
  const submitted = response.request().postDataJSON();
  expect(submitted).toEqual({
    expected_spec_hash: before.expected_spec_hash,
    ...changes,
  });
  const key = response.request().headers()["idempotency-key"];
  expect(key).toBeTruthy();
  const acceptedBody = await response.json();
  const accepted = acceptedBody.data ?? acceptedBody;
  expect(accepted.kit_deployment_id).toBe(stackId);
  expect(accepted.state).toBe("provisioning");
  expect(accepted.previous_spec_hash).toBe(before.expected_spec_hash);
  expect(accepted.spec_hash).toBeTruthy();
  expect(accepted.spec_hash).not.toBe(accepted.previous_spec_hash);
  expect(accepted.job_ids.length).toBeGreaterThan(0);
  const replay = await authorizedFetch(token, url(configurePath), {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": key },
    body: JSON.stringify(submitted),
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
    expect(receipt.configuration_workload_ref).toBe(workloadRef);
    expect(receipt.configuration_previous_spec_hash).toBe(
      accepted.previous_spec_hash,
    );
    expect(receipt.configuration_spec_hash).toBe(accepted.spec_hash);
  }
  await expect(page.getByTestId("use-case-run-progress")).toHaveAttribute(
    "data-complete",
    "true",
    { timeout: 180_000 },
  );
  await expect(page.getByTestId("use-case-run-job")).toHaveCount(
    accepted.job_ids.length,
  );

  const specAfter = await read(`${stackPath}/export`);
  const updated = specAfter.stack_spec ?? specAfter.kombination;
  // Only the selected workload inputs and its requested module profiles may change.
  const withoutSelected = (spec: Record<string, any>) => ({
    ...Object.fromEntries(
      Object.entries(spec).filter(
        ([name]) => name !== "workloads" && name !== "metadata" && name !== "modules",
      ),
    ),
    workloads: Object.fromEntries(
      Object.entries(spec.workloads).filter(([id]) => id !== workloadRef),
    ),
    modules: Object.fromEntries(
      Object.entries(spec.modules ?? {}).filter(([id]) => id !== member.module_ref),
    ),
  });
  expect(withoutSelected(updated)).toEqual(withoutSelected(baseline));
  const expectedModule = { ...baseline.modules?.[member.module_ref] };
  const changesProfile = Boolean(changes.compute_profile || changes.storage_profile || changes.accelerator_profile);
  if (!baseline.modules?.[member.module_ref] && changesProfile) expectedModule.enabled = true;
  for (const profile of ["compute", "storage", "accelerator"] as const) {
    const value = changes[`${profile}_profile`];
    if (value) expectedModule[`${profile}Profile`] = value;
  }
  if (!baseline.modules?.[member.module_ref] && !changesProfile) {
    expect(updated.modules?.[member.module_ref]).toBeUndefined();
  } else expect(updated.modules?.[member.module_ref]).toEqual(expectedModule);
  const expectedWorkload = { ...baseline.workloads[workloadRef] };
  if (changes.settings) expectedWorkload.settings = { ...expectedWorkload.settings, ...changes.settings };
  if (changes.secret_refs) expectedWorkload.secretRefs = { ...expectedWorkload.secretRefs, ...changes.secret_refs };
  expect(updated.workloads[workloadRef]).toEqual(expectedWorkload);
  for (const [setting, value] of Object.entries(changes.settings ?? {}))
    expect(updated.workloads[workloadRef].settings[setting]).toEqual(value);
  for (const [secret, value] of Object.entries(changes.secret_refs ?? {}))
    expect(updated.workloads[workloadRef].secretRefs[secret]).toBe(value);
  const afterView = (await read(configurationPath)) as UseCaseConfigurationView;
  const afterMember = afterView.use_cases
    .flatMap((entry) => entry.workloads)
    .find((entry) => entry.workload_ref === workloadRef);
  expect(afterMember).toBeTruthy();
  for (const profile of ["compute", "storage", "accelerator"] as const) {
    const value = changes[`${profile}_profile`];
    if (value) expect(afterMember!.profiles[profile].current).toBe(value);
  }
  for (const [setting, value] of Object.entries(changes.settings ?? {}))
    expect(
      afterMember!.settings.find((entry) => entry.key === setting)?.current,
    ).toEqual(value);
  for (const [secret, value] of Object.entries(changes.secret_refs ?? {}))
    expect(
      afterMember!.secret_refs.find((entry) => entry.key === secret)?.current,
    ).toBe(value);
  const stackAfter = await read(stackPath);
  expect(stackAfter.id ?? stackAfter.kit_deployment_id).toBe(
    stackBefore.id ?? stackBefore.kit_deployment_id,
  );
  expect(stackAfter.lease_id).toBe(stackBefore.lease_id);
  const serversAfter = await read("/api/v1/servers");
  expect(
    serverBindings(
      Array.isArray(serversAfter) ? serversAfter : serversAfter.servers,
    ),
  ).toEqual(initialBindings);
  await page.getByTestId("configure-review").click();
  await expect(page.getByTestId("configure-workload")).toHaveValue(workloadRef);
  await expect(page.getByTestId("configure-submit")).toBeDisabled();
}

/** The real fault harness supplies an already checkpointed, not yet accepted attempt
 * and puts its server into maintenance or locks its service before calling this.
 * It owns recovery; this assertion never changes production admission controls. */
export async function assertConfigureBlockedCheckpointRetry(options: {
  token: RuntimeBearer;
  stackId: string;
  workloadRef: string;
  request: ConfigureInstalledWorkloadRequest;
  idempotencyKey: string;
  url: (path: string) => string;
  readJobIds: () => Promise<string[]>;
  unblock: () => Promise<void>;
  expectedJobIds: string[];
  expectedSpecHash: string;
}) {
  const before = (await options.readJobIds()).slice().sort();
  const response = await authorizedFetch(options.token, options.url(
    `/api/v1/stacks/${encodeURIComponent(options.stackId)}/use-cases/${encodeURIComponent(options.workloadRef)}/configure`,
  ), {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": options.idempotencyKey },
    body: JSON.stringify(options.request),
  });
  expect(response.status, "a checkpoint retry must still respect live service locks and maintenance").toBe(409);
  const failure = await response.json();
  const details = failure.error?.details ?? failure.details ?? failure.error?.data ?? failure.data ?? {};
  expect(details.reason_code === "service_locked" || details.reason === "maintenance_active", "the retry must be refused by the live lock or maintenance guard").toBe(true);
  expect((await options.readJobIds()).slice().sort(), "blocked retry must enqueue no job").toEqual(before);
  await options.unblock();
  const resumed = await authorizedFetch(options.token, options.url(
    `/api/v1/stacks/${encodeURIComponent(options.stackId)}/use-cases/${encodeURIComponent(options.workloadRef)}/configure`,
  ), {
    method: "POST",
    headers: { "Content-Type": "application/json", "Idempotency-Key": options.idempotencyKey },
    body: JSON.stringify(options.request),
  });
  expect(resumed.status).toBe(202);
  const acceptedBody = await resumed.json();
  const accepted = acceptedBody.data ?? acceptedBody;
  expect(accepted.spec_hash).toBe(options.expectedSpecHash);
  expect(accepted.job_ids).toEqual(options.expectedJobIds);
}
