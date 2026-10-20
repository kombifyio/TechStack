import { expect, test } from "@playwright/test";
import { authenticateRuntimeUser, authorizedFetch, captureGatewayApiSession } from "./runtime-auth";

const productBase = (process.env.TECHSTACK_RUNTIME_E2E_PRODUCT_URL ?? process.env.PLAYWRIGHT_BASE_URL ?? "https://techstack.kombify.io").replace(/\/+$/, "");

test("new Marketplace intent hosts Files on Coolify with its required TLS policy", async ({ page }) => {
  await authenticateRuntimeUser("cloud", page, { productBase, apiBase: productBase });
  const session = await captureGatewayApiSession(page);
  // Preview executes the running product's real pinned release projection and
  // validator; it does not create a stack, lease or application.
  const response = await authorizedFetch(session.token, session.apiBase + "/wizard/preview", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ intent: {
      schema: "techstack.wizard-intent/v1", run_kind: "first-run",
      name: "platform-selection", goals: ["marketplace", "files"],
      kit_assignment: { mode: "found", kit_slug: "basement-kit" },
    } }),
  });
  expect(response.ok, "the running product must admit the platform preview").toBeTruthy();
  const body = await response.json();
  const preview = body.data ?? body;
  expect(preview.valid, preview.validate_error).toBe(true);
  expect(preview.unmapped_goals).toEqual([]);
  expect(preview.spec.workloads.marketplace.alternative).toBe("coolify");
  expect(preview.spec.workloads.files.runtimeAdapterRef).toBe("coolify");
  expect(preview.spec.network.tls.minVersion).toBe("TLS1.3");
});
