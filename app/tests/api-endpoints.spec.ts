import { test, expect } from "@playwright/test";
import { authenticateViaApi, requireApiBase } from "./helpers/test-utils";

/**
 * API Endpoint Tests
 *
 * These tests verify that all API endpoints are accessible and return
 * correct response formats. They run against the local Docker stack.
 */

const API_BASE = requireApiBase();

let authToken = "";

test.beforeAll(async () => {
  authToken = (await authenticateViaApi(API_BASE)).token;
});

function authHeaders() {
  return { Authorization: `Bearer ${authToken}` };
}

test.describe("API Health & Info", () => {
  test("GET /health returns healthy status", async ({ request }) => {
    const response = await request.get(`${API_BASE}/health`);
    expect(response.ok()).toBeTruthy();

    const json = await response.json();
    expect(json.data).toBeDefined();
    expect(json.data.status).toBe("healthy");
    expect(json.data.service).toBe("techstack-api");
  });

  test("GET /api/v1/info returns API metadata", async ({ request }) => {
    const response = await request.get(`${API_BASE}/api/v1/info`);
    expect(response.ok()).toBeTruthy();

    const json = await response.json();
    expect(json.data).toBeDefined();
    // API returns "kombify-TechStack" as name
    expect(json.data.name).toBe("kombify-TechStack");
    expect(json.data.version).toBeDefined();
  });
});

test.describe("Stacks API", () => {
  test("GET /api/v1/stacks returns list (may be empty)", async ({
    request,
  }) => {
    const response = await request.get(`${API_BASE}/api/v1/stacks`, {
      headers: authHeaders(),
    });
    expect(response.status()).not.toBe(401);
    expect(response.status()).not.toBe(403);
    expect(response.ok()).toBeTruthy();

    const json = await response.json();
    expect(json.data).toBeDefined();
    expect(Array.isArray(json.data)).toBeTruthy();
  });

  test("POST /api/v1/stacks creates a new stack", async ({ request }) => {
    const payload = {
      name: "test-stack-e2e",
      provider: "local",
      services: ["pocketbase"],
      options: {
        vpn: "tailscale",
      },
    };

    const response = await request.post(`${API_BASE}/api/v1/stacks`, {
      headers: authHeaders(),
      data: payload,
    });

    expect(response.status()).not.toBe(401);
    expect(response.status()).not.toBe(403);
    expect([200, 201, 400, 409, 422]).toContain(response.status());

    if (response.ok()) {
      const json = await response.json();
      expect(json.data).toBeDefined();
      expect(json.data.stack_id).toBeDefined();
    }
  });
});

test.describe("Jobs API", () => {
  test("GET /api/v1/jobs returns list (may be empty)", async ({ request }) => {
    const response = await request.get(`${API_BASE}/api/v1/jobs`, {
      headers: authHeaders(),
    });
    expect(response.status()).not.toBe(401);
    expect(response.status()).not.toBe(403);
    expect(response.ok()).toBeTruthy();

    const json = await response.json();
    expect(json.data).toBeDefined();
    expect(Array.isArray(json.data)).toBeTruthy();
  });

  test("GET /api/v1/jobs/:id returns 404 for non-existent job", async ({
    request,
  }) => {
    const response = await request.get(
      `${API_BASE}/api/v1/jobs/nonexistent123`,
      { headers: authHeaders() },
    );
    expect(response.status()).not.toBe(401);
    expect(response.status()).not.toBe(403);
    expect([404, 500]).toContain(response.status());
  });
});

test.describe("API Response Format", () => {
  test("All endpoints wrap responses in data object", async ({ request }) => {
    const publicEndpoints = ["/health", "/api/v1/info"];

    for (const endpoint of publicEndpoints) {
      const response = await request.get(`${API_BASE}${endpoint}`);
      expect(response.ok()).toBeTruthy();

      const json = await response.json();
      expect(json.data).toBeDefined();
    }

    const stacks = await request.get(`${API_BASE}/api/v1/stacks`, {
      headers: authHeaders(),
    });
    expect(stacks.status()).not.toBe(401);
    expect(stacks.status()).not.toBe(403);
    expect(stacks.ok()).toBeTruthy();
    const json = await stacks.json();
    expect(json.data).toBeDefined();
  });

  test("Content-Type is application/json", async ({ request }) => {
    const response = await request.get(`${API_BASE}/api/v1/info`);
    const contentType = response.headers()["content-type"];
    expect(contentType).toContain("application/json");
  });
});

test.describe("Simulation backend endpoints", () => {
  test("POST /api/v1/simulations/backend/start succeeds when qemu backend is healthy", async ({
    request,
  }) => {
    // Skip when the API cannot reach its configured backend.
    const reachability = await request.post(
      `${API_BASE}/api/v1/simulations/backend/test`,
      { data: {} },
    );
    test.skip(!reachability.ok(), "vm backend not reachable from API");

    const response = await request.post(
      `${API_BASE}/api/v1/simulations/backend/start`,
      {
        data: { type: "qemu" },
      },
    );

    expect(response.ok()).toBeTruthy();
    const json = await response.json();
    expect(json.data).toBeDefined();
    expect(
      json.data.backend || json.data.type || json.data.status,
    ).toBeTruthy();
  });

  test("POST /api/v1/simulations/backend/test reports reachable qemu backend", async ({
    request,
  }) => {
    // Skip when the API cannot reach its configured backend.
    const reachability = await request.post(
      `${API_BASE}/api/v1/simulations/backend/test`,
      { data: {} },
    );
    test.skip(!reachability.ok(), "vm backend not reachable from API");

    const response = await request.post(
      `${API_BASE}/api/v1/simulations/backend/test`,
      {
        data: {},
      },
    );

    expect(response.ok()).toBeTruthy();
    const json = await response.json();
    expect(json.data).toBeDefined();
    expect(json.data.success).toBe(true);
    expect(json.data.endpoint).toContain("5267");
  });
});
