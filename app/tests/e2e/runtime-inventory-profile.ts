export const LOCAL_DEMO_ANCHOR_PROFILE = "local-demo-anchor";

// Service and server ids are read back from the connected server at run time:
// a fresh Wizard connect creates new ids, so none is pinned here.
export interface RuntimeInventoryServiceExpectation {
  key: string;
  url: string;
  health: "healthy" | "reachable";
}

export interface RuntimeInventoryExpectation {
  profile: typeof LOCAL_DEMO_ANCHOR_PROFILE;
  server: {
    // The declared Runtime Inventory id of the protected local server.
    targetId: string;
    // The product server id: TECHSTACK_RUNTIME_E2E_INVENTORY_SERVER_ID, else
    // the one server the owner's inventory holds.
    id: string | null;
    domain: string;
    os: string;
    stackkit: string;
    version: string;
    variant: string;
    connection: "connected";
    lifecycle: "active";
    health: "healthy";
  };
  services: RuntimeInventoryServiceExpectation[];
}

export interface RuntimeInventoryAssertionResult {
  serverIds: string[];
  serviceIds: string[];
  semantic: {
    server: {
      id: string;
      privateIp: string;
      domain: string;
      os: string;
      stackkit: string;
      version: string;
      variant: string;
      connection: string;
      lifecycle: string;
      health: string;
    };
    services: Array<{
      id: string;
      serverId: string;
      key: string;
      health: string;
      url: string;
      mode: string;
      source: string;
      stackkitVersion: string;
    }>;
  };
}

const localBasementServices: RuntimeInventoryServiceExpectation[] = [
  { key: "base", url: "https://base.home", health: "healthy" },
  { key: "id", url: "https://id.home", health: "reachable" },
  { key: "auth", url: "https://auth.home", health: "reachable" },
];

export function runtimeInventoryExpectation(
  env: Record<string, string | undefined> = process.env,
): RuntimeInventoryExpectation | null {
  const profile = clean(env.TECHSTACK_RUNTIME_E2E_INVENTORY_PROFILE);
  if (!profile) return null;
  if (profile !== LOCAL_DEMO_ANCHOR_PROFILE) {
    throw new Error(
      `Unsupported TECHSTACK_RUNTIME_E2E_INVENTORY_PROFILE ${profile}; expected ${LOCAL_DEMO_ANCHOR_PROFILE}.`,
    );
  }
  return {
    profile: LOCAL_DEMO_ANCHOR_PROFILE,
    server: {
      targetId: "esprimo-local-demo-anchor",
      id: clean(env.TECHSTACK_RUNTIME_E2E_INVENTORY_SERVER_ID) || null,
      domain: "home",
      os: clean(env.TECHSTACK_RUNTIME_E2E_EXPECTED_OS) || "ubuntu",
      stackkit: "basement-kit",
      version: "5.0.0",
      variant: "bootstrapped",
      connection: "connected",
      lifecycle: "active",
      health: "healthy",
    },
    services: localBasementServices.map((service) => ({ ...service })),
  };
}

