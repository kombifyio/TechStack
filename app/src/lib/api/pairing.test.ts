import { afterEach, describe, expect, it, vi } from "vitest";

import { renewNodePairing } from "./pairing";

describe("renewNodePairing", () => {
  afterEach(() => vi.restoreAllMocks());

  it("mints one ephemeral command from redacted job metadata", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            token: "kpt1.ephemeral-token",
            expires_at: "2026-09-06T12:15:00Z",
          },
        }),
        {
          status: 201,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    const result = await renewNodePairing("deployment-1", {
      server_provisioning_mode: "install-command",
      server_node_role: "worker",
      stackkit_foundation: "basement-kit",
      requested_services: null,
    });

    expect(result).toEqual({
      token: "kpt1.ephemeral-token",
      expires_at: "2026-09-06T12:15:00Z",
    });
    expect(fetchMock).toHaveBeenCalledTimes(1);

    const [input, init] = fetchMock.mock.calls[0];
    expect(new URL(String(input), "http://test.local").pathname).toBe(
      "/api/v1/trust/pairing-tokens",
    );
    expect(init?.method).toBe("POST");
    expect(JSON.parse(String(init?.body))).toEqual({
      stack_id: "deployment-1",
      name: "Node connection",
      server_provisioning_mode: "install-command",
      node_role: "worker",
      stackkit: "basement-kit",
      services: [],
    });
  });

  it("fails closed when the original Node metadata is incomplete", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch");

    await expect(
      renewNodePairing("deployment-1", {
        server_provisioning_mode: "install-command",
        server_node_role: "worker",
        requested_services: [],
      }),
    ).rejects.toThrow();

    expect(fetchMock).not.toHaveBeenCalled();
  });

  it("mints a connect-remote command with remote planning metadata", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            token: "kpt1.connect-remote-token",
            expires_at: "2026-09-06T12:15:00Z",
          },
        }),
        {
          status: 201,
          headers: { "content-type": "application/json" },
        },
      ),
    );

    const result = await renewNodePairing("deployment-1", {
      server_provisioning_mode: "connect-remote",
      server_node_role: "worker",
      stackkit_foundation: "basement-kit",
      requested_services: ["vault"],
      server_remote_host: "82.165.251.178",
      server_remote_port: 22,
      server_remote_user: "root",
      server_remote_auth_method: "password",
      server_remote_use_sudo: true,
    });

    expect(result.token).toBe("kpt1.connect-remote-token");
    expect(JSON.parse(String(fetchMock.mock.calls[0][1]?.body))).toMatchObject({
      stack_id: "deployment-1",
      server_provisioning_mode: "connect-remote",
      server_remote_host: "82.165.251.178",
      server_remote_port: 22,
      server_remote_user: "root",
      server_remote_auth_method: "password",
      server_remote_use_sudo: true,
    });
  });
});
