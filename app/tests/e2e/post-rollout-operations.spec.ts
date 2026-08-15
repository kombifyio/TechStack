import { expect, test, type Page, type Route } from "@playwright/test";

const stackId = "stack-ops";
const workerId = "worker-legacy";

function apiEnvelope(data: unknown) {
  return {
    data,
    meta: {
      request_id: "test",
      timestamp: "2026-05-18T09:00:00Z",
    },
  };
}

async function fulfillJson(route: Route, data: unknown, status = 200) {
  const origin =
    route.request().headers()["origin"] ??
    process.env.PLAYWRIGHT_BASE_URL ??
    "http://127.0.0.1:5261";
  await route.fulfill({
    status,
    headers: {
      "access-control-allow-headers": "*",
      "access-control-allow-methods": "GET,POST,OPTIONS",
      "access-control-allow-origin": origin,
      "access-control-allow-credentials": "true",
      "content-type": "application/json",
    },
    body: JSON.stringify(data),
  });
}

function stackRecord(options: { monthlyRuntime?: boolean } = {}) {
  const base = {
    id: stackId,
    name: "Ops Beta Stack",
    mode: "easy",
    status: "pending",
    state: "pending",
    provider: "local",
    services: ["pocket_id", "traefik", "monitoring"],
    created: "2026-05-18T09:00:00Z",
    updated: "2026-05-18T09:00:00Z",
    created_at: "2026-05-18T09:00:00Z",
    updated_at: "2026-05-18T09:00:00Z",
  };
  if (!options.monthlyRuntime) return base;
  return {
    ...base,
    status: "running",
    state: "running",
    server_mode: "monthly-runtime",
    runtime_lane: "monthly-runtime",
    runtime_offering_id: "centron-basic",
    lease_provider: "centron-managed",
    lease_id: "lease-stack-ops",
    server_ip: "203.0.113.10",
    desired_state: "running",
    runtime_phase: "running",
    verification_status: "enrolled",
  };
}

function serverRecord(
  assigned: boolean,
  options: { managedRuntimeProjection?: boolean; connected?: boolean } = {},
) {
  if (options.managedRuntimeProjection) {
    return {
      id: "lease:lease-stack-ops",
      hostname: "ops-managed-runtime",
      role: "foundation",
      status: "offline",
      assignment: "stack",
      stack_id: stackId,
      agent_id: "lease:lease-stack-ops",
      ip: "203.0.113.10",
      os: "linux",
      arch: "amd64",
      last_seen: "2026-05-18T09:00:00Z",
      approved: false,
      approved_at: "2026-05-18T09:00:00Z",
      precheck_state: "managed",
      source: "managed-runtime",
      lease_id: "lease-stack-ops",
      runtime_lane: "monthly-runtime",
      runtime_offering_id: "centron-basic",
      desired_state: "stopped",
      enrollment_status: "enrolled",
      assignable: false,
      capabilities: {
        cpu_cores: 2,
        ram_mb: 4096,
        disk_gb: 80,
        desired_state: "stopped",
        enrollment_status: "enrolled",
        image: "ubuntu-24.04",
        lease_id: "lease-stack-ops",
        provider: "centron-managed",
        region: "de-fra",
        runtime_lane: "monthly-runtime",
        runtime_offering_id: "centron-basic",
        runtime_ssh_host: "203.0.113.10",
      },
      health: {
        state: "offline",
        source: "managed-runtime",
        cpu_percent: { status: "unknown", unit: "%" },
        memory_percent: { status: "unknown", unit: "%" },
        disk_percent: { status: "unknown", unit: "%" },
        uptime_seconds: { status: "unknown", unit: "s" },
        updated_at: "2026-05-18T09:00:00Z",
        notes: ["managed monthly runtime projection"],
      },
    };
  }

  const connected = options.connected ?? assigned;

  return {
    id: workerId,
    hostname: "ops-node-1",
    role: "worker",
    status: connected ? "connected" : "approved",
    assignment: assigned ? "stack" : "unassigned",
    stack_id: assigned ? stackId : "",
    agent_id: "agent-ops-1",
    ip: "10.0.0.10",
    host_addresses: [
      {
        address: "10.0.0.10",
        scope: "private",
        provenance: "guard inventory",
      },
    ],
    os: "Ubuntu",
    os_version: "24.04.2 LTS",
    arch: "amd64",
    domains: ["base.home.localhost", "auth.home.localhost"],
    service_endpoints: [
      {
        service_key: "base",
        name: "Base",
        url: "http://base.home.localhost",
        domain: "base.home.localhost",
        visibility: "private",
        health: connected ? "healthy" : "unknown",
        provenance: "StackKit access manifest",
        source: "guard inventory",
        observed_at: connected ? new Date().toISOString() : "",
      },
    ],
    stackkit: {
      name: "Basement Kit",
      catalog_ref: "basement-kit",
      version: "1.4.0",
      mode: "standard",
      context: "homelab",
      paas: "coolify",
      compute_tier: "local",
      state: "observed",
      sources: ["guard inventory", "StackKit access manifest"],
    },
    last_seen: connected ? new Date().toISOString() : "",
    approved: true,
    approved_at: "2026-05-18T08:59:00Z",
    precheck_state: "passed",
    capabilities: {
      cpu_cores: 8,
      ram_mb: 8192,
      disk_gb: 256,
      docker_version: "26.1.0",
      provider: "local",
    },
    health: {
      state: connected ? "healthy" : "unknown",
      source: connected ? "canonical-server" : "unverified",
      cpu_percent: connected
        ? { status: "ok", value: 12, unit: "%" }
        : { status: "unknown", unit: "%" },
      memory_percent: connected
        ? { status: "ok", value: 41, unit: "%" }
        : { status: "unknown", unit: "%" },
      disk_percent: connected
        ? { status: "ok", value: 28, unit: "%" }
        : { status: "unknown", unit: "%" },
      uptime_seconds: connected
        ? { status: "ok", value: 7200, unit: "s" }
        : { status: "unknown", unit: "s" },
      updated_at: connected ? new Date().toISOString() : "",
    },
  };
}

