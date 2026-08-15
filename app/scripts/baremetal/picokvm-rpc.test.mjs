import assert from "node:assert/strict";
import { describe, it } from "node:test";

import { parseArgs } from "./picokvm-rpc.mjs";

describe("PicoKVM RPC CLI", () => {
  it("requires an attachment control endpoint instead of a target host address", () => {
    assert.throws(
      () => parseArgs(["--method", "getVirtualMediaState"], {}),
      /PICOKVM_ENDPOINT is required/,
    );
  });

  it("parses an RPC request without any target IP field", () => {
    assert.deepEqual(
      parseArgs(
        [
          "--endpoint",
          "http://192.0.2.10",
          "--method",
          "mountWithHTTP",
          "--params",
          '{"url":"https://images.example/os.iso","mode":"CDROM"}',
        ],
        {},
      ),
      {
        endpoint: "http://192.0.2.10/",
        method: "mountWithHTTP",
        params: { url: "https://images.example/os.iso", mode: "CDROM" },
        sequence: null,
        timeoutMs: 20_000,
      },
    );
  });

  it("parses a timed HID sequence for a single KVM session", () => {
    const result = parseArgs(
      [
        "--endpoint",
        "http://192.0.2.10",
        "--sequence",
        '[{"method":"keyboardReport","params":{"modifier":0,"keys":[69]},"delayMs":250}]',
      ],
      {},
    );

    assert.equal(result.method, "");
    assert.deepEqual(result.sequence, [
      {
        method: "keyboardReport",
        params: { modifier: 0, keys: [69] },
        delayMs: 250,
      },
    ]);
  });

  it("rejects non-object parameter payloads", () => {
    assert.throws(
      () =>
        parseArgs(
          [
            "--endpoint",
            "http://192.0.2.10",
            "--method",
            "ping",
            "--params",
            "[]",
          ],
          {},
        ),
      /JSON object/,
    );
  });
});
