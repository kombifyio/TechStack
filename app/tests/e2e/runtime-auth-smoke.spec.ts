import { expect, test } from "@playwright/test";
import { createHash } from "node:crypto";
import { mkdir, writeFile } from "node:fs/promises";
import path from "node:path";

import {
  authenticateRuntimeUser,
  fetchBrowserWhoAmI,
  type RuntimeAuthRole,
} from "./runtime-auth";
import {
  assertRuntimeInventoryExpectation,
  runtimeInventoryExpectation,
  type RuntimeInventoryExpectation,
} from "./runtime-inventory-profile";

const PRODUCT_BASE = requireLiveProductURL(
  "TechStack product",
  firstProductURL(
    process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL,
    process.env.TECHSTACK_E2E_PRODUCT_URL,
    process.env.PLAYWRIGHT_BASE_URL,
  ) || "https://techstack.kombify.io",
);
const API_BASE = requireLiveProductURL(
  "TechStack API",
  firstProductURL(
    process.env.TECHSTACK_RUNTIME_E2E_API_URL,
    process.env.TECHSTACK_API_URL,
    PRODUCT_BASE,
  ) || PRODUCT_BASE,
);
const SESSION_COOKIE_NAME =
  process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session";
const RUNTIME_AUTH_OPTIONS = {
  productBase: PRODUCT_BASE,
  apiBase: API_BASE,
  sessionCookieName: SESSION_COOKIE_NAME,
};
const INVENTORY_TOOLS = [
  "get_stack_operations",
  "list_servers",
  "list_services",
  "server_access_context",
  "server_health",
].sort();
const INVENTORY_EXPECTATION = runtimeInventoryExpectation(process.env);

const allRoles: RuntimeAuthRole[] = ["oneliner", "remote", "cloud"];
const roles = runtimeAuthRolesFromEnv(
  process.env.TECHSTACK_RUNTIME_E2E_AUTH_ROLES,
);

test.describe.serial("Runtime Auth0 smoke", () => {
  for (const role of roles) {
    test(`${role} user can create a TechStack browser session`, async ({
      page,
    }) => {
      const auth = await authenticateRuntimeUser(
        role,
        page,
        RUNTIME_AUTH_OPTIONS,
      );
      expect(auth.token).toBeTruthy();
      await expect(page.getByText("Session Expired")).toHaveCount(0);
      const whoami = await fetchBrowserWhoAmI(page);
      await test.info().attach(`${role}-whoami.json`, {
        body: Buffer.from(`${JSON.stringify(whoami.body, null, 2)}\n`, "utf8"),
        contentType: "application/json",
      });
      expect(whoami.status).toBe(200);
      if (role === "cloud") {
        if (INVENTORY_EXPECTATION) {
          assertCanonicalDemoPrincipal(whoami.body);
        }
        await verifyCloudInventoryCore(page, auth.token);
      }
    });
  }

  test("cloud user can create a session through the Windows browser handoff login URL", async ({
    page,
  }) => {
    const auth = await authenticateRuntimeUser("cloud", page, {
      ...RUNTIME_AUTH_OPTIONS,
      entryPath:
        "/api/v2/auth/login?return_to=%2Fstacks&client=windows&open_browser=1",
    });
    expect(auth.token).toBeTruthy();
    await expect(page.getByText("Session Expired")).toHaveCount(0);
    const whoami = await fetchBrowserWhoAmI(page);
    await test.info().attach("cloud-windows-browser-handoff-whoami.json", {
      body: Buffer.from(`${JSON.stringify(whoami.body, null, 2)}\n`, "utf8"),
      contentType: "application/json",
    });
    expect(whoami.status).toBe(200);
    if (INVENTORY_EXPECTATION) {
      assertCanonicalDemoPrincipal(whoami.body);
    }
  });
});