function operationsPayload(
  assigned: boolean,
  includeServices = true,
  options: {
    monthlyRuntime?: boolean;
    managedRuntimeProjection?: boolean;
    connected?: boolean;
  } = {},
) {
  const connected = options.connected ?? assigned;
  const server = serverRecord(assigned, {
    managedRuntimeProjection: options.managedRuntimeProjection,
    connected,
  });
  const services = includeServices
    ? [
        {
          name: "pocket_id",
          display_name: "Pocket ID",
          type: "identity",
          status: connected ? "running" : "unknown",
          target_server: assigned ? "ops-node-1" : "",
          target_server_id: assigned ? workerId : "",
        },
      ]
    : [];
  return {
    stack: stackRecord({ monthlyRuntime: options.monthlyRuntime }),
    readiness: {
      status: connected
        ? "ready"
        : assigned
          ? "waiting_for_connection"
          : "waiting_for_assignment",
      can_start: connected,
      required_servers: 1,
      approved_servers: options.managedRuntimeProjection ? 0 : 1,
      connected_servers: connected ? 1 : 0,
      pending_servers: 0,
      assigned_servers: assigned ? 1 : 0,
      available_servers: assigned ? 0 : 1,
      unassigned_servers: assigned ? 0 : 1,
      message: connected
        ? "Review the configuration and start the rollout when ready."
        : assigned
          ? "Waiting for a fresh canonical Guard heartbeat."
          : "Assign available approved servers to this stack before rollout.",
      review_required: true,
    },
    nextSteps: [
      {
        id: "review_config",
        label: "Config pruefen",
        description:
          "StackKit Standard, Services und Server-Zuordnung kontrollieren.",
        status: "completed",
      },
      {
        id: "start_rollout",
        label: "Rollout starten",
        description: "Startet die Orchestration explizit ueber Review + Start.",
        status: connected ? "current" : "pending",
        action: "review_start",
      },
    ],
    kpis: {
      registered_servers: 1,
      healthy_servers: connected ? 1 : 0,
      running_services: includeServices && connected ? 1 : 0,
      active_alerts: 0,
    },
    servers: [server],
    services,
    monitoring: {
      status: "ok",
      queryBackend: "embedded-tsdb",
      ingestBackend: "embedded-tsdb",
      collectorMode: "otel",
      compatibilityMode: "dual-ingest",
      seriesCount: 42,
      unscopedAlerts: 1,
      message: "metrics backend reachable",
    },
    alerts: [],
  };
}

function listResponse(
  items: unknown[],
  page: number,
  perPage: number,
  totalItems = items.length,
) {
  return {
    page,
    perPage,
    totalItems,
    totalPages: totalItems > 0 ? Math.ceil(totalItems / perPage) : 0,
    items,
  };
}

async function mockPostRolloutApi(
  page: Page,
  options: {
    includeServices?: boolean;
    monthlyRuntime?: boolean;
    managedRuntimeProjection?: boolean;
    pocketBaseStackMissing?: boolean;
    operationsNotFound?: boolean;
  } = {},
): Promise<{ reportGuardConnected: () => void }> {
  let assigned = false;
  let guardConnected = false;
  const includeServices = options.includeServices ?? true;
  const monthlyRuntime = options.monthlyRuntime ?? false;
  const managedRuntimeProjection = options.managedRuntimeProjection ?? false;

  await page.addInitScript(() => {
    const payload = btoa(
      JSON.stringify({
        id: "owner-1",
        exp: Math.floor(Date.now() / 1000) + 3600,
      }),
    )
      .replace(/\+/g, "-")
      .replace(/\//g, "_")
      .replace(/=+$/g, "");
    window.localStorage.setItem(
      "pocketbase_auth",
      JSON.stringify({
        token: `test.${payload}.signature`,
        model: {
          id: "owner-1",
          email: "owner@example.com",
          collectionName: "users",
        },
      }),
    );
  });

  await page.route("**/*", async (route) => {
    const url = new URL(route.request().url());
    if (
      route.request().method() === "OPTIONS" &&
      (url.origin === "http://127.0.0.1:5260" ||
        url.origin === "http://127.0.0.1:5276")
    ) {
      const origin =
        route.request().headers()["origin"] ??
        process.env.PLAYWRIGHT_BASE_URL ??
        "http://127.0.0.1:5261";
      await route.fulfill({
        status: 204,
        headers: {
          "access-control-allow-headers": "*",
          "access-control-allow-methods": "GET,POST,OPTIONS",
          "access-control-allow-origin": origin,
          "access-control-allow-credentials": "true",
        },
      });
      return;
    }
    await route.fallback();
  });

  await page.route("**/api/v1/auth/mode", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        mode: "local",
        deployment_mode: "self-hosted",
        is_first_run: false,
        allow_local_login: true,
      }),
    );
  });

  await page.route("**/api/v2/whoami", async (route) => {
    await fulfillJson(route, {
      user: {
        id: "owner-1",
        email: "owner@example.com",
        name: "Owner",
      },
      source: "local",
    });
  });

  await page.route("**/api/v1/features", async (route) => {
    await fulfillJson(route, apiEnvelope({ security: [], beta: [], ux: [] }));
  });

  await page.route("**/api/v1/tunnel/registry-url", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({ url: "http://localhost:5260", mode: "local" }),
    );
  });

  await page.route("**/api/v1/stacks", async (route) => {
    if (route.request().method() !== "GET") {
      await route.fallback();
      return;
    }
    await fulfillJson(route, apiEnvelope([stackRecord({ monthlyRuntime })]));
  });

  await page.route(`**/api/v1/stacks/${stackId}/operations`, async (route) => {
    if (options.operationsNotFound) {
      await fulfillJson(
        route,
        { code: 404, message: "Stack not found", data: {} },
        404,
      );
      return;
    }
    await fulfillJson(
      route,
      apiEnvelope(
        operationsPayload(assigned, includeServices, {
          monthlyRuntime,
          managedRuntimeProjection,
          connected: guardConnected,
        }),
      ),
    );
  });

  await page.route(
    `**/api/v1/stacks/${stackId}/servers/${workerId}`,
    async (route) => {
      await fulfillJson(
        route,
        apiEnvelope({
          stack: stackRecord({ monthlyRuntime }),
          server: serverRecord(assigned, {
            managedRuntimeProjection,
            connected: guardConnected,
          }),
          services: guardConnected
            ? operationsPayload(true, includeServices, {
                monthlyRuntime,
                connected: true,
              }).services
            : [],
          checks: [],
          logs: [],
          health: serverRecord(assigned, { connected: guardConnected }).health,
          monitoring: operationsPayload(assigned, includeServices, {
            monthlyRuntime,
            connected: guardConnected,
          }).monitoring,
        }),
      );
    },
  );

  await page.route(
    `**/api/v1/stacks/${stackId}/workers/${workerId}/assign`,
    async (route) => {
      assigned = true;
      await fulfillJson(
        route,
        apiEnvelope({
          stack_id: stackId,
          worker_id: workerId,
          server: serverRecord(true, { connected: false }),
        }),
      );
    },
  );

  await page.route(`**/api/v1/stacks/${stackId}/deploy`, async (route) => {
    await fulfillJson(
      route,
      apiEnvelope({
        success: true,
        message: "Deployment started",
        job_id: "job-rollout",
      }),
      202,
    );
  });

  await page.route("**/api/v1/monthly-runtimes/offerings", async (route) => {
    await fulfillJson(
      route,
      apiEnvelope([
        {
          id: "centron-basic",
          name: "Centron Basic",
          billing_cadence: "monthly",
          vcpus: 2,
          memory_mb: 4096,
          disk_gb: 80,
          region: "de",
        },
      ]),
    );
  });

  await page.route(
    "**/api/v1/monthly-runtimes/lease-stack-ops",
    async (route) => {
      await fulfillJson(
        route,
        apiEnvelope({
          lease_id: "lease-stack-ops",
          runtime_offering_id: "centron-basic",
          desired_state: "running",
          observed_state: "running",
          lease_state: "running",
          enrollment_status: "enrolled",
          ssh_enabled: true,
          status: {
            id: "lease-stack-ops",
            state: "running",
            public_ip: "203.0.113.10",
            ssh_enabled: true,
          },
          ssh: {
            host: "203.0.113.10",
            port: 22,
            user: "root",
            enabled: true,
          },
        }),
      );
    },
  );

  await page.route(
    "**/api/v1/monthly-runtimes/lease-stack-ops/operations**",
    async (route) => {
      await fulfillJson(
        route,
        apiEnvelope([
          {
            tenant_id: "owner-1",
            lease_id: "lease-stack-ops",
            event_type: "enrollment",
            status: "completed",
            actor: "runtime-e2e",
            created_at: "2026-05-18T09:05:00Z",
          },
        ]),
      );
    },
  );

  await page.route("**/api/v1/workers", async (route) => {
    await fulfillJson(route, apiEnvelope([]));
  });

  await page.route("**/api/collections/**", async (route) => {
    const url = new URL(route.request().url());
    const collection = url.pathname.match(
      /\/api\/collections\/([^/]+)\/records/,
    )?.[1];
    if (
      options.pocketBaseStackMissing &&
      collection === "stacks" &&
      url.pathname.endsWith(`/records/${stackId}`)
    ) {
      await fulfillJson(
        route,
        { code: 404, message: "The requested record was not found.", data: {} },
        404,
      );
      return;
    }
    const pageNumber = Number(url.searchParams.get("page") || "1");
    const perPage = Number(url.searchParams.get("perPage") || "30");
    const itemsByCollection: Record<string, unknown[]> = {
      stacks: [stackRecord({ monthlyRuntime })],
      nodes: [],
      services: [],
      wallet: [],
      jobs: [
        {
          id: "job-provision",
          stack_id: stackId,
          type: "provision",
          state: "completed",
          result: { registration_token: "pair-token" },
          created: "2026-05-18T09:00:00Z",
        },
      ],
      activity_log: [],
    };
    const allItems = itemsByCollection[collection ?? ""] ?? [];
    const items = pageNumber === 1 ? allItems : [];
    await fulfillJson(
      route,
      listResponse(items, pageNumber, perPage, allItems.length),
    );
  });

  return {
    reportGuardConnected: () => {
      guardConnected = true;
    },
  };
}

