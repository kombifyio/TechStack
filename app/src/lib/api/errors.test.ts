import { describe, expect, it } from "vitest";
import { isCancelledRequestError, parseApiError } from "./errors";

describe("API error helpers", () => {
  it("maps status-bearing API errors into route-friendly flags", () => {
    const parsed = parseApiError(
      Object.assign(new Error("missing stack"), {
        status: 404,
        code: "not_found",
        details: { id: "stack-1" },
      }),
    );

    expect(parsed.message).toBe("missing stack");
    expect(parsed.status).toBe(404);
    expect(parsed.code).toBe("not_found");
    expect(parsed.isNotFound).toBe(true);
    expect(parsed.details).toEqual({ id: "stack-1" });
  });

  it("recognizes abort and cancellation errors", () => {
    expect(
      isCancelledRequestError(new DOMException("aborted", "AbortError")),
    ).toBe(true);
    expect(isCancelledRequestError(new Error("request autocancelled"))).toBe(
      true,
    );
  });
});