async function verifyCloudInventoryCore(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  token: string,
) {
  // The general live Auth0 smoke follows the canonical runtime authorities.
  // The older exact local-anchor profile retains its REST/MCP compatibility
  // proof below until that fixture is migrated independently.
  if (!INVENTORY_EXPECTATION) {
    await verifyCanonicalCloudRuntimeCore(page, token);
    return;
  }
  const servers = await authenticatedJSON(token, "/api/v1/inventory/servers");
  const services = await authenticatedJSON(token, "/api/v1/inventory/services");
  const initialize = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: { "MCP-Protocol-Version": "2025-11-25" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-initialize",
      method: "initialize",
      params: {
        protocolVersion: "2025-11-25",
        capabilities: {},
        clientInfo: { name: "techstack-p1-gate", version: "1" },
      },
    }),
  });
  const tools = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: { "MCP-Protocol-Version": "2025-11-25" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-tools",
      method: "tools/list",
      params: {},
    }),
  });
  const listServers = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: { "MCP-Protocol-Version": "2025-11-25" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-list-servers",
      method: "tools/call",
      params: { name: "list_servers", arguments: {} },
    }),
  });
  const listServices = await authenticatedJSON(token, "/api/v1/mcp", {
    method: "POST",
    headers: { "MCP-Protocol-Version": "2025-11-25" },
    body: JSON.stringify({
      jsonrpc: "2.0",
      id: "p1-list-services",
      method: "tools/call",
      params: { name: "list_services", arguments: {} },
    }),
  });

  const actualTools = Array.isArray(tools.body?.result?.tools)
    ? tools.body.result.tools
        .map((tool: { name?: unknown }) => String(tool?.name ?? "").trim())
        .filter(Boolean)
        .sort()
    : [];
  expect(actualTools).toEqual(INVENTORY_TOOLS);
  expect(initialize.body?.result?.protocolVersion).toBe("2025-11-25");
  expect(listServers.body?.result?.isError).toBe(false);
  expect(listServices.body?.result?.isError).toBe(false);

  const restServerIDs = inventoryIDs(servers.body, "servers", true);
  const restServiceIDs = inventoryIDs(services.body, "services", true);
  const exactInventory = INVENTORY_EXPECTATION
    ? assertRuntimeInventoryExpectation(
        INVENTORY_EXPECTATION,
        servers.body,
        services.body,
      )
    : null;
  expect(
    restServerIDs.length,
    "P1 inventory proof requires at least one real REST server",
  ).toBeGreaterThan(0);
  expect(
    restServiceIDs.length,
    "P1 inventory proof requires at least one real REST service",
  ).toBeGreaterThan(0);
  const mcpServerIDs = inventoryIDs(
    listServers.body?.result?.structuredContent,
    "servers",
    false,
  );
  const mcpServiceIDs = inventoryIDs(
    listServices.body?.result?.structuredContent,
    "services",
    false,
  );
  expect(mcpServerIDs).toEqual(restServerIDs);
  expect(mcpServiceIDs).toEqual(restServiceIDs);
  if (INVENTORY_EXPECTATION && exactInventory) {
    const exactMCPInventory = assertRuntimeInventoryExpectation(
      INVENTORY_EXPECTATION,
      listServers.body?.result?.structuredContent,
      listServices.body?.result?.structuredContent,
    );
    expect(mcpServerIDs).toEqual(exactInventory.serverIds);
    expect(mcpServiceIDs).toEqual(exactInventory.serviceIds);
    expect(exactMCPInventory.semantic).toEqual(exactInventory.semantic);
  }
  expect(
    mcpServerIDs.length,
    "P1 inventory proof requires at least one MCP server",
  ).toBeGreaterThan(0);
  expect(
    mcpServiceIDs.length,
    "P1 inventory proof requires at least one MCP service",
  ).toBeGreaterThan(0);

  const artifactsDir = path.resolve(
    process.env.RUNTIME_E2E_ARTIFACTS_DIR ?? "../artifacts/runtime-e2e",
  );
  await mkdir(artifactsDir, { recursive: true });
  const stackOperationStatuses: number[] = [];
  page.on("response", (response) => {
    if (isStackOperationsURL(response.url())) {
      stackOperationStatuses.push(response.status());
    }
  });
  const stacksResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "stacks"),
    { timeout: 20_000 },
  );
  await page.goto("/stacks", { waitUntil: "domcontentloaded" });
  const stacksResponse = await stacksResponsePromise;
  assertCandidateUIResponseOrigin(stacksResponse.url());
  expect(stacksResponse.ok()).toBe(true);
  const stackIDs = uiStackIDs(await stacksResponse.json());
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();
  if (stackIDs.length > 0) {
    await expect(page.getByTestId("stack-action-bar")).toBeVisible();
    await expect
      .poll(() => stackOperationStatuses.some(isHTTP2xx), { timeout: 20_000 })
      .toBe(true);
  }
  await expect(page.getByTestId("stacks-error-panel")).toHaveCount(0);
  // Unattested: the dashboard leg's proof is the assertions above. The image
  // is kept for triage only, so it must not carry the inventory artifact name
  // the gate hashes against the evidence.
  await writeFile(
    path.join(artifactsDir, "p1-dashboard.png"),
    await page.screenshot({ fullPage: true }),
  );

  // The canonical server inventory is an observation surface, not a dashboard
  // control: the dashboard renders the servers the operator acts on, and the
  // authoritative record (with its REST/DOM parity proof) lives on /monitoring.
  const monitoringInventoryResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "inventory/servers"),
    { timeout: 20_000 },
  );
  await page.goto("/monitoring", { waitUntil: "domcontentloaded" });
  const monitoringInventoryResponse = await monitoringInventoryResponsePromise;
  assertCandidateUIResponseOrigin(monitoringInventoryResponse.url());
  expect(monitoringInventoryResponse.ok()).toBe(true);
  const dashboardServerIDs = inventoryIDs(
    await monitoringInventoryResponse.json(),
    "servers",
    true,
  );
  expect(dashboardServerIDs).toEqual(restServerIDs);
  await expect(page.getByTestId("inventory-server-list")).toBeVisible();
  await expect(page.getByTestId("inventory-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("inventory-server-card")).toHaveCount(
    dashboardServerIDs.length,
  );
  const dashboardDOMServerIDs = await domInventoryIDs(
    page,
    "inventory-server-card",
    "data-server-id",
  );
  expect(dashboardDOMServerIDs).toEqual(restServerIDs);
  expect(
    dashboardDOMServerIDs.length,
    "P1 inventory proof requires a canonical server card",
  ).toBeGreaterThan(0);
  if (INVENTORY_EXPECTATION && exactInventory) {
    await assertExpectedDashboardInventory(
      page,
      INVENTORY_EXPECTATION,
      exactInventory.semantic.server.privateIp,
    );
  }
  const inventoryScreenshot = await page.screenshot({ fullPage: true });
  await writeFile(
    path.join(artifactsDir, "p1-inventory-monitoring.png"),
    inventoryScreenshot,
  );

  const servicesResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "registry/services"),
    { timeout: 20_000 },
  );
  const servicesInventoryResponsePromise = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "inventory/services"),
    { timeout: 20_000 },
  );
  await page.goto("/services", { waitUntil: "domcontentloaded" });
  const [servicesResponse, servicesInventoryResponse] = await Promise.all([
    servicesResponsePromise,
    servicesInventoryResponsePromise,
  ]);
  assertCandidateUIResponseOrigin(servicesResponse.url());
  assertCandidateUIResponseOrigin(servicesInventoryResponse.url());
  expect(servicesResponse.ok()).toBe(true);
  expect(servicesInventoryResponse.ok()).toBe(true);
  const registryServiceIDs = uiRegistryServiceIDs(
    await servicesResponse.json(),
  );
  const uiServiceIDs = inventoryIDs(
    await servicesInventoryResponse.json(),
    "services",
    true,
  );
  expect(uiServiceIDs).toEqual(restServiceIDs);
  await expect(page.getByTestId("services-page")).toBeVisible();
  await expect(page.getByTestId("runtime-service-card")).toHaveCount(
    uiServiceIDs.length,
  );
  await expect(page.getByTestId("runtime-service-list")).toBeVisible();
  await expect(page.getByTestId("inventory-services-error-panel")).toHaveCount(
    0,
  );
  const registryWarningCount = await page
    .getByTestId("registry-services-warning")
    .count();
  const registryManagementAction = page.getByTestId("new-service-button");
  await expect(registryManagementAction).toBeEnabled({ timeout: 10_000 });
  const registryManagementActionEnabled =
    await registryManagementAction.isEnabled();
  expect(registryWarningCount).toBe(0);
  expect(registryManagementActionEnabled).toBe(true);
  const servicesDOMIDs = await domInventoryIDs(
    page,
    "runtime-service-card",
    "data-service-id",
  );
  expect(servicesDOMIDs).toEqual(restServiceIDs);
  expect(
    servicesDOMIDs.length,
    "P1 inventory proof requires a canonical service card",
  ).toBeGreaterThan(0);
  if (INVENTORY_EXPECTATION) {
    await assertExpectedServicesInventory(page, INVENTORY_EXPECTATION);
  }
  const servicesScreenshot = await page.screenshot({ fullPage: true });
  await writeFile(
    path.join(artifactsDir, "p1-inventory-services.png"),
    servicesScreenshot,
  );

  const evidence = {
    schema_version: 1,
    kind: "techstack-p1-inventory-core",
    result: "PASS",
    head_sha: process.env.GITHUB_SHA ?? "",
    origin: PRODUCT_BASE,
    expectation: {
      profile: INVENTORY_EXPECTATION?.profile ?? null,
      exact_fixture_required: Boolean(INVENTORY_EXPECTATION),
      exact_fixture_verified: Boolean(exactInventory),
    },
    rest: {
      servers_status: servers.status,
      services_status: services.status,
      response_shape_valid: true,
      server_count: restServerIDs.length,
      service_count: restServiceIDs.length,
      server_ids_sha256: idsSha256(restServerIDs),
      service_ids_sha256: idsSha256(restServiceIDs),
    },
    mcp: {
      initialize_status: initialize.status,
      protocol_version: initialize.body?.result?.protocolVersion,
      tools_list_status: tools.status,
      actual_tools: actualTools,
      list_servers_status: listServers.status,
      list_servers_is_error: listServers.body?.result?.isError,
      list_services_status: listServices.status,
      list_services_is_error: listServices.body?.result?.isError,
      response_shape_valid: true,
      server_count: mcpServerIDs.length,
      service_count: mcpServiceIDs.length,
      server_ids_sha256: idsSha256(mcpServerIDs),
      service_ids_sha256: idsSha256(mcpServiceIDs),
      server_ids_match_rest: true,
      service_ids_match_rest: true,
      server_count_matches_rest: true,
      service_count_matches_rest: true,
      semantic_fields_match_rest: Boolean(exactInventory),
    },
    ui: {
      dashboard: {
        // The canonical inventory proof moved to /monitoring; the dashboard
        // surface is still exercised above (rendered, no error panel).
        path: "/monitoring",
        rendered: true,
        backend_origin: new URL(monitoringInventoryResponse.url()).origin,
        backend_status: monitoringInventoryResponse.status(),
        backend_response_shape_valid: true,
        backend_item_count: dashboardServerIDs.length,
        backend_ids_sha256: idsSha256(dashboardServerIDs),
        dom_item_count: dashboardDOMServerIDs.length,
        dom_ids_sha256: idsSha256(dashboardDOMServerIDs),
        dom_ids_match_backend: true,
        dom_count_matches_backend: true,
        legacy_stacks_status: stacksResponse.status(),
        stack_operations_status: stackOperationStatuses.find(isHTTP2xx) ?? null,
        expected_state: "data",
        state_matches_backend: true,
        error_panel_absent: true,
        screenshot_sha256: sha256(inventoryScreenshot),
      },
      services: {
        path: "/services",
        rendered: true,
        backend_origin: new URL(servicesInventoryResponse.url()).origin,
        backend_status: servicesInventoryResponse.status(),
        backend_response_shape_valid: true,
        backend_item_count: uiServiceIDs.length,
        backend_ids_sha256: idsSha256(uiServiceIDs),
        dom_item_count: servicesDOMIDs.length,
        dom_ids_sha256: idsSha256(servicesDOMIDs),
        dom_ids_match_backend: true,
        dom_count_matches_backend: true,
        registry_backend_status: servicesResponse.status(),
        registry_response_shape_valid: true,
        registry_item_count: registryServiceIDs.length,
        registry_warning_absent: registryWarningCount === 0,
        registry_management_action_enabled: registryManagementActionEnabled,
        expected_state: "data",
        state_matches_backend: true,
        error_panel_absent: true,
        screenshot_sha256: sha256(servicesScreenshot),
        registry_backend_ok: servicesResponse.ok(),
      },
    },
    verified_at: new Date().toISOString(),
  };
  await writeFile(
    path.join(artifactsDir, "inventory-core-evidence.json"),
    `${JSON.stringify(evidence, null, 2)}\n`,
    "utf8",
  );
}

