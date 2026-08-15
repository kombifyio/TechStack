import { expect, type Page, test } from "@playwright/test";

const registryPayload = {
  data: {
    catalog: [
      {
        id: "vaultwarden",
        display_name: "Vaultwarden",
        type: "auth",
        description: "Password vault managed by the StackKit gateway.",
        recommended: true,
        foundations: ["basement-kit"],
      },
    ],
    stacks: [
      {
        id: "stack-1",
        name: "Demo Stack",
        status: "running",
        stackkit_foundation: "basement-kit",
      },
    ],
    servers: [
      {
        id: "node-1",
        stack_id: "stack-1",
        name: "foundation-1",
        hostname: "foundation-1",
        role: "foundation",
        role_label: "Foundation Node",
        rollout_ready: true,
      },
    ],
    services: [
      {
        id: "svc-1",
        name: "custom_dashboard",
        display_name: "Custom Dashboard",
        type: "custom",
        status: "observed",
        management_state: "observed",
        move_allowed: false,
        stack_id: "stack-1",
        stack_name: "Demo Stack",
        server_id: "node-1",
        server_name: "foundation-1",
        port: 8088,
      },
    ],
    migration_available: false,
    migration_unavailable_reason:
      "Runtime service migration is not enabled until a real deploy, health verification, cutover, and source-drain executor is available.",
  },
};

function canonicalServicesPayload() {
  return {
    data: registryPayload.data.services.map((service) => ({
      id: service.id,
      stack_id: service.stack_id,
      server_id: service.server_id,
      target_kind: "server",
      placement: {
        evidence_ref: `runtime:${service.id}`,
        observed_at: "2026-05-18T10:00:00Z",
        freshness: { state: "recorded", age_seconds: 4 },
      },
      service_key: service.name,
      service_instance: "default",
      name: service.display_name || service.name,
      management_state: service.management_state,
      desired_state:
        service.management_state === "managed" ? "running" : "unknown",
      observed_state: service.status,
      inventory_revision: 1,
      health: {
        state: service.status,
        observed_at: "2026-05-18T10:00:00Z",
      },
      stackkit_version: "basement-kit@1.0.0",
      access: {},
      allowed_actions: [],
      source: "runtime",
      provenance: {},
      created_at: "2026-05-18T09:00:00Z",
      updated_at: "2026-05-18T10:00:00Z",
    })),
  };
}

/**
 * Canonical server read model (`GET /api/v1/servers`). The Add Server and
 * pairing flows read servers from here since the Wave 2 UI cutover; the legacy
 * `/api/v1/registry/servers` projection has no client left.
 */
function canonicalServer(overrides: Record<string, unknown> = {}) {
  return {
    id: "node-1",
    stack_id: "stack-1",
    name: "foundation-1",
    worker_id: "agent-1",
    lifecycle: { state: "active", desired_state: "running" },
    connection: {
      state: "connected",
      changed_at: "2026-05-18T10:00:00Z",
      last_heartbeat_at: "2026-05-18T10:00:00Z",
      staleness_seconds: 4,
    },
    health: { state: "healthy", observed_at: "2026-05-18T10:00:00Z" },
    channels: [],
    inventory_revision: 1,
    provider: {},
    environment_class: "local",
    offering: "self_owned_device",
    availability_owner: "customer",
    operations_owner: "customer",
    target_evidence: {
      ref: "guard:agent-1",
      observed_at: "2026-05-18T10:00:00Z",
      freshness: { state: "recorded", age_seconds: 4 },
    },
    mutations_allowed: true,
    created_at: "2026-05-18T09:00:00Z",
    updated_at: "2026-05-18T10:00:00Z",
    ...overrides,
  };
}

/** Canonical stack detail (`GET /api/v1/stacks/{id}`). */
const canonicalStackDetail = {
  id: "stack-1",
  name: "Demo Stack",
  provider: "local",
  state: "running",
  services: ["vaultwarden"],
  stackkit_catalog_ref: "basement-kit",
  catalog_ref: "basement-kit",
  created_at: "2026-05-18T10:00:00Z",
  updated_at: "2026-05-18T12:00:00Z",
};

