export const LOCAL_DEMO_ANCHOR_PROFILE = "local-demo-anchor";

export interface RuntimeInventoryServiceExpectation {
  key: string;
  id: string;
  url: string;
  health: "healthy" | "reachable";
}

export interface RuntimeInventoryExpectation {
  profile: typeof LOCAL_DEMO_ANCHOR_PROFILE;
  server: {
    id: string;
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
  {
    key: "base",
    id: "service_dab7e8c1a39876fab631f848",
    url: "http://base.home.localhost",
    health: "healthy",
  },
  {
    key: "home",
    id: "service_efd223188d464c3fbb4ecedd",
    url: "http://home.home.localhost",
    health: "reachable",
  },
  {
    key: "id",
    id: "service_f890052679b10856421a0da9",
    url: "http://id.home.localhost",
    health: "reachable",
  },
  {
    key: "auth",
    id: "service_f4e4047891f15cbde7049e81",
    url: "http://auth.home.localhost",
    health: "reachable",
  },
  {
    key: "kuma",
    id: "service_5835ee5520c07dbb47b381bf",
    url: "http://kuma.home.localhost",
    health: "reachable",
  },
  {
    key: "whoami",
    id: "service_464ad6c8fdc09fe86681ed49",
    url: "http://whoami.home.localhost",
    health: "reachable",
  },
  {
    key: "vault",
    id: "service_3c7dbafe175ee4f7e080a897",
    url: "http://vault.home.localhost",
    health: "reachable",
  },
  {
    key: "photos",
    id: "service_ce38f692813891aa3debc763",
    url: "http://photos.home.localhost",
    health: "reachable",
  },
  {
    key: "files",
    id: "service_6dd57908432b0432a4904df7",
    url: "http://files.home.localhost/stackkit/files/session",
    health: "reachable",
  },
  {
    key: "coolify",
    id: "service_b2db4dc8ba989e036867c460",
    url: "http://coolify.home.localhost",
    health: "reachable",
  },
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
      id: "proxmox-vm-200-demo-anchor",
      domain: "home.localhost",
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
  assertExactStrings(
    servers.map((server, index) =>
      requiredString(server.id, `servers[${index}].id`),
    ),
    [expectation.server.id],
    `${expectation.profile} REST server ids`,
  );

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

  const expectedServiceIds = expectation.services.map((service) => service.id);
  assertExactStrings(
    services.map((service, index) =>
      requiredString(service.id, `services[${index}].id`),
    ),
    expectedServiceIds,
    `${expectation.profile} REST service ids`,
  );
  const servicesById = new Map(
    services.map((service) => [
      requiredString(service.id, "service.id"),
      service,
    ]),
  );
  const semanticServices: RuntimeInventoryAssertionResult["semantic"]["services"] =
    [];
  for (const expected of expectation.services) {
    const service = servicesById.get(expected.id);
    invariant(service, `services omitted expected id ${expected.id}`);
    assertEqual(
      service.server_id,
      expectation.server.id,
      `${expected.id}.server_id`,
    );
    assertEqual(service.key, expected.key, `${expected.id}.key`);
    assertEqual(
      requiredRecord(service.health, `${expected.id}.health`).state,
      expected.health,
      `${expected.id}.health.state`,
    );
    const links = requiredArray(service.links, `${expected.id}.links`);
    invariant(
      links.length === 1,
      `${expected.id}.links length = ${links.length}, expected 1`,
    );
    const link = requiredRecord(links[0], `${expected.id}.links[0]`);
    assertEqual(link.mode, "direct", `${expected.id}.links[0].mode`);
    assertExactStrings(
      links.map((candidate, index) =>
        requiredString(
          requiredRecord(candidate, `${expected.id}.links[${index}]`).url,
          `${expected.id}.links[${index}].url`,
        ),
      ),
      [expected.url],
      `${expected.id} link urls`,
    );
    assertEqual(service.source, "stackkits-inventory", `${expected.id}.source`);
    assertEqual(
      requiredRecord(service.stackkit, `${expected.id}.stackkit`).version,
      expectation.server.version,
      `${expected.id}.stackkit.version`,
    );
    assertPositiveInventoryRevision(
      service.inventory_revision,
      `${expected.id}.inventory_revision`,
    );
    assertFreshInventoryEvidence(service.freshness, `${expected.id}.freshness`);
    semanticServices.push({
      id: expected.id,
      serverId: expectation.server.id,
      key: expected.key,
      health: expected.health,
      url: expected.url,
      mode: "direct",
      source: "stackkits-inventory",
      stackkitVersion: expectation.server.version,
    });
  }

  return {
    serverIds: [expectation.server.id],
    serviceIds: [...expectedServiceIds].sort(),
    semantic: {
      server: {
        id: expectation.server.id,
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