async function verifyCanonicalCloudRuntimeCore(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  token: string,
) {
  const servers = await authenticatedJSON(token, "/api/v1/servers");
  const services = await authenticatedJSON(token, "/api/v1/services");
  const cockpit = await authenticatedJSON(token, "/api/v1/monitor/cockpit");
  const serverRows = canonicalRows(servers.body, "servers");
  const serviceRows = canonicalRows(services.body, "services");
  const restServerIDs = canonicalRowIDs(serverRows, "servers");
  const restServiceIDs = canonicalRowIDs(serviceRows, "services");
  const expectedServerRef = String(
    process.env.TECHSTACK_RUNTIME_E2E_EXPECTED_SERVER_ID ?? "",
  ).trim();
  const expectedServiceCount = Number(
    process.env.TECHSTACK_RUNTIME_E2E_EXPECTED_SERVICE_COUNT ?? "0",
  );
  const expectedServer = expectedServerRef
    ? serverRows.find((candidate) =>
        [candidate.id, candidate.name]
          .map((value) => String(value ?? "").trim())
          .includes(expectedServerRef),
      )
    : undefined;
  const cockpitData = requireRecord(
    requireRecord(cockpit.body, "cockpit response").data,
    "cockpit data",
  );
  const selectedTechstackID = String(
    expectedServer?.techstack_id ?? cockpitData.techstack_id ?? "",
  ).trim();

  expect(
    restServerIDs.length,
    "Canonical runtime proof requires at least one real server",
  ).toBeGreaterThan(0);
  if (expectedServerRef) {
    expect(
      expectedServer,
      `Canonical servers omitted id/name ${expectedServerRef}; observed=${JSON.stringify(serverDiagnostic(serverRows))}`,
    ).toBeTruthy();
    expect(String(expectedServer?.environment_class ?? "").toLowerCase()).toBe(
      "cloud",
    );
    expect(String(expectedServer?.offering ?? "").toLowerCase()).toBe(
      "external_vps",
    );
    expect(String(expectedServer?.provider_id ?? "").toLowerCase()).toBe(
      "hostinger",
    );

    const expectedServerID = String(expectedServer?.id ?? "").trim();
    const serverServices = serviceRows.filter(
      (service) => String(service.server_id ?? "").trim() === expectedServerID,
    );
    if (Number.isInteger(expectedServiceCount) && expectedServiceCount > 0) {
      const cockpit = await authenticatedJSON(
        token,
        `/api/v1/monitor/cockpit?techstack_id=${encodeURIComponent(String(expectedServer?.techstack_id ?? "").trim())}`,
      );
      expect(
        serverServices,
        `Canonical service authority omitted ${expectedServerRef}; cockpit diagnostic=${JSON.stringify(cockpitServiceDiagnostic(cockpit.body))}`,
      ).toHaveLength(expectedServiceCount);
    }
    for (const service of serverServices) {
      expect(service.target_kind).toBe("server");
      expect(service.management_state).toBe("observed");
      expect(String(service.desired_state ?? "").trim()).not.toBe("");
      expect(String(service.observed_state ?? "").trim()).not.toBe("");
      expect(
        String(
          requireRecord(service.health, "service health").state ?? "",
        ).trim(),
      ).not.toBe("");
      expect(
        [
          service.desired_state,
          service.observed_state,
          requireRecord(service.health, "service health").state,
        ].map((value) => String(value ?? "").toLowerCase()),
      ).not.toContain("migrating");
    }
  }

  const artifactsDir = path.resolve(
    process.env.RUNTIME_E2E_ARTIFACTS_DIR ?? "../artifacts/runtime-e2e",
  );
  await mkdir(artifactsDir, { recursive: true });

  const techstackQuery = selectedTechstackID
    ? `?techstack_id=${encodeURIComponent(selectedTechstackID)}`
    : "";
  await page.goto(`/stacks${techstackQuery}`, {
    waitUntil: "domcontentloaded",
  });
  await expect(page.getByTestId("stacks-dashboard")).toBeVisible();
  await expect(page.getByTestId("stacks-error-panel")).toHaveCount(0);
  if (expectedServiceCount > 0) {
    await expect(page.getByTestId("dashboard-services-summary")).toContainText(
      `${expectedServiceCount} runtime services recorded`,
    );
  }
  await writeFile(
    path.join(artifactsDir, "canonical-dashboard.png"),
    await page.screenshot({ fullPage: true }),
  );

  const monitoringServersResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "servers"),
    { timeout: 20_000 },
  );
  await page.goto(`/monitoring${techstackQuery}`, {
    waitUntil: "domcontentloaded",
  });
  const monitoringResponse = await monitoringServersResponse;
  expect(monitoringResponse.ok()).toBe(true);
  const monitoringAPIRows = canonicalRows(
    await monitoringResponse.json(),
    "servers",
  );
  const monitoringAPIIDs = canonicalRowIDs(monitoringAPIRows, "servers");
  await expect(page.getByTestId("inventory-unavailable")).toHaveCount(0);
  await expect(page.getByTestId("inventory-server-card")).toHaveCount(
    monitoringAPIIDs.length,
  );
  const monitoringDOMIDs = await domInventoryIDs(
    page,
    "inventory-server-card",
    "data-server-id",
  );
  expect(monitoringDOMIDs).toEqual(monitoringAPIIDs);
  if (expectedServer) {
    const expectedServerID = String(expectedServer.id ?? "").trim();
    const serverCard = page.locator(
      `[data-testid="inventory-server-card"][data-server-id="${expectedServerID}"]`,
    );
    await expect(serverCard).toHaveCount(1);
    await expect(serverCard).toContainText(/cloud/i);
    await expect(serverCard).toContainText(/external vps/i);
    await expect(serverCard).toContainText(/hostinger/i);
  }
  await writeFile(
    path.join(artifactsDir, "canonical-monitoring.png"),
    await page.screenshot({ fullPage: true }),
  );

  const servicesResponse = page.waitForResponse(
    (response) =>
      response.request().method() === "GET" &&
      isUIAPIURL(response.url(), "services"),
    { timeout: 20_000 },
  );
  await page.goto("/services", { waitUntil: "domcontentloaded" });
  const servicesUIResponse = await servicesResponse;
  expect(servicesUIResponse.ok()).toBe(true);
  const servicesAPIIDs = canonicalRowIDs(
    canonicalRows(await servicesUIResponse.json(), "services"),
    "services",
  );
  await expect(page.getByTestId("inventory-services-error-panel")).toHaveCount(
    0,
  );
  await expect(page.getByTestId("runtime-service-card")).toHaveCount(
    servicesAPIIDs.length,
  );
  const servicesDOMIDs = await domInventoryIDs(
    page,
    "runtime-service-card",
    "data-service-id",
  );
  expect(servicesDOMIDs).toEqual(servicesAPIIDs);
  expect(servicesAPIIDs).toEqual(restServiceIDs);
  await writeFile(
    path.join(artifactsDir, "canonical-services.png"),
    await page.screenshot({ fullPage: true }),
  );

  await writeFile(
    path.join(artifactsDir, "canonical-runtime-evidence.json"),
    `${JSON.stringify(
      {
        schema_version: 1,
        kind: "techstack-canonical-runtime-live",
        result: "PASS",
        head_sha: process.env.GITHUB_SHA ?? "",
        origin: PRODUCT_BASE,
        server_count: restServerIDs.length,
        service_count: restServiceIDs.length,
        server_ids: restServerIDs,
        service_ids_sha256: idsSha256(restServiceIDs),
        expected_server_ref: expectedServerRef || null,
        resolved_server_id: expectedServer
          ? String(expectedServer.id ?? "").trim()
          : null,
        expected_service_count: expectedServiceCount || null,
        dashboard_matches_api: true,
        monitoring_techstack_id: selectedTechstackID || null,
        monitoring_server_count: monitoringAPIIDs.length,
        monitoring_matches_api: true,
        services_matches_api: true,
        verified_at: new Date().toISOString(),
      },
      null,
      2,
    )}\n`,
    "utf8",
  );
}

