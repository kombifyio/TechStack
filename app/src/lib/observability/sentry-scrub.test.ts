import { describe, expect, it } from "vitest";
import { sanitizeSentryText, scrubSentryEvent } from "./sentry-scrub";

describe("Sentry event scrubbing", () => {
  it("redacts secrets and private targets from custom context", () => {
    const event = scrubSentryEvent({
      message: "Bearer abcdefghijklmnopqrstuvwxyz",
      request: {
        url: "https://techstack.kombify.io/stacks?token=hidden",
        data: { password: "hidden" },
        headers: { Authorization: "Bearer hidden", "X-Request-ID": "req-1" },
      },
      contexts: {
        job: {
          api_token: "top-secret",
          output:
            "connect https://root:pass@10.0.0.4/admin?token=hidden password=supersecret",
          request_id: "req-1",
        },
      },
    });

    const encoded = JSON.stringify(event);
    expect(encoded).not.toContain("top-secret");
    expect(encoded).not.toContain("10.0.0.4");
    expect(encoded).not.toContain("supersecret");
    expect(event.request.url).toBe("https://techstack.kombify.io/stacks");
    expect(event.contexts.job.request_id).toBe("req-1");
  });

  it("retains reason codes while bounding summaries", () => {
    expect(sanitizeSentryText("reason=server_offline")).toContain(
      "server_offline",
    );
    expect(sanitizeSentryText("x".repeat(3_000))).toHaveLength(2_000);
  });
});