test.describe("Post-rollout operations review", () => {
  test("never swaps the legacy worker card in before the operations dashboard", async ({
    page,
  }) => {
    await mockPostRolloutApi(page);

    await page.addInitScript(() => {
      const observedWindow = window as Window & {
        __dashboardSurfaceHistory?: string[];
      };
      observedWindow.__dashboardSurfaceHistory = [];
      const recordVisibleSurfaces = () => {
        for (const testId of [
          "worker-management-card",
          "stack-operations-dashboard",
        ]) {
          if (
            document.querySelector(`[data-testid="${testId}"]`) &&
            !observedWindow.__dashboardSurfaceHistory?.includes(testId)
          ) {
            observedWindow.__dashboardSurfaceHistory?.push(testId);
          }
        }
      };
      new MutationObserver(recordVisibleSurfaces).observe(
        document.documentElement,
        {
          childList: true,
          subtree: true,
        },
      );
    });

    await page.route(
      `**/api/v1/stacks/${stackId}/operations`,
      async (route) => {
        await new Promise((resolve) => setTimeout(resolve, 350));
        await fulfillJson(route, apiEnvelope(operationsPayload(true)));
      },
    );

    await page.goto(`/stacks?stack=${stackId}`);
    await expect(page.getByTestId("stack-operations-dashboard")).toBeVisible();

    const surfaceHistory = await page.evaluate(
      () =>
        (window as Window & { __dashboardSurfaceHistory?: string[] })
          .__dashboardSurfaceHistory ?? [],
    );
    expect(surfaceHistory).toEqual(["stack-operations-dashboard"]);
    await expect(page.getByTestId("stackkit-lifecycle-button")).toHaveCount(0);
  });

  test("makes custody-only leases and a failed destroy actionable", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { includeServices: false });

    await page.route(
      `**/api/v1/stacks/${stackId}/operations`,
      async (route) => {
        await fulfillJson(
          route,
          apiEnvelope({
            ...operationsPayload(false, false),
            servers: [],
            custodyLeases: [
              {
                lease_id: "lease-centron-enrollment-failed",
                label: "homelab-foundation-centron",
                provider: "centron-managed",
                reason: "enrollment_failed",
                allowed_actions: ["decommission"],
              },
              {
                lease_id: "lease-ionos-legacy",
                label: "homelab-foundation-ionos",
                provider: "ionos-managed",
                reason: "no_execution_authority",
                allowed_actions: ["resolve_custody"],
                last_known_ip: "85.215.38.99",
              },
            ],
            latestFailure: {
              job_id: "job-destroy-failed",
              type: "destroy",
              state: "failed",
              step: "decommission_managed_runtime",
              message: "Decommissioning managed runtime leases...",
              error: "managed runtime cleanup failed",
              diagnostics_available: false,
            },
          }),
        );
      },
    );

    let decommissionRequests = 0;
    await page.route(
      "**/api/v1/monthly-runtimes/lease-centron-enrollment-failed/decommission",
      async (route) => {
        decommissionRequests += 1;
        await fulfillJson(
          route,
          apiEnvelope({
            lease_id: "lease-centron-enrollment-failed",
            lease_state: "pending",
          }),
          202,
        );
      },
    );
    let resolutionRequests = 0;
    await page.route(
      "**/api/v1/monthly-runtimes/lease-ionos-legacy/resolve-custody",
      async (route) => {
        resolutionRequests += 1;
        expect(route.request().postDataJSON()).toEqual({
          provider_cleanup_confirmed: true,
        });
        await fulfillJson(
          route,
          apiEnvelope({
            lease_id: "lease-ionos-legacy",
            lease_state: "resolved",
          }),
        );
      },
    );

    await page.goto(`/stacks?stack_id=${stackId}`);

    await expect(page.getByTestId("custody-leases")).toContainText(
      "2 leases without a server",
    );
    await expect(page.getByTestId("decommission-custody-lease")).toBeVisible();
    await expect(page.getByTestId("resolve-custody-lease")).toBeVisible();
    await expect(page.getByTestId("retry-destroy-cleanup")).toBeVisible();

    await page.getByTestId("retry-destroy-cleanup").click();
    await expect.poll(() => decommissionRequests).toBe(1);

    page.once("dialog", (dialog) => dialog.accept());
    await page.getByTestId("resolve-custody-lease").click();
    await expect.poll(() => resolutionRequests).toBe(1);
  });

  test("restarts a failed pre-lease Wizard provision instead of only refreshing operations", async ({
    page,
  }) => {
    await mockPostRolloutApi(page);

    await page.route(
      `**/api/v1/stacks/${stackId}/operations`,
      async (route) => {
        await fulfillJson(
          route,
          apiEnvelope({
            ...operationsPayload(false),
            latestFailure: {
              job_id: "job-failed-allocation",
              type: "provision",
              state: "failed",
              step: "server_allocate",
              message: "Managed server allocation failed",
              error: "provider mutation was unavailable",
              reason: "provider_mutation_unavailable",
              diagnostics_available: false,
            },
          }),
        );
      },
    );

    let provisionRequests = 0;
    await page.route(`**/api/v1/stacks/${stackId}/provision`, async (route) => {
      provisionRequests += 1;
      await fulfillJson(
        route,
        apiEnvelope({
          success: true,
          message: "Provisioning started",
          job_id: "job-provision-retry",
        }),
        202,
      );
    });

    await page.goto(`/stacks?stack_id=${stackId}&creation=1`);
    // Failure details moved below the server list into a collapsed panel;
    // expand it before the retry action becomes visible.
    await page
      .getByTestId("latest-failure-collapsible")
      .getByRole("button", { name: /Latest provision failed/ })
      .click();
    await page.getByRole("button", { name: "Rollout erneut starten" }).click();

    await expect.poll(() => provisionRequests).toBe(1);
    await expect(page).toHaveURL(/job_id=job-provision-retry/);
  });

  test("keeps canonical services visible when the legacy registry fails", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { includeServices: false });
    const serviceId = "inventory-service-exact";
    let inventoryRequests = 0;
    await page.route("**/api/v1/registry/services", async (route) => {
      await fulfillJson(
        route,
        {
          error: {
            code: "legacy_registry_unavailable",
            message: "legacy down",
          },
        },
        503,
      );
    });
    await page.route("**/api/v1/inventory/services", async (route) => {
      inventoryRequests += 1;
      await fulfillJson(
        route,
        apiEnvelope({
          observed_at: "2026-07-20T00:00:00Z",
          freshness: { state: "fresh", stale_after_seconds: 90 },
          inventory_revision: 7,
          services: [
            {
              id: serviceId,
              stack_id: stackId,
              server_id: "server-inventory-1",
              key: "coolify",
              name: "Coolify",
              // Required by the canonical inventory contract; the client
              // rejects a service page without it instead of silently
              // rendering every service as unmanaged.
              management_state: "managed",
              observed_state: "running",
              observed_at: "2026-07-20T00:00:00Z",
              freshness: { state: "fresh", stale_after_seconds: 90 },
              inventory_revision: 7,
              health: { state: "healthy" },
              stackkit: {
                name: "cloud-kit",
                version: "0.6.2",
                variant: "managed",
              },
              links: [{ url: "https://coolify.example.test", mode: "https" }],
              source: "runtime",
            },
          ],
        }),
      );
    });

    await page.goto("/services");

    const card = page.getByTestId("runtime-service-card");
    await expect(card).toHaveCount(1);
    await expect(card).toHaveAttribute("data-service-id", serviceId);
    await expect(card.getByText("Coolify", { exact: true })).toBeVisible();
    await expect(page.getByTestId("registry-services-warning")).toBeVisible();
    await expect(page.getByTestId("new-service-button")).toBeDisabled();
    await expect(page.getByTestId("runtime-service-list")).toBeVisible();
    await expect(
      page.getByTestId("inventory-services-error-panel"),
    ).toHaveCount(0);
    expect(inventoryRequests).toBeGreaterThan(0);
    const inventoryRequestsBeforeRetry = inventoryRequests;

    const registryRetryResponse = page.waitForResponse((response) =>
      response.url().includes("/api/v1/registry/services"),
    );
    await page.getByRole("button", { name: "Retry management data" }).click();
    await registryRetryResponse;
    await expect(page.getByTestId("registry-services-warning")).toBeVisible();
    await expect(card).toHaveCount(1);
    expect(inventoryRequests).toBe(inventoryRequestsBeforeRetry);

    await page.getByTestId("server-management-tab").click();
    await expect(page.getByTestId("registry-placement-warning")).toBeVisible();
    await expect(page.getByTestId("migration-unavailable")).toHaveCount(0);
  });

  test("shows an empty state when backend reports no runtime services", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { includeServices: false });

    await page.goto(`/stacks?stack=${stackId}&phase=review`);
    await expect(page.getByTestId("stack-operations-dashboard")).toBeVisible();
    await expect(page.getByTestId("dashboard-services-summary")).toBeVisible();
    await expect(
      page.getByText("No runtime services reported", { exact: true }),
    ).toBeVisible();
    await expect(page.getByText("Pocket ID")).toHaveCount(0);
  });

  test("settings cleanup prunes orphans and never calls the destructive reset", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { monthlyRuntime: true });

    let pruneCalled = false;
    let resetCalled = false;
    await page.route("**/api/v1/stacks/prune-orphans", async (route) => {
      pruneCalled = true;
      await fulfillJson(
        route,
        apiEnvelope({
          message: "Orphan stacks pruned",
          pruned_stacks: 1,
          skipped_active: 0,
        }),
      );
    });
    // Regression guard: the generic "clean up" button must NEVER hit the
    // destructive, lease-decommissioning reset endpoint. If it does, fail loud.
    await page.route("**/api/v1/stacks/reset", async (route) => {
      resetCalled = true;
      await fulfillJson(
        route,
        apiEnvelope({ message: "unexpected reset" }),
        500,
      );
    });

    // Destructive maintenance lives in Settings > Danger Zone, never on the
    // dashboard the operator sees on every visit.
    await page.goto("/settings");
    await expect(page.getByTestId("settings-prune-orphans-card")).toBeVisible();

    const button = page.getByTestId("settings-prune-orphans-button");
    await expect(button).toBeVisible();
    await button.click();

    const modal = page.getByTestId("settings-prune-orphans-modal");
    await expect(modal).toBeVisible();
    // Safe-prune wording, not a destructive-reset dialog.
    await expect(modal).toContainText("Clean up orphaned");
    await expect(modal).toContainText("remain unchanged");

    await page.getByTestId("settings-prune-orphans-confirm").click();

    await expect.poll(() => pruneCalled).toBe(true);
    expect(resetCalled).toBe(false);
  });

  test("delete action prunes the exact dead legacy entry without a destroy job", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { monthlyRuntime: true });

    let pruneCalled = false;
    let destroyCalled = false;
    let reloadedAfterPrune = false;
    // Later registrations take precedence: serve a legacy-labeled row until
    // targeted prune is called, then an empty list (the dead card is gone).
    await page.route("**/api/v1/stacks", async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }
      if (pruneCalled) {
        reloadedAfterPrune = true;
        await fulfillJson(route, apiEnvelope([]));
        return;
      }
      await fulfillJson(
        route,
        apiEnvelope([
          { ...stackRecord({ monthlyRuntime: true }), legacy: true },
        ]),
      );
    });
    await page.route(`**/api/v1/stacks/${stackId}/destroy`, async (route) => {
      destroyCalled = true;
      await fulfillJson(
        route,
        apiEnvelope({ message: "unexpected destroy" }),
        500,
      );
    });
    await page.route(
      `**/api/v1/stacks/prune-orphans?stack_id=${stackId}`,
      async (route) => {
        pruneCalled = true;
        await fulfillJson(
          route,
          apiEnvelope({
            message: "Orphan stacks pruned",
            pruned_stacks: 0,
            pruned_legacy: 1,
            skipped_active: 0,
            skipped_other_owner: 0,
          }),
        );
      },
    );

    await page.goto("/settings");

    const deleteButton = page
      .getByTestId("settings-delete-deployment-button")
      .first();
    await expect(deleteButton).toBeEnabled();
    await deleteButton.click();

    const modal = page.getByTestId("settings-delete-deployment-modal");
    await expect(modal).toBeVisible();
    // Deletion requires typing the exact deployment name.
    const confirmButton = page.getByTestId(
      "settings-delete-deployment-confirm",
    );
    await expect(confirmButton).toBeDisabled();
    await page
      .getByTestId("settings-delete-deployment-input")
      .fill(stackRecord({ monthlyRuntime: true }).name);
    await expect(confirmButton).toBeEnabled();
    await confirmButton.click();

    // A legacy row carries no runtime: the targeted orphan prune retires it,
    // the decommissioning destroy endpoint must never be called.
    await expect.poll(() => pruneCalled).toBe(true);
    expect(destroyCalled).toBe(false);
    await expect.poll(() => reloadedAfterPrune).toBe(true);
    await expect(modal).toHaveCount(0);
  });

  test("demo anchor stack is labeled and cannot be destroyed", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { monthlyRuntime: true });

    await page.route("**/api/v1/stacks", async (route) => {
      if (route.request().method() !== "GET") {
        await route.fallback();
        return;
      }
      await fulfillJson(
        route,
        apiEnvelope([
          {
            ...stackRecord({ monthlyRuntime: true }),
            name: "kombify-demo",
            demo_anchor: true,
          },
        ]),
      );
    });

    await page.goto(`/stacks?stack=${stackId}`);
    await expect(page.getByTestId("stack-demo-badge")).toBeVisible();

    // The protected demo deployment cannot be deleted from Settings either.
    await page.goto("/settings");
    await expect(
      page.getByTestId("settings-delete-deployment-button").first(),
    ).toBeDisabled();
  });

  test("shows managed-runtime projected server even when it is not assignable", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, {
      includeServices: false,
      monthlyRuntime: true,
      managedRuntimeProjection: true,
    });

    await page.goto(`/stacks?stack=${stackId}&phase=review`);
    await expect(page.getByTestId("stack-operations-dashboard")).toBeVisible();
    await expect(page.getByText("Create your TechStack")).toHaveCount(0);
    await expect(page.getByTestId("server-card").first()).toBeVisible();
    await expect(page.getByTestId("server-card")).toContainText(
      "ops-managed-runtime",
    );
    await expect(page.getByTestId("server-card")).toContainText(
      "managed runtime",
    );
    await expect(page.getByTestId("managed-runtime-action-row")).toBeVisible();
    await expect(page.getByTestId("deploy-stackkit-button")).toHaveText(
      "Deploy StackKit",
    );
    await expect(page.getByTestId("deploy-stackkit-button")).toBeDisabled();
    await expect(page.getByTestId("open-server-details-link")).toBeVisible();
    await expect(page.getByTestId("decommission-server-button")).toHaveCount(0);
  });

  test("shows an assigned canonical server even when it is not assignable", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { includeServices: false });

    const canonicalServer = {
      ...serverRecord(true),
      id: "server-canonical",
      hostname: "cloudkit-ionos-1",
      ip: "82.165.66.160",
      os: "Ubuntu",
      os_version: "24.04",
      arch: "arm64",
      domains: ["base.kombified.com", "auth.kombified.com"],
      service_endpoints: [
        {
          service_key: "base",
          name: "Base",
          url: "https://base.kombified.com",
          domain: "base.kombified.com",
          visibility: "public",
          health: "healthy",
          provenance: "guard inventory",
          source: "guard inventory",
        },
      ],
      stackkit: {
        name: "Cloud Kit",
        catalog_ref: "cloud-kit",
        version: "2.1.0",
        mode: "standard",
        context: "cloud",
        paas: "coolify",
        compute_tier: "managed-vps",
        state: "observed",
        sources: ["guard inventory"],
      },
      source: "canonical-server",
      assignable: false,
    };
    await page.route(
      `**/api/v1/stacks/${stackId}/operations`,
      async (route) => {
        await fulfillJson(
          route,
          apiEnvelope({
            ...operationsPayload(true, false),
            servers: [
              canonicalServer,
              {
                ...canonicalServer,
                id: "server-intent-only",
                hostname: "intent-only",
                approved: false,
              },
              {
                ...canonicalServer,
                id: "server-decommissioned",
                hostname: "decommissioned",
                approved: false,
                capabilities: {
                  ...canonicalServer.capabilities,
                  lifecycle_state: "decommissioned",
                },
              },
            ],
          }),
        );
      },
    );

    await page.goto(`/stacks?stack=${stackId}&phase=review`);

    const serverCard = page.getByTestId("server-card");
    await expect(serverCard).toHaveCount(1);
    await expect(serverCard).toContainText("cloudkit-ionos-1");
    await expect(serverCard).toContainText("Ubuntu 24.04/arm64");
    await expect(serverCard).toContainText("82.165.66.160");
    await expect(serverCard).toContainText("base.kombified.com");
    await expect(serverCard).toContainText("Cloud Kit");
    await expect(serverCard).toContainText("2.1.0 · standard · cloud");
    await expect(serverCard).toContainText("observed");
    // The shared ServerCard renders the capitalized badge label ("Healthy").
    await expect(serverCard).toContainText(/healthy/i);
    await expect(page.getByTestId("assign-server-button")).toHaveCount(0);
    await expect(page.getByText("intent-only", { exact: true })).toHaveCount(0);
    await expect(page.getByText("decommissioned", { exact: true })).toHaveCount(
      0,
    );
  });

  test("retains managed-runtime server card when operations data is temporarily missing", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, {
      monthlyRuntime: true,
      operationsNotFound: true,
    });

    await page.goto(`/stacks?stack=${stackId}&phase=review`);
    await expect(
      page.getByText("Alter Stack-Eintrag wurde entfernt"),
    ).toHaveCount(0);
    await expect(page.getByTestId("partial-rollout-dashboard")).toBeVisible();
    await expect(page.getByTestId("partial-rollout-dashboard")).toContainText(
      "lease/allocation metadata exists",
    );
    await expect(
      page.getByTestId("partial-rollout-dashboard"),
    ).not.toContainText("Managed VPS wurde bereitgestellt");
    await expect(page.getByTestId("server-card")).toContainText(
      "managed runtime",
    );
    await expect(page.getByTestId("server-card")).toContainText(
      "lease-stack-ops",
    );
    await expect(page.getByTestId("server-card")).toContainText(
      "centron-managed",
    );
    await expect(page.getByTestId("server-card")).toContainText("203.0.113.10");
    await expect(page.getByTestId("managed-runtime-action-row")).toBeVisible();
    await expect(page.getByTestId("deploy-stackkit-button")).toHaveText(
      "Deploy StackKit",
    );
    await expect(page.getByTestId("deploy-stackkit-button")).toBeDisabled();
    await expect(page.getByTestId("worker-connected-count")).toContainText(
      "0 verified connected",
    );
    await expect(page.getByTestId("open-server-details-link")).toBeVisible();
    await expect(page.getByTestId("decommission-server-button")).toHaveCount(0);
  });

  test("keeps the last operations snapshot visible but disables mutations when refresh evidence is stale", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { monthlyRuntime: true });
    let failOperations = false;
    await page.route(
      `**/api/v1/stacks/${stackId}/operations`,
      async (route) => {
        if (!failOperations) {
          const payload = operationsPayload(true, true, {
            monthlyRuntime: true,
            connected: true,
          });
          payload.servers.push(
            serverRecord(true, {
              managedRuntimeProjection: true,
              connected: false,
            }),
          );
          payload.kpis.registered_servers = 2;
          await fulfillJson(route, apiEnvelope(payload));
          return;
        }
        await fulfillJson(
          route,
          {
            error: {
              code: "operations_unavailable",
              message: "canonical evidence temporarily unavailable",
            },
          },
          503,
        );
      },
    );

    await page.goto(`/stacks?stack=${stackId}&phase=review`);
    const dashboard = page.getByTestId("stack-operations-dashboard");
    await expect(dashboard).toBeVisible();
    await expect(page.getByTestId("deploy-stackkit-button")).toBeEnabled();
    await expect(page.getByTestId("reconnect-server-button")).toBeEnabled();
    await expect(page.getByTestId("decommission-server-button")).toHaveCount(0);

    failOperations = true;
    // Refresh lives in the single top action bar, not inside the operations
    // section.
    await page.getByTestId("dashboard-refresh-button").click();

    await expect(page.getByTestId("operations-evidence-stale")).toContainText(
      "canonical evidence temporarily unavailable",
    );
    await expect(page.getByTestId("server-card").first()).toBeVisible();
    await expect(page.getByTestId("deploy-stackkit-button")).toBeDisabled();
    await expect(page.getByTestId("reconnect-server-button")).toBeDisabled();
    await expect(page.getByTestId("decommission-server-button")).toHaveCount(0);
  });

  test("revalidates operations when the legacy worker list poll fails", async ({
    page,
  }) => {
    await page.clock.install({ time: new Date("2026-07-22T08:00:00Z") });
    const runtime = await mockPostRolloutApi(page);
    runtime.reportGuardConnected();
    let workersUnavailable = false;
    let operationsUnavailable = false;

    await page.unroute("**/api/v1/workers");
    await page.route("**/api/v1/workers", async (route) => {
      if (workersUnavailable) {
        await fulfillJson(
          route,
          {
            error: {
              code: "workers_unavailable",
              message: "legacy worker list unavailable",
            },
          },
          503,
        );
        return;
      }
      await fulfillJson(route, apiEnvelope([]));
    });
    await page.route(
      `**/api/v1/stacks/${stackId}/operations`,
      async (route) => {
        if (operationsUnavailable) {
          await fulfillJson(
            route,
            {
              error: {
                code: "operations_unavailable",
                message: "canonical evidence temporarily unavailable",
              },
            },
            503,
          );
          return;
        }
        await fulfillJson(
          route,
          apiEnvelope(
            operationsPayload(true, true, {
              connected: true,
            }),
          ),
        );
      },
    );

    await page.goto(`/stacks?stack=${stackId}&phase=review`);
    await expect(page.getByTestId("review-start-button")).toBeEnabled();

    workersUnavailable = true;
    operationsUnavailable = true;
    await page.clock.fastForward(15_001);

    await expect(page.getByTestId("operations-evidence-stale")).toContainText(
      "canonical evidence temporarily unavailable",
    );
    await expect(page.getByTestId("review-start-button")).toBeDisabled();
  });

  test("marks a retained monitoring connection snapshot stale after refresh failure", async ({
    page,
  }) => {
    await mockPostRolloutApi(page);
    let cockpitUnavailable = false;
    await page.route("**/api/v1/monitor/cockpit**", async (route) => {
      if (cockpitUnavailable) {
        await fulfillJson(
          route,
          {
            error: {
              code: "monitoring_unavailable",
              message: "current cockpit evidence unavailable",
            },
          },
          503,
        );
        return;
      }
      const snapshot = operationsPayload(true, true, { connected: true });
      await fulfillJson(
        route,
        apiEnvelope({
          stacks: [snapshot.stack],
          selected_stack_id: stackId,
          stack: snapshot.stack,
          readiness: snapshot.readiness,
          nextSteps: snapshot.nextSteps,
          kpis: snapshot.kpis,
          servers: snapshot.servers,
          services: snapshot.services,
          monitoring: snapshot.monitoring,
          alerts: snapshot.alerts,
          jobs: [],
        }),
      );
    });

    await page.goto(`/monitoring?stack_id=${stackId}`);
    await expect(
      page.getByTestId("monitoring-connected-agent-count"),
    ).toHaveText("1");

    cockpitUnavailable = true;
    await page.getByRole("button", { name: "Refresh" }).click();

    await expect(
      page.getByTestId("monitoring-connected-agent-count"),
    ).toHaveText("1");
    await expect(
      page.getByTestId("monitoring-connected-agents-stale"),
    ).toContainText("Last verified count retained");
  });

  test("assigns an available server before Review + Start", async ({
    page,
  }) => {
    const runtime = await mockPostRolloutApi(page);

    await page.goto(`/stacks?stack=${stackId}&phase=review`);
    await expect(page.getByTestId("stack-operations-dashboard")).toBeVisible();
    await expect(page.getByTestId("assign-server-button")).toBeVisible();
    await expect(page.getByTestId("worker-connected-count")).toContainText(
      "0/1 connected",
    );
    await expect(page.getByTestId("review-start-button")).toBeDisabled();

    await page.getByTestId("assign-server-button").click();
    await expect(page.getByTestId("worker-connected-count")).toContainText(
      "0/1 connected",
    );
    await expect(page.getByTestId("review-start-button")).toBeDisabled();

    runtime.reportGuardConnected();
    // Refresh lives in the single top action bar, not inside the operations
    // section.
    await page.getByTestId("dashboard-refresh-button").click();
    await expect(page.getByTestId("worker-connected-count")).toContainText(
      "1/1 connected",
    );
    await expect(page.getByTestId("review-start-button")).toBeEnabled();

    await page.getByTestId("review-start-button").click();
    await expect(page).toHaveURL(/\/stacks\/creating/);
    await expect(page).toHaveURL(/job_id=job-rollout/);
    await expect(page).toHaveURL(new RegExp(`stack_id=${stackId}`));
  });

  test("keeps StackKit rollout visible for a healthy user-owned server after the current deploy failed", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, { includeServices: false });
    await page.route(
      `**/api/v1/stacks/${stackId}/operations`,
      async (route) => {
        const payload = operationsPayload(true, false, { connected: true });
        payload.stack = {
          ...payload.stack,
          status: "running",
          state: "running",
        };
        payload.readiness = {
          ...payload.readiness,
          status: "ready",
          can_start: true,
          review_required: true,
          message:
            "The last rollout failed. Review the configuration and start a fresh rollout when ready.",
        };
        await fulfillJson(
          route,
          apiEnvelope({
            ...payload,
            latestFailure: {
              job_id: "job-failed-byo-rollout",
              type: "deploy",
              state: "failed",
              step: "stackkit_rollout",
              message: "StackKit rollout failed",
            },
          }),
        );
      },
    );

    await page.goto(`/stacks?stack=${stackId}`);
    await expect(page.getByTestId("review-start-button")).toBeVisible();
    await expect(page.getByTestId("review-start-button")).toBeEnabled();
    await expect(page.getByTestId("stackkit-rollout-guidance")).toContainText(
      "Your server is connected. Continue with the StackKit rollout.",
    );

    await page.getByTestId("review-start-button").click();
    await expect(page).toHaveURL(/job_id=job-rollout/);
    await expect(page).toHaveURL(/phase=rollout/);
  });

  test("opens retained live stack detail from operations evidence without a PocketBase stack record", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, {
      monthlyRuntime: true,
      pocketBaseStackMissing: true,
    });

    await page.goto(`/stacks/${stackId}`);
    await expect(page).toHaveURL(new RegExp(`/stacks/${stackId}`));
    await expect(page.getByTestId("monthly-runtime-card")).toBeVisible();
    await expect(page.getByTestId("monthly-runtime-card")).toContainText(
      "lease-stack-ops",
    );
    await expect(page.getByTestId("monthly-runtime-enrollment")).toContainText(
      /enrolled/i,
    );
    await expect(
      page.getByTestId("stack-detail-monitoring-evidence"),
    ).toContainText("embedded-tsdb");
    await expect(
      page.getByTestId("stack-detail-service-registry"),
    ).toContainText("Pocket ID");
  });

  test("opens server detail on mobile without horizontal overflow", async ({
    page,
  }) => {
    await mockPostRolloutApi(page);
    await page.setViewportSize({ width: 320, height: 800 });

    await page.goto(`/stacks?stack=${stackId}&phase=review`);
    await expect(page.getByTestId("server-card")).toBeVisible();

    const dashboardOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(dashboardOverflow).toBe(false);

    // The shared ServerCard exposes the details link as its header anchor.
    await page.getByTestId("server-card").locator("a").first().click();
    await expect(page.getByTestId("server-details-page")).toBeVisible();

    const detailsOverflow = await page.evaluate(
      () =>
        document.documentElement.scrollWidth >
        document.documentElement.clientWidth,
    );
    expect(detailsOverflow).toBe(false);
  });

  test("shows observed StackKit, addresses, domains and live service endpoints in server details", async ({
    page,
  }) => {
    await mockPostRolloutApi(page);

    await page.goto(`/stacks/${stackId}/servers/${workerId}`);

    await expect(page.getByTestId("server-details-page")).toBeVisible();
    await expect(page.getByText("Ubuntu 24.04.2 LTS/amd64")).toBeVisible();
    await expect(page.getByTestId("server-stackkit-details")).toContainText(
      "Basement Kit",
    );
    await expect(page.getByTestId("server-stackkit-details")).toContainText(
      "1.4.0 · standard · homelab · coolify · local",
    );
    await expect(page.getByTestId("server-stackkit-details")).toContainText(
      "observed",
    );
    await expect(page.getByTestId("server-network-details")).toContainText(
      "10.0.0.10",
    );
    await expect(page.getByTestId("server-domains")).toContainText(
      "base.home.localhost",
    );

    const endpoint = page
      .getByTestId("server-service-endpoints")
      .getByRole("link", { name: "http://base.home.localhost" });
    await expect(endpoint).toHaveAttribute(
      "href",
      "http://base.home.localhost",
    );
    await expect(page.getByTestId("server-service-endpoints")).toContainText(
      "healthy",
    );
    await expect(page.getByTestId("server-service-endpoints")).toContainText(
      "StackKit access manifest",
    );
  });

  test("keeps overview cards out of other tabs and decommissions only from settings", async ({
    page,
  }) => {
    await mockPostRolloutApi(page, {
      monthlyRuntime: true,
      managedRuntimeProjection: true,
    });

    await page.goto(`/stacks/${stackId}/servers/${workerId}`);

    const tabs = page.getByTestId("server-details-tabs");
    const overviewHeader = page.getByRole("heading", {
      name: "ops-managed-runtime",
    });
    await expect(tabs).toBeVisible();
    await expect(overviewHeader).toBeVisible();
    await expect(page.getByTestId("server-decommission-button")).toHaveCount(0);

    await page.getByTestId("server-tab-services").click();
    await expect(overviewHeader).toHaveCount(0);
    await expect(page.getByText("Services", { exact: true })).toBeVisible();

    await page.getByTestId("server-tab-settings").click();
    await expect(page.getByTestId("server-settings-panel")).toBeVisible();
    await expect(page.getByTestId("server-danger-zone")).toContainText(
      "Decommission server",
    );
    await page.getByTestId("server-decommission-button").click();
    await expect(
      page.getByTestId("server-decommission-confirmation"),
    ).toBeVisible();
    await expect(
      page.getByTestId("server-decommission-confirm-button"),
    ).toBeVisible();
    await expect(page.getByText("Force decommission")).toHaveCount(0);
  });

  test("runs lifecycle actions from the concrete server settings without a target picker", async ({
    page,
  }) => {
    await mockPostRolloutApi(page);
    await page.route(
      `**/api/v1/stacks/${stackId}/servers/${workerId}`,
      async (route) => {
        await fulfillJson(
          route,
          apiEnvelope({
            stack: stackRecord(),
            server: serverRecord(true, { connected: true }),
            services: operationsPayload(true).services,
            checks: [],
            logs: [],
            health: serverRecord(true, { connected: true }).health,
            monitoring: operationsPayload(true).monitoring,
          }),
        );
      },
    );

    let lifecycleBody: unknown = null;
    await page.route(
      `**/api/v1/stacks/${stackId}/stackkit/operations`,
      async (route) => {
        lifecycleBody = route.request().postDataJSON();
        await fulfillJson(
          route,
          apiEnvelope({
            job_id: "job-upgrade",
            stack_id: stackId,
            agent_id: "agent-ops-1",
            operation: "upgrade",
            status: "accepted",
          }),
          202,
        );
      },
    );

    await page.goto(`/stacks/${stackId}/servers/${workerId}`);
    await page.getByTestId("server-tab-settings").click();

    await expect(page.getByTestId("server-lifecycle-actions")).toBeVisible();
    await expect(page.getByTestId("server-lifecycle-upgrade")).toBeVisible();
    await expect(page.locator("#stackkit-lifecycle-agent")).toHaveCount(0);

    await page.getByTestId("server-lifecycle-upgrade").click();
    await expect(
      page.getByTestId("server-lifecycle-confirmation"),
    ).toContainText("ops-node-1");
    await page.getByTestId("server-lifecycle-confirm-button").click();

    await expect
      .poll(() => lifecycleBody)
      .toEqual({
        operation: "upgrade",
        agent_id: "agent-ops-1",
        target_release: "latest",
        owner_approved: true,
      });
    await expect(page.getByTestId("server-lifecycle-accepted")).toContainText(
      "job-upgrade",
    );
  });

  test("keeps configured StackKit intent and stale runtime evidence explicit", async ({
    page,
  }) => {
    await mockPostRolloutApi(page);

    const configuredServer = {
      ...serverRecord(true),
      status: "stale",
      stackkit: {
        name: "Cloud Kit",
        catalog_ref: "cloud-kit",
        mode: "standard",
        context: "cloud",
        state: "configured",
        sources: ["stack-config"],
      },
      service_endpoints: [
        {
          service_key: "base",
          name: "Base",
          url: "https://base.kombified.com",
          domain: "base.kombified.com",
          visibility: "public",
          health: "unknown",
          provenance: "stack-config",
          source: "registry-store",
          observed_at: "2026-05-18T08:00:00Z",
        },
      ],
      health: {
        ...serverRecord(true).health,
        state: "stale",
        source: "guard inventory",
        updated_at: "2026-05-18T08:00:00Z",
      },
    };

    await page.route(
      `**/api/v1/stacks/${stackId}/servers/${workerId}`,
      async (route) => {
        await fulfillJson(
          route,
          apiEnvelope({
            stack: stackRecord(),
            server: configuredServer,
            services: [],
            checks: [],
            logs: [],
            health: configuredServer.health,
            monitoring: operationsPayload(true).monitoring,
          }),
        );
      },
    );

    await page.goto(`/stacks/${stackId}/servers/${workerId}`);

    await expect(page.getByText("stale", { exact: true })).toBeVisible();
    const stackKit = page.getByTestId("server-stackkit-details");
    await expect(stackKit).toContainText("configured");
    await expect(stackKit).toContainText("stack-config");
    await expect(stackKit).toContainText(
      "the Guard has not reported matching deployment evidence yet",
    );
    await expect(stackKit).not.toContainText("observed");
    await expect(page.getByTestId("server-service-endpoints")).toContainText(
      "unknown",
    );
    await expect(
      page
        .getByTestId("server-service-endpoints")
        .getByRole("link", { name: "https://base.kombified.com" }),
    ).toHaveCount(0);
    await expect(page.getByTestId("server-service-endpoints")).toContainText(
      "https://base.kombified.com",
    );
  });
});