function cockpitServiceDiagnostic(body: unknown) {
  const root = requireRecord(body, "cockpit response");
  const data = requireRecord(root.data, "cockpit data");
  const services = Array.isArray(data.services) ? data.services : [];
  return {
    techstack_id: String(data.techstack_id ?? "").trim(),
    count: services.length,
    services: services.map((service, index) => {
      const row = requireRecord(service, `cockpit services[${index}]`);
      return {
        id: String(row.id ?? "").trim(),
        name: String(row.name ?? "").trim(),
        status: String(row.status ?? "").trim(),
        target_server_id: String(row.target_server_id ?? "").trim(),
      };
    }),
  };
}

function serverDiagnostic(rows: Record<string, any>[]) {
  return rows.map((row) => ({
    id: String(row.id ?? "").trim(),
    name: String(row.name ?? "").trim(),
    techstack_id: String(row.techstack_id ?? "").trim(),
    environment_class: String(row.environment_class ?? "").trim(),
    offering: String(row.offering ?? "").trim(),
    provider_id: String(row.provider_id ?? "").trim(),
  }));
}

async function assertExpectedDashboardInventory(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  expectation: RuntimeInventoryExpectation,
  observedPrivateIP: string,
) {
  const card = page.locator(
    `[data-testid="inventory-server-card"][data-server-id="${expectation.server.id}"]`,
  );
  await expect(card).toHaveCount(1);
  await expect(
    card.getByText(observedPrivateIP, { exact: true }),
  ).toBeVisible();
  await expect(
    card.getByText(expectation.server.domain, { exact: true }),
  ).toBeVisible();
  await expect(card).toContainText(
    new RegExp(escapeRegExp(expectation.server.os), "i"),
  );
  await expect(
    card.getByText(
      [
        expectation.server.stackkit,
        expectation.server.version,
        expectation.server.variant,
      ].join(" · "),
      { exact: true },
    ),
  ).toBeVisible();
  await expect(
    card.getByText(
      new RegExp(`^${escapeRegExp(expectation.server.health)}$`, "i"),
    ),
  ).toBeVisible();
  const connection = card.getByTestId("inventory-server-connection-state");
  await expect(connection).toBeVisible();
  await expect(connection).toHaveText(
    new RegExp(`^${escapeRegExp(expectation.server.connection)}$`, "i"),
  );
  const lifecycle = card.getByTestId("inventory-server-lifecycle-state");
  await expect(lifecycle).toBeVisible();
  await expect(lifecycle).toHaveText(
    new RegExp(`^${escapeRegExp(expectation.server.lifecycle)}$`, "i"),
  );
}

