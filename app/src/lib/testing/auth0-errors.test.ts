import { describe, expect, it } from "vitest";

import { AUTH0_FORM_ERROR_PATTERN, compactAuth0Error } from "./auth0-errors";

describe("Auth0 error classification", () => {
  it("recognizes a callback mismatch before credentials are entered", () => {
    expect(
      AUTH0_FORM_ERROR_PATTERN.test(
        "Callback URL mismatch. The provided redirect_uri is not in the list of allowed callback URLs.",
      ),
    ).toBe(true);
  });

  it("compacts Auth0 text without returning an unbounded page body", () => {
    expect(compactAuth0Error("  Oops!\n\nCallback URL mismatch.  ")).toBe(
      "Oops! Callback URL mismatch.",
    );
    expect(compactAuth0Error("x".repeat(300))).toHaveLength(240);
  });
});
