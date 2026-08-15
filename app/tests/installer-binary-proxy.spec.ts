import { expect, test } from "@playwright/test";

test("installer binary POST crosses the SvelteKit proxy as octet-stream", async ({
  request,
}) => {
  const response = await request.post("/api/v1/agent/binary/linux/x86_64", {
    headers: {
      Authorization: "Bearer installer-csrf-regression-token",
      "Content-Type": "application/octet-stream",
      Origin: "https://api.kombify.io",
    },
  });

  expect(response.status()).toBe(401);
  const body = await response.text();
  expect(body).not.toContain("Cross-site POST form submissions are forbidden");
  const firstPayload = JSON.parse(
    body.split(/\r?\n/).find((line) => line.trim().length > 0) ?? "{}",
  );
  expect(firstPayload).toMatchObject({
    error: "invalid pairing token",
    received_content_type: "application/octet-stream",
  });
});