async function assertExpectedServicesInventory(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  expectation: RuntimeInventoryExpectation,
) {
  await expect(page.getByTestId("runtime-service-card")).toHaveCount(
    expectation.services.length,
  );
  for (const service of expectation.services) {
    const card = page.locator(
      `[data-testid="runtime-service-card"][data-service-id="${service.id}"]`,
    );
    await expect(card).toHaveCount(1);
    await expect(
      card.getByText(new RegExp(`^${escapeRegExp(service.health)}$`, "i")),
    ).toBeVisible();
    await expect(card.locator("article")).toHaveCount(1);
    await expect(
      card.getByRole("button", { name: "Open", exact: true }),
    ).toBeVisible();
  }
}

async function authenticatedJSON(
  token: string,
  endpoint: string,
  init: RequestInit = {},
) {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    ...init,
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      ...(init.body ? { "Content-Type": "application/json" } : {}),
      ...(init.headers ?? {}),
    },
    signal: AbortSignal.timeout(15_000),
  });
  const text = await response.text();
  let body: any;
  try {
    body = text ? JSON.parse(text) : null;
  } catch {
    throw new Error(`${endpoint} returned invalid JSON`);
  }
  if (!response.ok) {
    const diagnostic = safeAPIErrorDiagnostic(body);
    throw new Error(
      `${endpoint} failed with HTTP ${response.status}${diagnostic ? ` (${diagnostic})` : ""}`,
    );
  }
  return { status: response.status, body };
}

