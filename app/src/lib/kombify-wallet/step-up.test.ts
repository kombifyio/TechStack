// @vitest-environment jsdom

import {
  afterEach,
  beforeEach,
  describe,
  expect,
  it,
  vi,
} from "vite-plus/test";
import { createStoreSimulator } from "../testing/wallet-store-simulator";

vi.mock("#lib/api/client.js", () => ({
  resolveGatewayBearer: async () => "access-token",
}));
vi.mock("#lib/auth/session-recovery.js", () => ({
  isGatewayLoginRedirecting: () => false,
}));

import { isStepUpRequired, revealItem } from "./wallet-api";
import { beginStepUp, resumeFromUrl } from "./step-up";

const OWNER = "auth0|wallet-owner-fixture";
const ITEM = "itm_abcdefghijklmnopqrstuvwxyz";
const PAGE = { pathname: "/wallet", search: "" };

// A Gateway that refuses a reveal until the owner has completed the step-up,
// then seals the value to the recipient key the browser sent.
function gateway() {
  const store = createStoreSimulator();
  const state = { fresh: false };
  const fetchStub = vi.fn(async (_url: string, init: RequestInit) => {
    if (!state.fresh)
      return new Response(
        JSON.stringify({
          reason_code: "wallet_step_up_required",
          retryable: true,
          step_up_max_age_seconds: 600,
        }),
        { status: 403 },
      );
    const asked = JSON.parse(String(init.body)) as {
      recipientKey: { kty: "EC"; crv: "P-256"; x: string; y: string };
      expiresAt: number;
    };
    const { thumbprint } = await import("../testing/wallet-store-simulator");
    const context = {
      custodyNamespace: "personal",
      ownerSubject: OWNER,
      vaultRef: "personal",
      itemVersion: "1",
      keyGeneration: "1",
      operation: "reveal",
      operationId: crypto.randomUUID(),
      audience: "urn:kombify:wallet:owner-session",
      recipientKeyThumbprint: thumbprint(asked.recipientKey),
      expiresAt: asked.expiresAt,
    };
    return new Response(
      JSON.stringify({
        operationId: context.operationId,
        itemId: ITEM,
        version: "1",
        jwe: store.sealReveal("s3cret", asked.recipientKey, context),
        context,
      }),
      { status: 200 },
    );
  });
  return { state, fetchStub };
}

beforeEach(() => sessionStorage.clear());
afterEach(() => vi.unstubAllGlobals());

describe("Wallet reveal step-up", () => {
  it("resumes a refused reveal after this browser's step-up, and nothing for a crafted link", async () => {
    const { state, fetchStub } = gateway();
    vi.stubGlobal("fetch", fetchStub);

    const refusal = await revealItem(ITEM, OWNER).catch((e: unknown) => e);
    expect(isStepUpRequired(refusal)).toBe(true);

    let returnTo = "";
    await beginStepUp(
      { action: "reveal", itemId: ITEM },
      async (target) => {
        returnTo = target;
        return true;
      },
      PAGE,
    );
    state.fresh = true; // the Auth0 second factor completed

    const back = new URL(returnTo, "https://techstack.test");
    const resume = resumeFromUrl(back);
    expect(resume).toEqual({ action: "reveal", itemId: ITEM });
    expect((await revealItem(resume!.itemId, OWNER)).value).toBe("s3cret");

    // The marker was one-shot: the same link again, or a link this browser
    // never started a step-up for, reopens nothing.
    expect(resumeFromUrl(back)).toBeNull();
    expect(
      resumeFromUrl(new URL(`/wallet?wallet_resume=reveal.${ITEM}`, back)),
    ).toBeNull();
  });
});
