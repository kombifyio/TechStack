import { afterEach, describe, expect, it, vi } from "vitest";
import type { Auth0Client } from "@auth0/auth0-spa-js";

import { __setAuth0ClientForTest, getGatewayToken } from "./gateway-auth";

afterEach(() => {
  __setAuth0ClientForTest(null);
});

function fakeClient(over: Partial<Auth0Client>): Auth0Client {
  return over as Auth0Client;
}

describe("getGatewayToken", () => {
  it("returns the silently-acquired audience token", async () => {
    const getTokenSilently = vi.fn().mockResolvedValue("tok_abc");
    __setAuth0ClientForTest(fakeClient({ getTokenSilently }));

    await expect(getGatewayToken()).resolves.toBe("tok_abc");
    expect(getTokenSilently).toHaveBeenCalledTimes(1);
  });

  it("throws (no anonymous downgrade) when silent auth fails", async () => {
    __setAuth0ClientForTest(
      fakeClient({
        getTokenSilently: vi
          .fn()
          .mockRejectedValue(new Error("login_required")),
      }),
    );

    await expect(getGatewayToken()).rejects.toThrow("login_required");
  });

  it("throws when the token comes back empty", async () => {
    __setAuth0ClientForTest(
      fakeClient({ getTokenSilently: vi.fn().mockResolvedValue("") }),
    );

    await expect(getGatewayToken()).rejects.toThrow("gateway_token_empty");
  });
});