function safeAPIErrorDiagnostic(body: unknown) {
  if (!isRecord(body)) return "";
  const data = isRecord(body.data) ? body.data : null;
  const error = isRecord(body.error) ? body.error : null;
  const values = [
    body.reason_code,
    data?.reason_code,
    error?.reason_code,
    body.message,
    error?.message,
  ];
  return values
    .filter(
      (value): value is string =>
        typeof value === "string" && value.trim().length > 0,
    )
    .map((value) => value.trim().slice(0, 160))
    .filter((value, index, all) => all.indexOf(value) === index)
    .join(": ");
}

function inventoryIDs(
  body: unknown,
  collection: "servers" | "services",
  enveloped: boolean,
) {
  const root = requireRecord(body, `${collection} response`);
  const payload = enveloped
    ? requireRecord(root.data, `${collection} data envelope`)
    : root;
  if (!Array.isArray(payload[collection])) {
    throw new Error(`${collection} response omitted ${collection} array`);
  }
  if (
    typeof payload.observed_at !== "string" ||
    typeof payload.inventory_revision !== "number" ||
    !isRecord(payload.freshness)
  ) {
    throw new Error(`${collection} response has an invalid inventory shape`);
  }
  const ids = payload[collection].map((item, index) => {
    const record = requireRecord(item, `${collection}[${index}]`);
    const id = String(record.id ?? "").trim();
    if (!id) throw new Error(`${collection}[${index}] omitted id`);
    return id;
  });
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${collection} response contains duplicate ids`);
  }
  return ids.sort();
}

function canonicalRows(
  body: unknown,
  collection: "servers" | "services",
): Record<string, any>[] {
  const root = requireRecord(body, `${collection} canonical response`);
  if (!Array.isArray(root.data)) {
    throw new Error(`${collection} canonical response omitted data array`);
  }
  return root.data.map((item, index) =>
    requireRecord(item, `${collection}[${index}]`),
  );
}

function canonicalRowIDs(
  rows: Record<string, any>[],
  collection: "servers" | "services",
): string[] {
  const ids = rows.map((row, index) => {
    const id = String(row.id ?? "").trim();
    if (!id) throw new Error(`${collection}[${index}] omitted id`);
    return id;
  });
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${collection} canonical response contains duplicate ids`);
  }
  return ids.sort();
}