export function assertRuntimeInventoryExpectation(
  expectation: RuntimeInventoryExpectation,
  serverResponse: unknown,
  serviceResponse: unknown,
): RuntimeInventoryAssertionResult {
  const servers = inventoryItems(serverResponse, "servers");
  const services = inventoryItems(serviceResponse, "services");
  invariant(
    servers.length === 1,
    `${expectation.profile} expects exactly one server, got ${servers.length}`,
  );
  const serverId = requiredString(servers[0].id, "servers[0].id");
  if (expectation.server.id) {
    assertEqual(serverId, expectation.server.id, `${expectation.profile} REST server id`);
  }

  const server = servers[0];
  const addresses = requiredRecord(server.addresses, "servers[0].addresses");
  const observedPrivateIPs = requiredStringArray(
    addresses.private_ips,
    "servers[0].addresses.private_ips",
  );
  invariant(
    observedPrivateIPs.length > 0,
    "servers[0].addresses.private_ips must contain a current Guard observation",
  );
  assertIncludes(
    requiredStringArray(addresses.domains, "servers[0].addresses.domains"),
    expectation.server.domain,
    "servers[0].addresses.domains",
  );
  const platform = requiredRecord(server.platform, "servers[0].platform");
  assertEqualFold(
    requiredString(platform.os, "servers[0].platform.os"),
    expectation.server.os,
    "servers[0].platform.os",
  );
  const stackkit = requiredRecord(server.stackkit, "servers[0].stackkit");
  assertEqual(
    stackkit.name,
    expectation.server.stackkit,
    "servers[0].stackkit.name",
  );
  assertEqual(
    stackkit.version,
    expectation.server.version,
    "servers[0].stackkit.version",
  );
  assertEqual(
    stackkit.variant,
    expectation.server.variant,
    "servers[0].stackkit.variant",
  );
  assertEqual(
    requiredRecord(server.health, "servers[0].health").state,
    expectation.server.health,
    "servers[0].health.state",
  );
  const connection = requiredString(
    requiredRecord(server.connection, "servers[0].connection").state,
    "servers[0].connection.state",
  );
  assertEqual(
    connection,
    expectation.server.connection,
    "servers[0].connection.state",
  );
  const lifecycle = requiredString(
    requiredRecord(server.lifecycle, "servers[0].lifecycle").state,
    "servers[0].lifecycle.state",
  );
  assertEqual(
    lifecycle,
    expectation.server.lifecycle,
    "servers[0].lifecycle.state",
  );
  assertPositiveInventoryRevision(
    server.inventory_revision,
    "servers[0].inventory_revision",
  );
  assertFreshInventoryEvidence(server.freshness, "servers[0].freshness");

  const observed = services.map((service, index) => ({
    id: requiredString(service.id, `services[${index}].id`),
    key: requiredString(service.key, `services[${index}].key`),
    service,
  }));
  for (const item of observed) {
    assertEqual(item.service.server_id, serverId, `${item.id}.server_id`);
  }
  invariant(
    new Set(observed.map((item) => item.key)).size === observed.length,
    `${expectation.profile} service keys must be unique`,
  );
  const servicesByKey = new Map(observed.map((item) => [item.key, item]));
  const observedServiceIds = observed.map((item) => item.id);
  const semanticServices: RuntimeInventoryAssertionResult["semantic"]["services"] =
    [];
  for (const expected of expectation.services) {
    const item = servicesByKey.get(expected.key);
    invariant(item, `services omitted expected key ${expected.key}`);
    const { service, id } = item;
    assertEqual(service.key, expected.key, `${id}.key`);
    assertEqual(
      requiredRecord(service.health, `${id}.health`).state,
      expected.health,
      `${id}.health.state`,
    );
    const links = requiredArray(service.links, `${id}.links`);
    invariant(
      links.length === 1,
      `${id}.links length = ${links.length}, expected 1`,
    );
    const link = requiredRecord(links[0], `${id}.links[0]`);
    assertEqual(link.mode, "direct", `${id}.links[0].mode`);
    assertExactStrings(
      links.map((candidate, index) =>
        requiredString(
          requiredRecord(candidate, `${id}.links[${index}]`).url,
          `${id}.links[${index}].url`,
        ),
      ),
      [expected.url],
      `${id} link urls`,
    );
    assertEqual(service.source, "stackkits-inventory", `${id}.source`);
    assertEqual(
      requiredRecord(service.stackkit, `${id}.stackkit`).version,
      expectation.server.version,
      `${id}.stackkit.version`,
    );
    assertPositiveInventoryRevision(
      service.inventory_revision,
      `${id}.inventory_revision`,
    );
    assertFreshInventoryEvidence(service.freshness, `${id}.freshness`);
    semanticServices.push({
      id,
      serverId,
      key: expected.key,
      health: expected.health,
      url: expected.url,
      mode: "direct",
      source: "stackkits-inventory",
      stackkitVersion: expectation.server.version,
    });
  }

  return {
    serverIds: [serverId],
    serviceIds: [...observedServiceIds].sort(),
    semantic: {
      server: {
        id: serverId,
        // The registry id is target authority. The address is current Guard
        // evidence only and may change without changing the protected target.
        privateIp: observedPrivateIPs[0],
        domain: expectation.server.domain,
        os: expectation.server.os.toLowerCase(),
        stackkit: expectation.server.stackkit,
        version: expectation.server.version,
        variant: expectation.server.variant,
        connection: expectation.server.connection,
        lifecycle: expectation.server.lifecycle,
        health: expectation.server.health,
      },
      services: semanticServices.sort((left, right) =>
        left.id.localeCompare(right.id),
      ),
    },
  };
}

