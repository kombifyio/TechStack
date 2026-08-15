import assert from "node:assert/strict";
import { mkdtemp, mkdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { afterEach, describe, it } from "node:test";

import { createCallbackServer } from "./callback-server.mjs";

const cleanup = [];

afterEach(async () => {
  await Promise.all(
    cleanup.splice(0).map((path) => rm(path, { force: true, recursive: true })),
  );
});

describe("bare-metal callback server", () => {
  it("serves a complete NoCloud seed and records installer evidence", async () => {
    const root = await mkdtemp(join(tmpdir(), "kombify-baremetal-test-"));
    cleanup.push(root);
    const runId = "bm-0123456789abcdef";
    const seed = join(root, "seed", runId);
    await mkdir(seed, { recursive: true });
    await writeFile(join(seed, "user-data"), "#cloud-config\n", "utf8");
    await writeFile(join(seed, "meta-data"), `instance-id: ${runId}\n`, "utf8");

    const server = createCallbackServer({ root });
    await new Promise((resolve) => server.listen(0, "127.0.0.1", resolve));
    const { port } = server.address();
    const base = `http://127.0.0.1:${port}`;

    try {
      assert.equal(
        (await fetch(`${base}/seed/${runId}/vendor-data`)).status,
        200,
      );
      assert.equal(
        (await fetch(`${base}/seed/${runId}/network-config`)).status,
        200,
      );
      assert.equal(
        (await fetch(`${base}/seed/${runId}/user-data`)).status,
        200,
      );

      const event = await fetch(`${base}/runs/${runId}/events`, {
        method: "POST",
        body: JSON.stringify({ event_type: "finish", result: "SUCCESS" }),
      });
      assert.equal(event.status, 202);

      const callback = await fetch(`${base}/runs/${runId}/callback`, {
        method: "POST",
        headers: {
          "content-type": "application/json",
          "x-kombify-run-token": runId,
        },
        body: JSON.stringify({
          runId,
          addresses: ["192.0.2.44"],
          interfaces: [],
        }),
      });
      assert.equal(callback.status, 202);
      assert.match(
        await readFile(join(root, "events.jsonl"), "utf8"),
        /SUCCESS/,
      );
      assert.match(
        await readFile(join(root, "callbacks.jsonl"), "utf8"),
        /192\.0\.2\.44/,
      );
    } finally {
      await new Promise((resolve, reject) =>
        server.close((error) => (error ? reject(error) : resolve())),
      );
    }
  });
});
