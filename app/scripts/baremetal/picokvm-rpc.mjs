#!/usr/bin/env node

import { chromium } from "@playwright/test";
import { pathToFileURL } from "node:url";

const DEFAULT_TIMEOUT_MS = 20_000;

export function parseArgs(argv, env = process.env) {
  const options = {
    endpoint: env.PICOKVM_ENDPOINT ?? "",
    method: "",
    params: {},
    sequence: null,
    timeoutMs: DEFAULT_TIMEOUT_MS,
  };

  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    switch (argument) {
      case "--endpoint":
        options.endpoint = argv[++index] ?? "";
        break;
      case "--method":
        options.method = argv[++index] ?? "";
        break;
      case "--params":
        options.params = JSON.parse(argv[++index] ?? "{}");
        break;
      case "--sequence":
        options.sequence = JSON.parse(argv[++index] ?? "[]");
        break;
      case "--timeout-ms":
        options.timeoutMs = Number.parseInt(argv[++index] ?? "", 10);
        break;
      default:
        throw new Error(`unknown argument: ${argument}`);
    }
  }

  if (!options.endpoint) {
    throw new Error("--endpoint or PICOKVM_ENDPOINT is required");
  }
  const endpoint = new URL(options.endpoint);
  if (endpoint.protocol !== "http:" && endpoint.protocol !== "https:") {
    throw new Error("PicoKVM endpoint must use HTTP or HTTPS");
  }
  options.endpoint = endpoint.toString();

  if (!options.sequence && !/^[A-Za-z][A-Za-z0-9]*$/.test(options.method)) {
    throw new Error("--method must be a JSON-RPC method name");
  }
  if (options.sequence) {
    if (!Array.isArray(options.sequence) || options.sequence.length === 0) {
      throw new Error("--sequence must decode to a non-empty JSON array");
    }
    for (const call of options.sequence) {
      if (!call || !/^[A-Za-z][A-Za-z0-9]*$/.test(call.method ?? "")) {
        throw new Error("every sequence item must have a JSON-RPC method name");
      }
      call.params ??= {};
      call.delayMs ??= 0;
      if (
        !Number.isInteger(call.delayMs) ||
        call.delayMs < 0 ||
        call.delayMs > 30_000
      ) {
        throw new Error("sequence delayMs must be between 0 and 30000");
      }
    }
  }
  if (
    options.params === null ||
    Array.isArray(options.params) ||
    typeof options.params !== "object"
  ) {
    throw new Error("--params must decode to a JSON object");
  }
  if (
    !Number.isInteger(options.timeoutMs) ||
    options.timeoutMs < 1_000 ||
    options.timeoutMs > 60_000
  ) {
    throw new Error("--timeout-ms must be between 1000 and 60000");
  }

  return options;
}

async function captureRpcDataChannel(page) {
  await page.addInitScript(() => {
    window.__kombifyKvmChannels = {};
    const originalCreateDataChannel =
      RTCPeerConnection.prototype.createDataChannel;
    RTCPeerConnection.prototype.createDataChannel = function createDataChannel(
      label,
      options,
    ) {
      const channel = originalCreateDataChannel.call(this, label, options);
      window.__kombifyKvmChannels[label] = channel;
      return channel;
    };
  });
}

export async function callPicoKvmRpc({
  endpoint,
  method,
  params,
  sequence,
  timeoutMs,
}) {
  const browser = await chromium.launch({ headless: true });
  const page = await browser.newPage();

  try {
    await captureRpcDataChannel(page);
    await page.goto(endpoint, {
      waitUntil: "domcontentloaded",
      timeout: Math.min(timeoutMs, 15_000),
    });
    await page.waitForFunction(
      () => window.__kombifyKvmChannels?.rpc?.readyState === "open",
      null,
      { timeout: timeoutMs },
    );

    return await page.evaluate(
      async ({ calls, rpcTimeoutMs }) => {
        const invoke = ({ method: rpcMethod, params: rpcParams }) =>
          new Promise((resolve, reject) => {
            const channel = window.__kombifyKvmChannels.rpc;
            const id = `kombify-${Date.now()}-${Math.random().toString(36).slice(2)}`;
            let settled = false;

            const finish = (callback) => {
              if (settled) return;
              settled = true;
              clearTimeout(timer);
              channel.removeEventListener("message", handleMessage);
              callback();
            };
            const handleMessage = (event) => {
              try {
                const response = JSON.parse(event.data);
                if (response.id !== id) return;
                if (response.error) {
                  finish(() =>
                    reject(
                      new Error(response.error.data ?? response.error.message),
                    ),
                  );
                  return;
                }
                finish(() => resolve(response.result));
              } catch {
                // Ignore non-JSON WebRTC events and responses for the device UI.
              }
            };
            const timer = setTimeout(
              () =>
                finish(() => reject(new Error(`RPC timeout for ${rpcMethod}`))),
              rpcTimeoutMs,
            );

            channel.addEventListener("message", handleMessage);
            channel.send(
              JSON.stringify({
                jsonrpc: "2.0",
                method: rpcMethod,
                params: rpcParams,
                id,
              }),
            );
          });
        const results = [];
        for (const call of calls) {
          if (call.delayMs)
            await new Promise((resolve) => setTimeout(resolve, call.delayMs));
          results.push(await invoke(call));
        }
        return results.length === 1 ? results[0] : results;
      },
      {
        calls: sequence ?? [{ method, params, delayMs: 0 }],
        rpcTimeoutMs: timeoutMs,
      },
    );
  } finally {
    await browser.close();
  }
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const result = await callPicoKvmRpc(options);
  process.stdout.write(
    `${JSON.stringify({ ok: true, method: options.method || "sequence", result })}\n`,
  );
}

if (
  process.argv[1] &&
  import.meta.url === pathToFileURL(process.argv[1]).href
) {
  main().catch((error) => {
    process.stderr.write(
      `${JSON.stringify({ ok: false, error: error.message })}\n`,
    );
    process.exitCode = 1;
  });
}