function inventoryItems(
  response: unknown,
  collection: "servers" | "services",
): Record<string, unknown>[] {
  const root = requiredRecord(response, `${collection} response`);
  const payload =
    root.data === undefined
      ? root
      : requiredRecord(root.data, `${collection} data envelope`);
  return requiredArray(payload[collection], `${collection} collection`).map(
    (item, index) => requiredRecord(item, `${collection} collection[${index}]`),
  );
}

function requiredRecord(
  value: unknown,
  label: string,
): Record<string, unknown> {
  invariant(
    typeof value === "object" && value !== null && !Array.isArray(value),
    `${label} must be an object`,
  );
  return value as Record<string, unknown>;
}

function requiredArray(value: unknown, label: string): unknown[] {
  invariant(Array.isArray(value), `${label} must be an array`);
  return value;
}

function requiredString(value: unknown, label: string): string {
  const result = clean(value);
  invariant(result, `${label} must be a non-empty string`);
  return result;
}

function requiredStringArray(value: unknown, label: string): string[] {
  return requiredArray(value, label).map((item, index) =>
    requiredString(item, `${label}[${index}]`),
  );
}

function assertExactStrings(
  actual: string[],
  expected: string[],
  label: string,
) {
  const actualSorted = [...actual].sort();
  const expectedSorted = [...expected].sort();
  invariant(
    JSON.stringify(actualSorted) === JSON.stringify(expectedSorted),
    `${label} = ${JSON.stringify(actualSorted)}, expected ${JSON.stringify(expectedSorted)}`,
  );
}

function assertIncludes(actual: string[], expected: string, label: string) {
  invariant(
    actual.includes(expected),
    `${label} = ${JSON.stringify(actual)}, expected to include ${expected}`,
  );
}

function assertEqual(actual: unknown, expected: string, label: string) {
  invariant(
    clean(actual) === expected,
    `${label} = ${JSON.stringify(actual)}, expected ${JSON.stringify(expected)}`,
  );
}

function assertEqualFold(actual: string, expected: string, label: string) {
  invariant(
    actual.toLowerCase() === expected.toLowerCase(),
    `${label} = ${JSON.stringify(actual)}, expected ${JSON.stringify(expected)} (case-insensitive)`,
  );
}

function assertPositiveInventoryRevision(value: unknown, label: string) {
  invariant(
    typeof value === "number" && Number.isInteger(value) && value > 0,
    `${label} = ${JSON.stringify(value)}, expected a positive integer`,
  );
}

function assertFreshInventoryEvidence(value: unknown, label: string) {
  const state = requiredString(
    requiredRecord(value, label).state,
    `${label}.state`,
  ).toLowerCase();
  invariant(
    state === "fresh",
    `${label}.state = ${JSON.stringify(state)}, expected fresh (never stale/expired)`,
  );
}

function clean(value: unknown): string {
  return `${value ?? ""}`.trim();
}

function invariant(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}
