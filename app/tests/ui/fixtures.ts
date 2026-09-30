import { test as base, expect } from "@playwright/test";

/**
 * UI specs run through `scripts/run-playwright-nosetup.mjs`, which serves the
 * app against an in-process mock backend. That backend answers every request
 * no handler covers with 404 and `X-Kombify-Unmocked: 1`. This fixture
 * collects those requests and attaches them to the test result, so a failure
 * caused by a missing mock names the request instead of only the locator that
 * later timed out.
 */
export const test = base.extend({
  context: async ({ context }, use, testInfo) => {
    const unmocked = new Set<string>();
    context.on("response", (response) => {
      if (response.headers()["x-kombify-unmocked"] !== "1") return;
      const request = response.request();
      unmocked.add(`${request.method()} ${new URL(request.url()).pathname}`);
    });
    await use(context);
    if (unmocked.size > 0) {
      await testInfo.attach("unmocked-api-requests", {
        body: [...unmocked].join("\n"),
        contentType: "text/plain",
      });
    }
  },
});

export { expect };