async function expectServicesSurfaceReady(page: Page) {
  await expect(page.getByRole("tab", { name: "Applications" })).toBeVisible();
  await expect(page.getByTestId("new-service-button")).toBeVisible();
}

test.describe("Service Registry", () => {
  test.beforeEach(async ({ page }) => {
    page.on("pageerror", (error) => console.error("browser page error", error));
    await page.addInitScript(() => {
      const header = btoa(JSON.stringify({ alg: "none", typ: "JWT" }))
        .replaceAll("+", "-")
        .replaceAll("/", "_")
        .replaceAll("=", "");
      const payload = btoa(
        JSON.stringify({ exp: 1893456000, id: "owner-1", type: "authRecord" }),
      )
        .replaceAll("+", "-")
        .replaceAll("/", "_")
        .replaceAll("=", "");
      window.localStorage.setItem(
        "pocketbase_auth",
        JSON.stringify({
          token: `${header}.${payload}.signature`,
          model: {
            id: "owner-1",
            email: "owner@example.com",
            collectionName: "users",
          },
        }),
      );
    });
    await page.route("**/api/v1/auth/mode", async (route) => {
      await route.fulfill({
        json: {
          data: {
            mode: "local",
            deployment_mode: "self-hosted",
            is_first_run: false,
            allow_local_login: true,
          },
        },
      });
    });
    await page.route("**/api/v1/features", async (route) => {
      const managedRuntimeFeature = (key: string, name: string) => ({
        key,
        name,
        enabled: true,
        locked: false,
        requires_consent: false,
        has_consent: true,
        risk_level: "low",
        description: `${name} is enabled for this test tenant.`,
        category: "beta",
      });
      await route.fulfill({
        json: {
          data: {
            security: [],
            beta: [
              managedRuntimeFeature("monthly_runtime", "Monthly Runtime"),
              managedRuntimeFeature(
                "monthly_runtime_cloudkit",
                "Cloud Kit rollout",
              ),
              managedRuntimeFeature(
                "monthly_runtime_centron",
                "Centron Managed VPS",
              ),
              managedRuntimeFeature(
                "monthly_runtime_ionos",
                "IONOS Managed VPS",
              ),
            ],
            ux: [],
          },
        },
      });
    });
    await page.route("**/api/v1/registry/services", async (route) => {
      if (route.request().method() === "GET") {
        await route.fulfill({ json: registryPayload });
        return;
      }
      await route.fallback();
    });
    await page.route("**/api/v1/services", async (route) => {
      await route.fulfill({ json: canonicalServicesPayload() });
    });
    await page.route("**/api/v1/servers", async (route) => {
      await route.fulfill({ json: { data: [canonicalServer()] } });
    });
    await page.route("**/api/v1/stacks/stack-1", async (route) => {
      await route.fulfill({ json: { data: canonicalStackDetail } });
    });
    await page.route("**/api/v1/registry/services/import", async (route) => {
      const body = route.request().postDataJSON();
      await route.fulfill({
        json: {
          data: {
            service: {
              id: "svc-imported",
              name: body.name,
              display_name: body.display_name || body.name,
              type: body.type || "custom",
              status: "observed",
              management_state: "observed",
              stack_id: body.stack_id,
              stack_name: "Demo Stack",
              server_id: body.server_id,
              server_name: "foundation-1",
              port: body.port,
            },
          },
        },
      });
    });
    await page.route("**/api/v1/registry/services/attach", async (route) => {
      const body = route.request().postDataJSON();
      await route.fulfill({
        json: {
          data: {
            service: {
              id: "svc-vaultwarden",
              name: body.service_id,
              display_name: "Vaultwarden",
              type: "auth",
              status: "pending",
              management_state: "managed",
              stack_id: body.stack_id,
              stack_name: "Demo Stack",
              server_id: body.server_id,
              server_name: "foundation-1",
            },
          },
        },
      });
    });
    await page.route("**/api/v1/registry/services/migrate", async (route) => {
      const body = route.request().postDataJSON();
      const sourceService = {
        id: body.service_id,
        name: "custom_dashboard",
        display_name: "Custom Dashboard",
        type: "custom",
        status: "migrating",
        management_state: "managed",
        move_allowed: false,
        stack_id: "stack-1",
        stack_name: "Demo Stack",
        server_id: "node-1",
        server_name: "foundation-1",
        port: 8088,
      };
      const targetService = {
        id: "svc-new",
        name: "custom_dashboard",
        display_name: "Custom Dashboard",
        type: "custom",
        status: "pending_verification",
        management_state: "managed",
        move_allowed: false,
        stack_id: "stack-1",
        stack_name: "Demo Stack",
        server_id: body.target_server_id,
        server_name: "worker-1",
        port: 8088,
      };
      registryPayload.data.services = [
        sourceService,
        targetService,
        ...registryPayload.data.services.filter(
          (service) =>
            service.id !== body.service_id && service.id !== "svc-new",
        ),
      ];
      await route.fulfill({
        json: {
          data: {
            job_id: "job-migrate-1",
            source_service: sourceService,
            target_service: targetService,
          },
        },
      });
    });
    await page.route("**/api/v1/jobs/job-migrate-1", async (route) => {
      await route.fulfill({
        json: {
          data: {
            id: "job-migrate-1",
            type: "update",
            state: "completed",
            progress: 100,
            step: "service-migration",
            message: "Service migration handoff recorded",
          },
        },
      });
    });
    await page.route("**/api/v1/registry/services/verify", async (route) => {
      const body = route.request().postDataJSON();
      const service = {
        id: body.service_id,
        name: "custom_dashboard",
        display_name: "Custom Dashboard",
        type: "custom",
        status: "running",
        management_state: "managed",
        move_allowed: true,
        stack_id: "stack-1",
        stack_name: "Demo Stack",
        server_id: "node-2",
        server_name: "worker-1",
        port: 8088,
      };
      const archivedService = {
        id: "svc-1",
        name: "custom_dashboard",
        display_name: "Custom Dashboard",
        type: "custom",
        status: "archived",
        management_state: "managed",
        move_allowed: false,
        stack_id: "stack-1",
        stack_name: "Demo Stack",
        server_id: "node-1",
        server_name: "foundation-1",
        port: 8088,
      };
      registryPayload.data.services = [
        archivedService,
        service,
        ...registryPayload.data.services.filter(
          (item) => item.id !== archivedService.id && item.id !== service.id,
        ),
      ];
      await route.fulfill({
        json: {
          data: {
            service,
            archived_service: archivedService,
          },
        },
      });
    });
    await page.route("**/api/v1/registry/services/*", async (route) => {
      if (route.request().method() === "DELETE") {
        const url = route.request().url();
        const id = url.substring(url.lastIndexOf("/") + 1);
        await route.fulfill({
          json: {
            data: {
              message: "deleted",
              id,
            },
          },
        });
        return;
      }
      await route.fallback();
    });
    await page.route("**/trust/pairing-tokens", async (route) => {
      await route.fulfill({
        json: {
          data: {
            id: "pairing-token-1",
            token: "pair-token-123",
            expires_at: "2026-05-18T12:30:00Z",
            job_id: "job-add-server-1",
            stack_id: "stack-1",
          },
        },
      });
    });
    await page.route("**/api/v1/jobs/job-add-server-1", async (route) => {
      await route.fulfill({
        json: {
          data: {
            id: "job-add-server-1",
            type: "update",
            state: "completed",
            progress: 100,
            step: "create_spec",
            message: "Server registration prepared",
            stack_id: "stack-1",
            result: {
              creation_operation: "add-server",
              stack_id: "stack-1",
              registration_token: "pair-token-123",
              server_provisioning_mode: "install-command",
            },
          },
        },
      });
    });
  });

  test("opens New Application and imports unmanaged services as observed", async ({
    page,
  }) => {
    await page.goto("/services");

    await expectServicesSurfaceReady(page);
    await expect(page.getByTestId("runtime-service-card")).toContainText(
      "Custom Dashboard",
    );
    await expect(
      page.getByTestId("runtime-service-card").locator("article.kf-card"),
    ).toHaveCount(1);
    await expect(
      page
        .getByTestId("runtime-service-card")
        .getByText("Observed", { exact: true })
        .first(),
    ).toBeVisible();

    await page.getByTestId("new-service-button").click();
    await expect(page.getByTestId("new-service-dialog")).toBeVisible();
    await expect(page.getByTestId("catalog-service-vaultwarden")).toBeVisible();

    await page.getByRole("button", { name: "Import unmanaged" }).click();
    await page.getByTestId("unmanaged-service-name").fill("grafana");
    await page.getByTestId("import-observed-service").click();

    await expect(page.getByTestId("new-service-dialog")).toBeHidden();
    await expect(page.getByTestId("runtime-service-card")).toHaveCount(1);
  });

  test("shows stale service metadata without an Open action", async ({
    page,
  }) => {
    await page.unroute("**/api/v1/registry/services");
    await page.route("**/api/v1/registry/services", async (route) => {
      await route.fulfill({
        json: {
          data: {
            ...registryPayload.data,
            services: [
              {
                id: "svc-stale",
                name: "stale_dashboard",
                display_name: "Stale Dashboard",
                type: "custom",
                status: "unknown",
                management_state: "managed",
                stack_id: "stack-1",
                stack_name: "Demo Stack",
                server_id: "node-1",
                server_name: "foundation-1",
                url: "https://stale.example.test",
              },
              {
                id: "svc-live",
                name: "live_dashboard",
                display_name: "Live Dashboard",
                type: "custom",
                status: "reachable",
                management_state: "managed",
                stack_id: "stack-1",
                stack_name: "Demo Stack",
                server_id: "node-1",
                server_name: "foundation-1",
                url: "https://live.example.test",
              },
            ],
          },
        },
      });
    });

    await page.goto("/services");

    await expect(page.getByTestId("runtime-service-list")).toBeVisible();
    // Runtime inventory remains the only list owner. A management-only
    // registry variation must not create a second services grid.
    await expect(page.getByTestId("registry-service-card")).toHaveCount(0);
  });

  test("rolls out a catalog service to a selected server", async ({ page }) => {
    await page.goto("/services");

    await expectServicesSurfaceReady(page);
    await expect(page.getByTestId("runtime-service-card")).toContainText(
      "Custom Dashboard",
    );
    await page.getByTestId("new-service-button").click();
    await expect(page.getByTestId("new-service-dialog")).toBeVisible();
    await page.getByTestId("catalog-service-vaultwarden").click();
    await page.getByTestId("attach-catalog-service").click();

    await expect(page.getByTestId("new-service-dialog")).toBeHidden();
    await expect(page.getByTestId("runtime-service-list")).toBeVisible();
    await expect(page.getByTestId("registry-service-card")).toHaveCount(0);
  });

  test("Add Server connect-remote exposes the pairing command and waits for a real Guard projection", async ({
    page,
  }) => {
    const heartbeatAt = new Date("2026-07-22T08:00:00Z");
    await page.clock.install({ time: heartbeatAt });
    let guardProjected = false;
    let registryUnavailable = false;
    await page.unroute("**/api/v1/servers");
    await page.route("**/api/v1/servers", async (route) => {
      if (registryUnavailable) {
        await route.fulfill({
          status: 503,
          json: {
            error: {
              code: "registry_unavailable",
              message: "canonical server projection unavailable",
            },
          },
        });
        return;
      }
      await route.fulfill({
        json: {
          data: [
            canonicalServer(),
            ...(guardProjected
              ? [
                  // A real Guard heartbeat the sweeper persisted: connected +
                  // healthy + active. The page must not claim a connection
                  // from anything weaker than this.
                  canonicalServer({
                    id: "node-worker-2",
                    name: "worker-2",
                    worker_id: "guard-worker-2",
                    connection: {
                      state: "connected",
                      changed_at: heartbeatAt.toISOString(),
                      last_heartbeat_at: heartbeatAt.toISOString(),
                      staleness_seconds: 0,
                    },
                    health: {
                      state: "healthy",
                      observed_at: heartbeatAt.toISOString(),
                    },
                  }),
                ]
              : []),
          ],
        },
      });
    });
    await page.unroute("**/trust/pairing-tokens");
    await page.route("**/trust/pairing-tokens", async (route) => {
      const body = route.request().postDataJSON();
      expect(body.server_provisioning_mode).toBe("connect-remote");
      expect(body.server_remote_host).toBe("worker-2.local");
      await route.fulfill({
        json: {
          data: {
            id: "pairing-token-connect-remote",
            token: "pair-token-connect-remote",
            expires_at: "2099-07-18T12:30:00Z",
            job_id: "job-add-server-connect-remote",
            stack_id: "stack-1",
          },
        },
      });
    });
    await page.unroute("**/api/v1/jobs/job-add-server-1");
    await page.route(
      "**/api/v1/jobs/job-add-server-connect-remote",
      async (route) => {
        await route.fulfill({
          json: {
            data: {
              id: "job-add-server-connect-remote",
              type: "update",
              state: "completed",
              progress: 100,
              step: "create_spec",
              message: "Server registration prepared",
              stack_id: "stack-1",
              result: {
                creation_operation: "add-server",
                stack_id: "stack-1",
                registration_token: "pair-token-connect-remote",
                token_expires_at: "2099-07-18T12:30:00Z",
                server_provisioning_mode: "connect-remote",
                server_remote_host: "worker-2.local",
              },
            },
          },
        });
      },
    );

    await page.goto("/stacks/stack-1/servers/new");

    await expect(
      page.getByRole("heading", { name: "Add Server" }),
    ).toBeVisible();
    await page.getByTestId("wizard-next").click();
    await expect(page.getByTestId("easy-step-2")).toBeVisible();
    await expect(page.getByTestId("server-mode-install-command")).toBeVisible();
    await expect(page.getByTestId("server-mode-connect-remote")).toBeVisible();
    await expect(page.getByTestId("server-mode-kombify-cloud")).toBeVisible();
    await expect(
      page.getByTestId("stackkit-foundation-selector"),
    ).toBeVisible();
    await expect(page.getByTestId("server-role-worker")).toHaveAttribute(
      "aria-pressed",
      "true",
    );

    await page.getByTestId("server-mode-kombify-cloud").click();
    await expect(page.getByTestId("managed-provider-selector")).toBeVisible();
    await expect(page.getByTestId("add-server-review")).toHaveCount(0);

    await page.getByTestId("server-mode-connect-remote").click();
    await page.getByTestId("remote-server-host").fill("worker-2.local");
    await expect(page.getByTestId("remote-server-config")).toContainText(
      "Server host or IP",
    );

    await page.getByTestId("service-registry-option-vaultwarden").click();

    await page.getByTestId("wizard-next").click();
    await page.getByTestId("wizard-next").click();
    await page.getByTestId("wizard-next").click();
    await expect(
      page.getByTestId("owner-bootstrap-skipped-summary"),
    ).toBeVisible();
    await page.getByTestId("wizard-create").click();

    await expect(page).toHaveURL(/\/stacks\/creating\?.*operation=add-server/);
    await expect(
      page.getByRole("heading", { name: "Run the pairing command" }),
    ).toBeVisible();
    await expect(page.getByTestId("guard-pairing-status")).toContainText(
      "Not connected yet",
    );
    const pairingCommand = page.getByTestId("server-registration-command");
    await expect(pairingCommand).toContainText(
      'KOMBI_TOKEN="pair-token-connect-remote"',
    );
    await expect(pairingCommand).toContainText("TECHSTACK_AS_SERVICE=1");
    await expect(page.getByTestId("guard-connected-summary")).toHaveCount(0);

    await page
      .context()
      .grantPermissions(["clipboard-read", "clipboard-write"]);
    await page.getByTestId("copy-pairing-command").click();
    await expect(page.getByTestId("copy-pairing-command")).toContainText(
      "Pairing command copied",
    );
    const clipboard = await page.evaluate(() => navigator.clipboard.readText());
    expect(clipboard).toContain('KOMBI_TOKEN="pair-token-connect-remote"');

    guardProjected = true;
    await page.clock.fastForward(3_000);
    await expect(
      page.getByRole("heading", { name: "Server connected", level: 2 }),
    ).toBeVisible({ timeout: 10_000 });
    await expect(page.getByTestId("guard-connected-summary")).toContainText(
      "worker-2",
    );
    await expect(page.getByTestId("guard-connected-summary")).toContainText(
      "Guard heartbeat verified",
    );

    registryUnavailable = true;
    await page.clock.setFixedTime(new Date("2026-07-22T08:02:00Z"));
    await page.clock.fastForward(3_000);
    await expect(page.getByTestId("guard-connected-summary")).toHaveCount(0, {
      timeout: 10_000,
    });
    await expect(
      page.getByText("The server projection could not be checked.", {
        exact: false,
      }),
    ).toBeVisible();
  });

  test("Add Server preserves a submitted local registration for a managed stack", async ({
    page,
  }) => {
    const pageErrors: string[] = [];
    page.on("pageerror", (event) => pageErrors.push(event.message));

    await page.unroute("**/api/v1/stacks/stack-1");
    await page.route("**/api/v1/stacks/stack-1", async (route) => {
      await route.fulfill({
        json: {
          data: {
            ...canonicalStackDetail,
            stackkit_catalog_ref: "cloud-kit",
            catalog_ref: "cloud-kit",
            server_provisioning_mode: "kombify-cloud",
            server_mode: "monthly-runtime",
            runtime_lane: "monthly-runtime",
            lease_provider: "centron-managed",
            runtime_offering_id: "monthly-runtime-standard",
          },
        },
      });
    });
    await page.unroute("**/trust/pairing-tokens");
    await page.route("**/trust/pairing-tokens", async (route) => {
      const body = route.request().postDataJSON();
      expect(body.server_provisioning_mode).toBe("install-command");
      expect(body.node_role).toBe("foundation");
      expect(body.stackkit).toBe("basement-kit");
      await route.fulfill({
        json: {
          data: {
            id: "pairing-token-managed-stack-oneliner",
            token: "pair-token-managed-stack-oneliner",
            expires_at: "2099-07-18T12:30:00Z",
            job_id: "job-add-server-managed-stack-oneliner",
            stack_id: "stack-1",
          },
        },
      });
    });
    await page.route(
      "**/api/v1/jobs/job-add-server-managed-stack-oneliner",
      async (route) => {
        await route.fulfill({
          json: {
            data: {
              id: "job-add-server-managed-stack-oneliner",
              type: "update",
              state: "completed",
              progress: 100,
              step: "create_spec",
              message: "Server registration prepared",
              stack_id: "stack-1",
              result: {
                creation_operation: "add-server",
                stack_id: "stack-1",
                registration_token: "pair-token-managed-stack-oneliner",
                token_expires_at: "2099-07-18T12:30:00Z",
                server_provisioning_mode: "install-command",
              },
            },
          },
        });
      },
    );

    await page.goto("/stacks/stack-1/servers/new");
    await page.getByTestId("wizard-next").click();
    await page.getByTestId("server-mode-install-command").click();
    await page.getByTestId("foundation-basement-kit").click();
    await page.getByTestId("server-role-foundation").click();
    await page.getByTestId("wizard-next").click();
    await page.getByTestId("wizard-next").click();
    await page.getByTestId("wizard-next").click();
    await page.getByTestId("wizard-create").click();

    await expect(page).toHaveURL(/\/stacks\/creating\?.*operation=add-server/);
    await expect(
      page.getByRole("heading", { name: "Run the pairing command" }),
    ).toBeVisible();
    expect(pageErrors.join("\n")).not.toContain("effect_update_depth_exceeded");
  });

  test("Add Server reads the stack from the canonical stack detail route", async ({
    page,
  }) => {
    // No server aggregates at all: the page must still resolve the stack from
    // /api/v1/stacks/{id} rather than depending on a registry projection.
    await page.unroute("**/api/v1/servers");
    await page.route("**/api/v1/servers", async (route) => {
      await route.fulfill({ json: { data: [] } });
    });
    await page.unroute("**/api/v1/stacks/stack-1");
    await page.route("**/api/v1/stacks/stack-1", async (route) => {
      await route.fulfill({
        json: {
          data: {
            id: "stack-1",
            name: "Demo Stack",
            provider: "local",
            state: "running",
            services: ["vaultwarden"],
            stackkit_catalog_ref: "cloud-kit",
            server_provisioning_mode: "kombify-cloud",
            server_mode: "monthly-runtime",
            runtime_lane: "monthly-runtime",
            runtime_offering_id: "monthly-runtime-standard",
            lease_provider: "centron-managed",
            created_at: "2026-05-18T12:00:00Z",
            updated_at: "2026-05-18T12:00:00Z",
          },
        },
      });
    });

    await page.goto("/stacks/stack-1/servers/new");

    await expect(
      page.getByRole("heading", { name: "Add Server" }),
    ).toBeVisible();
    await expect(page.getByText("Stack not found.")).toHaveCount(0);
    await expect(page.getByTestId("wizard-next")).toBeVisible();
  });

  test("keeps placement visible but disables hollow runtime migration", async ({
    page,
  }) => {
    // Even a managed, running application must not become draggable while the
    // API reports that no real runtime migration executor is available.
    registryPayload.data.services[0].management_state = "managed";
    registryPayload.data.services[0].status = "running";
    // Legacy projections copied ordinary runtime health into migration_status.
    // The placement board must not turn that stale value into a fake job.
    Object.assign(registryPayload.data.services[0], {
      migration_status: "running",
    });
    registryPayload.data.services[0].move_allowed = true;

    // Add target server
    if (!registryPayload.data.servers.some((s) => s.id === "node-2")) {
      registryPayload.data.servers.push({
        id: "node-2",
        stack_id: "stack-1",
        name: "worker-1",
        hostname: "worker-1",
        role: "worker",
        role_label: "Worker Node",
        rollout_ready: true,
      });
    }

    await page.goto("/services?tab=servers");
    await expect(
      page.getByRole("tab", { name: "Placement Board" }),
    ).toHaveAttribute("aria-selected", "true");

    // Expect to see both servers columns
    await expect(page.getByText("foundation-1").first()).toBeVisible();
    await expect(page.getByText("worker-1").first()).toBeVisible();

    await expect(page.getByTestId("migration-unavailable")).toContainText(
      "Runtime migration is not enabled yet",
    );
    await expect(page.getByTestId("migration-unavailable")).toContainText(
      "real deploy, health verification, cutover, and source-drain executor",
    );

    const sourceCard = page
      .locator('[role="listitem"]')
      .filter({ hasText: "Custom Dashboard" });
    await expect(sourceCard).toHaveAttribute("draggable", "false");
    await expect(page.getByRole("button", { name: "Move" })).toHaveCount(0);
    await expect(page.getByText("Application Move")).toHaveCount(0);
    await expect(page.getByText("Migration job is active.")).toHaveCount(0);
    await expect(page.getByText("Runtime migration unavailable")).toBeVisible();
  });
});
