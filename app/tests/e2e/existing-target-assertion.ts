import { expect } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { authorizedFetch, type RuntimeBearer } from "./runtime-auth";

type TargetRecord = Record<string, any>;

/** Adds one use case through the real owner API, preserving the existing foundation. */
export async function assertExistingTargetExtension(options: {
  token: RuntimeBearer;
  stackId: string;
  serverId?: string;
  expectedRevision: string;
  previewOnly?: boolean;
  url: (path: string) => string;
  waitForJob: (id: string) => Promise<Record<string, unknown>>;
}) {
  const { token, stackId, url, waitForJob } = options;
  if (!stackId.trim()) throw new Error("existing_target_stack_id_required");
  if (!/^[a-f0-9]{40}$/.test(options.expectedRevision))
    throw new Error("existing_target_exact_source_revision_required");
  async function request(path: string, body?: unknown, key?: string) {
    const response = await authorizedFetch(token, url(path), {
      ...(body
        ? {
            method: "POST",
            headers: {
              "Content-Type": "application/json",
              ...(key ? { "X-Idempotency-Key": key } : {}),
            },
            body: JSON.stringify(body),
          }
        : { headers: { "Cache-Control": "no-store" } }),
      signal: AbortSignal.timeout(30_000),
    });
    if (!response.ok)
      throw new Error(
        `existing_target_api_${response.status}:${path.split("?")[0]}`,
      );
    const parsed = await response.json();
    return parsed.data ?? parsed;
  }
  async function sourceBinding() {
    const health = await request("/api/v1/health");
    expect(health.status, "existing_target_product_unhealthy").toBe("healthy");
    expect(health.revision, "existing_target_product_source_mismatch").toBe(
      options.expectedRevision,
    );
    if (options.previewOnly)
      expect(health.release_channel, "existing_target_requires_preview").toBe(
        "preview",
      );
    return { revision: health.revision, channel: health.release_channel };
  }
  // Inventory accepts server_id, cursor and limit, not stack_id. Each next_cursor
  // is bound to the authenticated scope and the selected server.
  async function inventory(kind: "servers" | "services", serverId?: string) {
    const rows: TargetRecord[] = [],
      cursors = new Set<string>();
    let cursor = "";
    do {
      const query = new URLSearchParams({ limit: "100" });
      if (serverId) query.set("server_id", serverId);
      if (cursor) query.set("cursor", cursor);
      const page = await request(`/api/v1/inventory/${kind}?${query}`);
      if (!Array.isArray(page[kind]))
        throw new Error(`existing_target_${kind}_shape_invalid`);
      rows.push(...page[kind]);
      cursor = page.next_cursor ?? "";
      if (typeof cursor !== "string" || (cursor && cursors.has(cursor)))
        throw new Error(`existing_target_${kind}_cursor_invalid`);
      if (cursor) cursors.add(cursor);
    } while (cursor);
    const ids = rows.map((row) => row.id);
    if (
      ids.some((id) => typeof id !== "string" || !id) ||
      new Set(ids).size !== ids.length
    )
      throw new Error(`existing_target_${kind}_identities_invalid`);
    return rows;
  }
  const stackPath = `/api/v1/stacks/${encodeURIComponent(stackId)}`;
  async function readFoundation() {
    const stack = await request(stackPath);
    expect(stack.id, "existing_target_stack_identity_mismatch").toBe(stackId);
    expect(stack.kit_deployment_id).toBe(stackId);
    // stack.mode is the UI mode (Found currently creates easy), not the native
    // install/dispatch mode. Advanced admission is checked on the real node below.
    if (!stack.stackkit_id || !stack.stackkit_instance_id)
      throw new Error("existing_target_foundation_identity_missing");
    const operations = await request(`${stackPath}/operations`);
    const servers = (await inventory("servers")).filter(
      (row) => row.stack_id === stackId,
    );
    if (!servers.length) throw new Error("existing_target_has_no_servers");
    const serverIds = servers.map((row) => row.id).sort();
    expect(
      (operations.servers ?? []).map((row: TargetRecord) => row.id).sort(),
      "existing_target_server_set_mismatch",
    ).toEqual(serverIds);
    if (options.serverId)
      expect(serverIds, "existing_target_selected_server_missing").toContain(
        options.serverId,
      );
    const identities = [];
    for (const server of servers.sort((a, b) => a.id.localeCompare(b.id))) {
      const aggregate = await request(
        `/api/v1/servers/${encodeURIComponent(server.id)}`,
      );
      expect(aggregate.id).toBe(server.id);
      expect(
        aggregate.kit_deployment_id,
        "existing_target_server_stack_binding_mismatch",
      ).toBe(stackId);
      expect(
        aggregate.connection?.state,
        "existing_target_guard_disconnected",
      ).toBe("connected");
      expect(
        aggregate.lifecycle?.state,
        "existing_target_server_inactive",
      ).toBe("active");
      expect(aggregate.health?.state, "existing_target_server_unhealthy").toBe(
        "healthy",
      );
      expect(
        aggregate.capabilities?.identity_binding,
        "existing_target_guard_identity_not_bound",
      ).toBe("key_bound");
      expect(
        aggregate.capabilities?.command_round_trip?.state,
        "existing_target_guard_round_trip_unverified",
      ).toBe("verified");
      expect(server.freshness?.state, "existing_target_inventory_stale").toBe(
        "fresh",
      );
      if (
        !server.stackkit?.name ||
        !server.stackkit?.version ||
        !aggregate.worker_id
      )
        throw new Error("existing_target_foundation_or_guard_identity_missing");
      // This backend action requires Ready convergence at the configured release
      // version AND archive hash, plus the current authenticated Advanced ABI.
      expect(
        aggregate.stack_actions,
        "existing_target_cli_artifact_or_advanced_capability_incompatible",
      ).toContain("advanced_change_set");
      identities.push({
        id: aggregate.id,
        node_id: aggregate.node_id,
        worker_id: aggregate.worker_id,
        stack_id: aggregate.kit_deployment_id,
        lease_id: aggregate.provider?.lease_id ?? "",
        provider_ref: aggregate.provider?.ref ?? "",
        stackkit: server.stackkit,
      });
    }
    return {
      stack: {
        id: stack.id,
        kit_deployment_id: stack.kit_deployment_id,
        stackkit_id: stack.stackkit_id,
        stackkit_instance_id: stack.stackkit_instance_id,
        catalog_ref: stack.catalog_ref,
        mode: stack.mode,
        lease_id: stack.lease_id,
      },
      servers: identities,
    };
  }
  async function services(serverIds: string[]) {
    const rows = [];
    for (const serverId of serverIds) {
      const page = await inventory("services", serverId);
      if (page.some((row) => row.server_id !== serverId))
        throw new Error("existing_target_service_server_scope_mismatch");
      rows.push(...page.filter((row) => row.stack_id === stackId));
    }
    return rows;
  }
  const serviceIdentity = (row: TargetRecord) => ({
    id: row.id,
    stack_id: row.stack_id,
    server_id: row.server_id,
    key: row.key,
  });
  const sourceBefore = await sourceBinding();
  const before = await readFoundation();
  const serverIds = before.servers.map((server) => server.id);
  const existing = await services(serverIds);
  if (!existing.length)
    throw new Error("existing_target_foundation_has_no_observed_services");
  const previouslyHealthy = existing.filter(
    (row) =>
      row.management_state === "managed" &&
      row.observed_state === "running" &&
      row.freshness?.state === "fresh",
  );
  if (!previouslyHealthy.length) {
    throw new Error(
      "existing_target_foundation_has_no_healthy_managed_services",
    );
  }
  if (existing.some((row) => row.key === "vaultwarden"))
    throw new Error(
      "existing_target_vault_already_installed_choose_unextended_target",
    );
  let primaryError: unknown;
  try {
    const selection = {
      flow: "add-use-case",
      stack_id: stackId,
      goals: ["vault"],
    };
    const recommendation = await request(
      "/api/v1/unifier/layout/recommendations",
      selection,
    );
    expect(recommendation.verification?.status).toBe("verified");
    const body = {
      intent: {
        schema: "techstack.wizard-intent/v1",
        run_kind: "expansion",
        name: "existing-target-extension",
        goals: selection.goals,
        server: {},
        kit_assignment: { mode: "extend", kit_deployment_id: stackId },
        layout_recommendation: {
          id: recommendation.id,
          proposal_digest: recommendation.proposal_digest,
        },
      },
    };
    const key = `existing-target-${randomUUID()}`;
    const run = await request("/api/v1/wizard/runs", body, key);
    expect(run.job_ids?.length).toBeGreaterThan(0);
    expect(run.state).toBe("provisioning");
    const replay = await request("/api/v1/wizard/runs", body, key);
    expect(replay.job_ids).toEqual(run.job_ids);
    expect(replay.run_id).toBe(run.run_id);
    for (const id of run.job_ids as string[]) {
      const job = await waitForJob(id);
      expect(job.state).toBe("completed");
      const result = job.result as TargetRecord;
      expect(result.operation).toBe("advanced_change_set");
      expect(result.success).toBe(true);
      expect(result.wizard_extend_receipt?.run_id).toBe(run.run_id);
      expect(result.wizard_extend_receipt?.stack_id).toBe(stackId);
      expect(result.wizard_extend_receipt?.group_job_ids).toBe(
        run.job_ids.join(","),
      );
      if (process.env.TECHSTACK_RUNTIME_E2E_PLAN_OWNER === "1") {
        const receipt = result.wizard_extend_receipt;
        expect(receipt.execution_binding_source).toBe("native-local-owner");
        expect(receipt.local_node_ref).toBe(receipt.node_ref);
        expect(receipt.local_site_ref).toBeTruthy();
        expect(receipt.execution_channel_ref).toBeTruthy();
        expect(receipt.execution_owner_ref).toBeTruthy();
        expect(receipt.execution_owner_key_id).toBeTruthy();
        expect(receipt.baseline_plan_hash).toMatch(/^sha256:[a-f0-9]{64}$/);
      }
    }
    await expect
      .poll(
        async () => {
          const current = await services(serverIds);
          return current.some(
            (row) =>
              row.key === "vaultwarden" &&
              row.management_state === "managed" &&
              row.observed_state === "running" &&
              row.freshness?.state === "fresh" &&
              !existing.some((old) => old.id === row.id),
          );
        },
        { timeout: 180_000, intervals: [5_000] },
      )
      .toBe(true);
    return {
      stack_id: stackId,
      server_ids: serverIds,
      run_id: run.run_id,
      job_ids: run.job_ids,
      source: sourceBefore,
      foundation: before,
      preserved_service_identities: existing.map(serviceIdentity),
      preserved_healthy_service_ids: previouslyHealthy.map((row) => row.id),
      runtime_artifact_admission: "backend_current_pin_advanced_change_set",
      foundation_retained: true,
    };
  } catch (error) {
    primaryError = error;
    throw error;
  } finally {
    try {
      expect(
        await readFoundation(),
        "existing_target_foundation_or_server_identity_changed",
      ).toEqual(before);
      // One shared, target-scoped inventory read per poll covers every baseline
      // service. Only rows that were healthy before Add must regain these states.
      await expect
        .poll(
          async () => {
            const current = await services(serverIds);
            const sameIdentity = (row: TargetRecord, service: TargetRecord) =>
              row.id === service.id &&
              row.stack_id === service.stack_id &&
              row.server_id === service.server_id &&
              row.key === service.key;
            return {
              missing_service_ids: existing
                .filter(
                  (service) =>
                    !current.some((row) => sameIdentity(row, service)),
                )
                .map((service) => service.id)
                .sort(),
              unhealthy_previous_service_ids: previouslyHealthy
                .filter(
                  (service) =>
                    !current.some(
                      (row) =>
                        sameIdentity(row, service) &&
                        row.management_state === "managed" &&
                        row.observed_state === "running" &&
                        row.freshness?.state === "fresh",
                    ),
                )
                .map((service) => service.id)
                .sort(),
            };
          },
          {
            message:
              "existing_target_prior_service_identity_or_health_not_restored",
            timeout: 180_000,
            intervals: [5_000],
          },
        )
        .toEqual({
          missing_service_ids: [],
          unhealthy_previous_service_ids: [],
        });
      expect(
        await sourceBinding(),
        "existing_target_source_changed_during_add",
      ).toEqual(sourceBefore);
    } catch (error) {
      if (primaryError)
        throw new AggregateError(
          [primaryError, error],
          "existing_target_add_failed_and_foundation_preservation_failed",
        );
      throw error;
    }
  }
}
