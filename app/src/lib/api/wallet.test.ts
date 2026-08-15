import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  createWalletItem,
  deleteWalletItem,
  getWalletItems,
  getWalletItemsByStack,
  requestWalletRevealProof,
  revealWalletItem,
  updateWalletItem,
} from "./wallet";

function requestPath(input: unknown): string {
  const raw = input instanceof Request ? input.url : String(input);
  return new URL(raw, "https://techstack.test").pathname;
}

describe("wallet API client", () => {
  beforeEach(() => {
    vi.restoreAllMocks();
    vi.stubGlobal("window", {
      location: { origin: "https://techstack.test" },
      localStorage: { getItem: () => null },
    });
  });

  it("lists wallet entries through the v1 BFF", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(
        JSON.stringify({
          data: {
            items: [{ id: "wallet-1", name: "Admin", kind: "password" }],
          },
        }),
        { status: 200, headers: { "content-type": "application/json" } },
      ),
    );

    const items = await getWalletItems();

    expect(requestPath(fetchMock.mock.calls[0][0])).toBe("/api/v1/wallet");
    expect(items[0]).toMatchObject({ id: "wallet-1", name: "Admin" });
  });

  it("filters wallet entries by stack through the v1 BFF", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(
      new Response(JSON.stringify({ data: [] }), {
        status: 200,
        headers: { "content-type": "application/json" },
      }),
    );

    await getWalletItemsByStack("stack-1");

    const called = new URL(String(fetchMock.mock.calls[0][0]));
    expect(called.pathname).toBe("/api/v1/wallet");
    expect(called.searchParams.get("stack_id")).toBe("stack-1");
  });

  it("writes wallet entries through backend-owned endpoints", async () => {
    const fetchMock = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation((input) => {
        const path = requestPath(input);
        if (path === "/api/v1/csrf") {
          return Promise.resolve(
            new Response(JSON.stringify({ token: "csrf" }), {
              status: 200,
              headers: { "content-type": "application/json" },
            }),
          );
        }
        return Promise.resolve(
          new Response(
            JSON.stringify({ data: { id: "wallet-1", name: "Admin" } }),
            { status: 200, headers: { "content-type": "application/json" } },
          ),
        );
      });

    await createWalletItem({ name: "Admin", kind: "password" });
    await updateWalletItem("wallet-1", { name: "Admin 2" });
    await deleteWalletItem("wallet-1");

    expect(fetchMock.mock.calls.map((call) => requestPath(call[0]))).toEqual([
      "/api/v1/csrf",
      "/api/v1/wallet",
      "/api/v1/wallet/wallet-1",
      "/api/v1/wallet/wallet-1",
    ]);
  });

  it("uses existing reveal endpoints through the v1 BFF", async () => {
    const fetchMock = vi.spyOn(globalThis, "fetch").mockImplementation(() =>
      Promise.resolve(
        new Response(
          JSON.stringify({
            data: {
              id: "wallet-1",
              reauth_timestamp: "123",
              reauth_signature: "sig",
              expires_at: "2026-05-28T10:00:00Z",
              secret: "revealed",
            },
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      ),
    );

    await requestWalletRevealProof("wallet-1", "copy");
    await revealWalletItem("wallet-1", {
      reason: "copy",
      reauthTimestamp: "123",
      reauthSignature: "sig",
    });

    expect(fetchMock.mock.calls.map((call) => requestPath(call[0]))).toEqual([
      "/api/v1/wallet/wallet-1/reauth-proof",
      "/api/v1/wallet/wallet-1/reveal",
    ]);
  });
});