function uiStackIDs(body: unknown) {
  const root = requireRecord(body, "stacks UI response");
  if (!Array.isArray(root.data)) {
    throw new Error("stacks UI response omitted data array");
  }
  return uniqueUIIDs(root.data, "stacks");
}

function uiRegistryServiceIDs(body: unknown) {
  const root = requireRecord(body, "services UI response");
  const data = requireRecord(root.data, "services UI data envelope");
  for (const field of ["catalog", "stacks", "servers", "services"]) {
    if (!Array.isArray(data[field])) {
      throw new Error(`services UI response omitted ${field} array`);
    }
  }
  return uniqueUIIDs(data.services, "registry services");
}

function uniqueUIIDs(items: unknown[], label: string) {
  const ids = items.map((item, index) => {
    const record = requireRecord(item, `${label}[${index}]`);
    const id = String(record.id ?? record.name ?? "").trim();
    if (!id) throw new Error(`${label}[${index}] omitted id/name`);
    return id;
  });
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${label} response contains duplicate ids`);
  }
  return ids.sort();
}

function requireRecord(value: unknown, label: string): Record<string, any> {
  if (!isRecord(value)) throw new Error(`${label} must be an object`);
  return value;
}

function isRecord(value: unknown): value is Record<string, any> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function assertCanonicalDemoPrincipal(body: unknown) {
  const whoami = requireRecord(body, "cloud whoami");
  const expectedTenant = String(
    process.env.TECHSTACK_DEMO_TENANT_ID ?? "",
  ).trim();
  const expectedSubject =
    String(process.env.TECHSTACK_DEMO_USER_IDS ?? "")
      .split(",")
      .map((value) => value.trim())
      .find(Boolean) ?? "";
  if (!expectedTenant || !expectedSubject) {
    throw new Error(
      "Exact inventory profile requires TECHSTACK_DEMO_TENANT_ID and TECHSTACK_DEMO_USER_IDS",
    );
  }
  expect(String(whoami.subject ?? "").trim()).toBe(expectedSubject);
  expect(String(whoami.tenantId ?? "").trim()).toBe(expectedTenant);
}

function isUIAPIURL(url: string, suffix: string) {
  const pathname = new URL(url).pathname.replace(/\/+$/, "");
  return (
    pathname.endsWith(`/api/v1/${suffix}`) ||
    pathname.endsWith(`/v1/techstack/${suffix}`)
  );
}

function assertCandidateUIResponseOrigin(responseURL: string) {
  const product = new URL(PRODUCT_BASE);
  if (!product.hostname.endsWith(".onrender.com")) return;
  expect(new URL(responseURL).origin).toBe(product.origin);
}

function isStackOperationsURL(url: string) {
  const pathname = new URL(url).pathname.replace(/\/+$/, "");
  return /\/(?:api\/v1|v1\/techstack)\/stacks\/[^/]+\/operations$/.test(
    pathname,
  );
}

function isHTTP2xx(value: number) {
  return value >= 200 && value < 300;
}

async function domInventoryIDs(
  page: Parameters<typeof authenticateRuntimeUser>[1],
  testID: string,
  attribute: string,
) {
  const ids = await page
    .getByTestId(testID)
    .evaluateAll(
      (elements, attributeName) =>
        elements.map((element) => element.getAttribute(attributeName) ?? ""),
      attribute,
    );
  if (ids.some((id) => !id.trim())) {
    throw new Error(`${testID} omitted ${attribute}`);
  }
  if (new Set(ids).size !== ids.length) {
    throw new Error(`${testID} contains duplicate ${attribute} values`);
  }
  return ids.sort();
}

function idsSha256(ids: string[]) {
  return createHash("sha256").update(ids.join("\n"), "utf8").digest("hex");
}

function sha256(value: Buffer) {
  return createHash("sha256").update(value).digest("hex");
}

function escapeRegExp(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function firstProductURL(...values: Array<string | undefined>) {
  for (const value of values) {
    const normalized = normalizeURL(value);
    if (normalized && isLiveHTTPSURL(normalized)) return normalized;
  }
  return "";
}

function normalizeURL(value: string | undefined) {
  const raw = value?.trim();
  if (!raw) return "";
  try {
    const parsed = new URL(raw);
    parsed.hash = "";
    parsed.search = "";
    return parsed.toString().replace(/\/+$/g, "");
  } catch {
    return "";
  }
}

function requireLiveProductURL(label: string, value: string) {
  const normalized = normalizeURL(value);
  if (!normalized) {
    throw new Error(`${label} URL is required for SaaS Runtime Auth smoke.`);
  }
  const parsed = new URL(normalized);
  if (parsed.protocol !== "https:" || isLoopbackURL(normalized)) {
    throw new Error(
      `${label} URL must be a real HTTPS product origin, got ${normalized}. Runtime Auth smoke does not run against localhost/self-hosted targets.`,
    );
  }
  return normalized;
}

function isLiveHTTPSURL(value: string) {
  const parsed = new URL(value);
  return parsed.protocol === "https:" && !isLoopbackURL(value);
}

function isLoopbackURL(value: string) {
  const host = new URL(value).hostname.toLowerCase();
  return host === "localhost" || host === "127.0.0.1" || host === "::1";
}

function runtimeAuthRolesFromEnv(value: string | undefined) {
  const requested = value
    ?.split(",")
    .map((item) => item.trim())
    .filter(Boolean);
  if (!requested?.length) return allRoles;

  const roles: RuntimeAuthRole[] = [];
  const invalid: string[] = [];
  for (const role of requested) {
    if (allRoles.includes(role as RuntimeAuthRole)) {
      const typedRole = role as RuntimeAuthRole;
      if (!roles.includes(typedRole)) roles.push(typedRole);
      continue;
    }
    invalid.push(role);
  }
  if (invalid.length > 0) {
    throw new Error(
      `TECHSTACK_RUNTIME_E2E_AUTH_ROLES contains unsupported roles: ${invalid.join(", ")}. Expected one or more of ${allRoles.join(", ")}.`,
    );
  }
  return roles;
}
