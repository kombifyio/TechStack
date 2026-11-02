import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, resolve } from "node:path";

import { expect, test } from "@playwright/test";

import { authenticateRuntimeUser } from "./runtime-auth";

// Cleanup proof of a failed B3 window run whose product removal did not pass
// (homelab-node-run-selfhosted.yml, job b3-cleanup-proof): the shared RIL-Pro
// owner reads that the run's stack is gone and its server retired, on Stable
// and on Early Access, because the run's evidence does not name its channel.
// This step only reads; the removal itself goes through the product.

const STACK_ID = process.env.TECHSTACK_B3_CLEANUP_STACK_ID?.trim() ?? "";
const SERVER_ID = process.env.TECHSTACK_B3_CLEANUP_SERVER_ID?.trim() ?? "";
const PRODUCT_BASE = liveProductURL(
  process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL ||
    process.env.TECHSTACK_E2E_PRODUCT_URL ||
    "https://techstack.kombify.io",
);
const API_BASE = liveProductURL(
  process.env.TECHSTACK_RUNTIME_E2E_API_URL || PRODUCT_BASE,
);
const CHANNELS = ["stable", "early-access"] as const;
const READBACK = resolve(
  process.env.RUNTIME_E2E_ARTIFACTS_DIR ?? "../artifacts/runtime-e2e",
  "b3-cleanup-readback.json",
);

test("a failed B3 run's stack is gone for its owner and its server is retired", async ({
  page,
}) => {
  test.skip(!STACK_ID || !SERVER_ID, "no B3 cleanup readback is requested");
  const auth = await authenticateRuntimeUser("cloud", page, {
    productBase: PRODUCT_BASE,
    apiBase: API_BASE,
    sessionCookieName:
      process.env.TECHSTACK_E2E_SESSION_COOKIE_NAME ?? "techstack_session",
    fixturePrefix: "TEST_RIL_PRO_USER",
  });
  const channels = [];
  for (const channel of CHANNELS) {
    const stack = await ownerJSON(
      auth.token,
      channel,
      `/api/v1/stacks/${encodeURIComponent(STACK_ID)}`,
    );
    const stacks = await ownerJSON(auth.token, channel, "/api/v1/stacks");
    const server = await ownerJSON(
      auth.token,
      channel,
      `/api/v1/servers/${encodeURIComponent(SERVER_ID)}`,
    );
    const data = server.body?.data ?? {};
    channels.push({
      channel,
      stack: {
        status: stack.status,
        error_code: stack.body?.error?.code ?? null,
        list_status: stacks.status,
        listed: Array.isArray(stacks.body?.data)
          ? stacks.body.data.some(
              (item: { id?: unknown }) => String(item?.id ?? "") === STACK_ID,
            )
          : null,
      },
      server: {
        status: server.status,
        error_code: server.body?.error?.code ?? null,
        lifecycle_state: data.lifecycle?.state ?? null,
        desired_state: data.lifecycle?.desired_state ?? null,
        connection_state: data.connection?.state ?? null,
      },
    });
  }
  const readback = {
    schema_version: "techstack.b3-cleanup-readback/v2",
    observed_at: new Date().toISOString(),
    api_base: API_BASE,
    owner: "TEST_RIL_PRO_USER",
    stack_id: STACK_ID,
    server_id: SERVER_ID,
    channels,
  };
  mkdirSync(dirname(READBACK), { recursive: true });
  writeFileSync(READBACK, `${JSON.stringify(readback, null, 2)}\n`);
  console.log(`[b3-cleanup-readback] ${JSON.stringify(readback)}`);

  for (const { channel, stack, server } of channels) {
    // A 404 counts only with the product's own not-found answer; a retained
    // server record must be retired with its Guard connection revoked.
    expect(stack, channel).toMatchObject({
      status: 404,
      error_code: "NOT_FOUND",
      list_status: 200,
      listed: false,
    });
    expect(
      (server.status === 404 && server.error_code === "NOT_FOUND") ||
        (server.status === 200 &&
          server.lifecycle_state === "decommissioned" &&
          server.desired_state === "absent" &&
          server.connection_state === "revoked"),
      `${channel} server ${JSON.stringify(server)}`,
    ).toBe(true);
  }
});

async function ownerJSON(
  token: string,
  channel: (typeof CHANNELS)[number],
  endpoint: string,
) {
  const response = await fetch(`${API_BASE}${endpoint}`, {
    headers: {
      Accept: "application/json",
      Authorization: `Bearer ${token}`,
      ...(channel === "early-access"
        ? { "x-kombify-release-channel": "early-access" }
        : {}),
    },
    signal: AbortSignal.timeout(30_000),
  });
  const text = await response.text();
  return { status: response.status, body: parseJSON(text) };
}

function parseJSON(text: string): any {
  try {
    return text ? JSON.parse(text) : null;
  } catch {
    return null;
  }
}

function liveProductURL(value: string): string {
  const parsed = new URL(value.trim());
  const host = parsed.hostname.toLowerCase();
  if (
    parsed.protocol !== "https:" ||
    host === "localhost" ||
    host === "127.0.0.1" ||
    host === "::1"
  ) {
    throw new Error(
      `The B3 cleanup readback must target the live product over HTTPS, got ${value}`,
    );
  }
  parsed.hash = "";
  parsed.search = "";
  return parsed.toString().replace(/\/+$/g, "");
}
