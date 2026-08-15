import { test, expect } from "@playwright/test";

/**
 * Unifier API E2E Tests
 *
 * Tests the /api/v1/unifier/* endpoints for spec validation,
 * unification, and tfvars generation.
 *
 * Prerequisites:
 *   - kombify-TechStack server running at a reachable URL
 *   - Run: go run ./cmd/techstack serve --dev
 */

const BASE_URL = process.env.TECHSTACK_URL;

if (!BASE_URL) {
  throw new Error(
    "TECHSTACK_URL must be set to run these tests (e.g. https://<your-host>:<port>).",
  );
}

// Valid test spec
const validSpec = {
  name: "e2e-test-stack",
  kit: "homelab-basic",
  nodes: [
    {
      name: "main-server",
      type: "main",
      provider: "local",
      ssh: {
        host: "192.168.1.100",
        port: 22,
        user: "ubuntu",
      },
    },
  ],
  services: [
    {
      name: "traefik",
      type: "reverse-proxy",
      node: "main-server",
    },
  ],
};

test.describe("Unifier API", () => {
  test.beforeAll(async ({ request }) => {
    // Verify server is running
    const health = await request.get(`${BASE_URL}/api/v1/health`);
    expect(health.ok()).toBeTruthy();
  });

  test.describe("POST /api/v1/unifier/validate", () => {
    test("should validate a correct spec", async ({ request }) => {
      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: validSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.ok()).toBeTruthy();
      const body = await response.json();
      expect(body.valid).toBe(true);
      expect(body.errors).toBeNull();
    });

    test("should reject spec without name", async ({ request }) => {
      const invalidSpec = { ...validSpec };
      delete (invalidSpec as any).name;

      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: invalidSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
      const body = await response.json();
      expect(body.valid).toBe(false);
      expect(body.details).toContain("name");
    });

    test("should reject spec without kit", async ({ request }) => {
      const invalidSpec = { ...validSpec };
      delete (invalidSpec as any).kit;

      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: invalidSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
      const body = await response.json();
      expect(body.valid).toBe(false);
    });

    test("should reject spec without nodes", async ({ request }) => {
      const invalidSpec = { ...validSpec, nodes: [] };

      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: invalidSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
    });

    test("should reject duplicate node names", async ({ request }) => {
      const invalidSpec = {
        ...validSpec,
        nodes: [
          { name: "server-1", type: "main", provider: "local" },
          { name: "server-1", type: "worker", provider: "local" },
        ],
      };

      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: invalidSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
      const body = await response.json();
      expect(body.details).toContain("duplicate");
    });

    test("should reject invalid node reference in service", async ({
      request,
    }) => {
      const invalidSpec = {
        ...validSpec,
        services: [
          { name: "traefik", type: "reverse-proxy", node: "nonexistent-node" },
        ],
      };

      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: invalidSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
      const body = await response.json();
      expect(body.details).toContain("unknown node");
    });

    test("should accept YAML content type", async ({ request }) => {
      const yamlSpec = `
name: yaml-e2e-test
kit: homelab-basic
nodes:
  - name: main-server
    type: main
    provider: local
    ssh:
      host: 10.0.0.1
      user: ubuntu
`;

      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: yamlSpec,
          headers: { "Content-Type": "application/x-yaml" },
        },
      );

      expect(response.ok()).toBeTruthy();
      const body = await response.json();
      expect(body.valid).toBe(true);
    });
  });

  test.describe("POST /api/v1/unifier/unify", () => {
    test("should unify a valid spec with defaults", async ({ request }) => {
      const response = await request.post(`${BASE_URL}/api/v1/unifier/unify`, {
        data: validSpec,
        headers: { "Content-Type": "application/json" },
      });

      expect(response.ok()).toBeTruthy();
      const body = await response.json();

      // Check resolved nodes
      expect(body.resolved_nodes).toBeDefined();
      expect(body.resolved_nodes.length).toBe(1);
      expect(body.resolved_nodes[0].name).toBe("main-server");

      // Check resolved services
      expect(body.resolved_services).toBeDefined();
      expect(body.resolved_services.length).toBe(1);
      expect(body.resolved_services[0].node).toBe("main-server");

      // Check resolved network with defaults
      expect(body.resolved_network).toBeDefined();
      expect(body.resolved_network.vpn_type).toBe("none");
      expect(body.resolved_network.domain).toBe("local");
    });

    test("should apply SSH user defaults by provider", async ({ request }) => {
      const hetznerSpec = {
        ...validSpec,
        nodes: [
          {
            name: "hetzner-server",
            type: "main",
            provider: "hetzner",
            ssh: { host: "1.2.3.4" },
          },
        ],
      };

      const response = await request.post(`${BASE_URL}/api/v1/unifier/unify`, {
        data: hetznerSpec,
        headers: { "Content-Type": "application/json" },
      });

      expect(response.ok()).toBeTruthy();
      const body = await response.json();

      // Hetzner default user should be 'root'
      expect(body.resolved_nodes[0].ssh.user).toBe("root");
    });

    test("should assign service to main node by default", async ({
      request,
    }) => {
      const specWithoutNode = {
        ...validSpec,
        services: [
          { name: "traefik", type: "reverse-proxy" }, // no node specified
        ],
      };

      const response = await request.post(`${BASE_URL}/api/v1/unifier/unify`, {
        data: specWithoutNode,
        headers: { "Content-Type": "application/json" },
      });

      expect(response.ok()).toBeTruthy();
      const body = await response.json();

      // Service should be assigned to the main node
      expect(body.resolved_services[0].node).toBe("main-server");
    });
  });

  test.describe("POST /api/v1/unifier/generate", () => {
    test("should generate valid tfvars.json", async ({ request }) => {
      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/generate`,
        {
          data: validSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.ok()).toBeTruthy();
      const body = await response.json();

      // Check tfvars structure
      expect(body.stack_name).toBe("e2e-test-stack");
      expect(body.stack_kit).toBe("homelab-basic");

      // Check nodes in tfvars format
      expect(body.nodes).toBeDefined();
      expect(body.nodes.length).toBe(1);
      expect(body.nodes[0].name).toBe("main-server");
      expect(body.nodes[0].host).toBe("192.168.1.100");
      expect(body.nodes[0].ssh_user).toBe("ubuntu");
      expect(body.nodes[0].ssh_port).toBe(22);

      // Check services in tfvars format
      expect(body.services).toBeDefined();
      expect(body.services.length).toBe(1);
      expect(body.services[0].name).toBe("traefik");
      expect(body.services[0].type).toBe("reverse-proxy");

      // Check network in tfvars format
      expect(body.network).toBeDefined();
      expect(body.network.vpn_type).toBe("none");
      expect(body.network.domain).toBe("local");
    });

    test("should reject invalid spec in generate", async ({ request }) => {
      const invalidSpec = { name: "test" }; // missing required fields

      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/generate`,
        {
          data: invalidSpec,
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
    });
  });

  test.describe("Error Handling", () => {
    test("should return proper error for empty body", async ({ request }) => {
      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: "",
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
    });

    test("should return proper error for malformed JSON", async ({
      request,
    }) => {
      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: "{ invalid json }",
          headers: { "Content-Type": "application/json" },
        },
      );

      expect(response.status()).toBe(400);
    });

    test("should return proper error for malformed YAML", async ({
      request,
    }) => {
      const response = await request.post(
        `${BASE_URL}/api/v1/unifier/validate`,
        {
          data: "invalid: yaml: content: [",
          headers: { "Content-Type": "application/x-yaml" },
        },
      );

      expect(response.status()).toBe(400);
    });
  });
});
