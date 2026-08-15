#!/usr/bin/env node

import { createServer } from "node:http";
import { createReadStream } from "node:fs";
import { appendFile, readFile, stat } from "node:fs/promises";
import { extname, join, normalize, resolve } from "node:path";

function parseArgs(argv) {
  const options = { bind: "0.0.0.0", port: 8765 };
  for (let index = 0; index < argv.length; index += 1) {
    const argument = argv[index];
    switch (argument) {
      case "--root":
        options.root = resolve(argv[++index] ?? "");
        break;
      case "--bind":
        options.bind = argv[++index] ?? "";
        break;
      case "--port":
        options.port = Number.parseInt(argv[++index] ?? "", 10);
        break;
      case "--iso":
        options.iso = resolve(argv[++index] ?? "");
        break;
      default:
        throw new Error(`unknown argument: ${argument}`);
    }
  }
  if (!options.root) throw new Error("--root is required");
  if (
    !Number.isInteger(options.port) ||
    options.port < 1 ||
    options.port > 65_535
  ) {
    throw new Error("--port must be a valid TCP port");
  }
  return options;
}

async function readBody(request) {
  const chunks = [];
  let size = 0;
  for await (const chunk of request) {
    size += chunk.length;
    if (size > 64 * 1024) throw new Error("callback body exceeds 64 KiB");
    chunks.push(chunk);
  }
  return Buffer.concat(chunks).toString("utf8");
}

export function createCallbackServer({ root, iso }) {
  return createServer(async (request, response) => {
    try {
      const url = new URL(request.url, "http://callback.invalid");
      await appendFile(
        join(root, "requests.jsonl"),
        `${JSON.stringify({
          at: new Date().toISOString(),
          method: request.method,
          path: url.pathname,
          remoteAddress: request.socket.remoteAddress,
        })}\n`,
        "utf8",
      );
      if (request.method === "GET" && url.pathname === "/health") {
        response.writeHead(200, { "content-type": "application/json" });
        response.end('{"ok":true}\n');
        return;
      }

      if (
        request.method === "GET" &&
        url.pathname === "/images/ubuntu-24.04.4-live-server-amd64.iso"
      ) {
        const isoPath = iso;
        if (!isoPath) {
          response.writeHead(404).end();
          return;
        }
        const file = await stat(isoPath);
        const range = request.headers.range?.match(/^bytes=(\d+)-(\d*)$/);
        const start = range ? Number.parseInt(range[1], 10) : 0;
        const end = range?.[2] ? Number.parseInt(range[2], 10) : file.size - 1;
        if (start < 0 || end < start || end >= file.size) {
          response
            .writeHead(416, { "content-range": `bytes */${file.size}` })
            .end();
          return;
        }
        await appendFile(
          join(root, "http-access.jsonl"),
          `${JSON.stringify({ at: new Date().toISOString(), path: url.pathname, start, end })}\n`,
          "utf8",
        );
        response.writeHead(range ? 206 : 200, {
          "accept-ranges": "bytes",
          "content-length": end - start + 1,
          "content-range": `bytes ${start}-${end}/${file.size}`,
          "content-type": "application/octet-stream",
          "cache-control": "no-store",
        });
        createReadStream(isoPath, { start, end }).pipe(response);
        return;
      }

      const seedMatch = url.pathname.match(
        /^\/seed\/([a-z0-9][a-z0-9-]{7,63})\/(user-data|meta-data|vendor-data|network-config)$/,
      );
      if (request.method === "GET" && seedMatch) {
        if (
          seedMatch[2] === "vendor-data" ||
          seedMatch[2] === "network-config"
        ) {
          response.writeHead(200, {
            "content-type": "text/plain",
            "cache-control": "no-store",
          });
          response.end("\n");
          return;
        }
        const path = normalize(join(root, "seed", seedMatch[1], seedMatch[2]));
        if (!path.startsWith(join(root, "seed")))
          throw new Error("invalid seed path");
        const content = await readFile(path);
        response.writeHead(200, {
          "content-type":
            extname(path) === ".json" ? "application/json" : "text/plain",
          "cache-control": "no-store",
        });
        response.end(content);
        return;
      }

      const callbackMatch = url.pathname.match(
        /^\/runs\/([a-z0-9][a-z0-9-]{7,63})\/callback$/,
      );
      if (request.method === "POST" && callbackMatch) {
        const runId = callbackMatch[1];
        if (request.headers["x-kombify-run-token"] !== runId) {
          response.writeHead(403).end();
          return;
        }
        const payload = JSON.parse(await readBody(request));
        if (payload.runId !== runId || !Array.isArray(payload.addresses)) {
          response.writeHead(400).end();
          return;
        }
        await appendFile(
          join(root, "callbacks.jsonl"),
          `${JSON.stringify({ receivedAt: new Date().toISOString(), ...payload })}\n`,
          "utf8",
        );
        response.writeHead(202, { "content-type": "application/json" });
        response.end('{"accepted":true}\n');
        return;
      }

      const eventMatch = url.pathname.match(
        /^\/runs\/([a-z0-9][a-z0-9-]{7,63})\/events$/,
      );
      if (request.method === "POST" && eventMatch) {
        const body = await readBody(request);
        await appendFile(
          join(root, "events.jsonl"),
          `${JSON.stringify({
            receivedAt: new Date().toISOString(),
            runId: eventMatch[1],
            body: JSON.parse(body),
          })}\n`,
          "utf8",
        );
        response.writeHead(202, { "content-type": "application/json" });
        response.end('{"accepted":true}\n');
        return;
      }

      const diagnosticMatch = url.pathname.match(
        /^\/runs\/([a-z0-9][a-z0-9-]{7,63})\/diagnostics$/,
      );
      if (request.method === "POST" && diagnosticMatch) {
        await appendFile(
          join(root, "diagnostics.log"),
          `\n--- ${new Date().toISOString()} ${diagnosticMatch[1]} ---\n${await readBody(request)}\n`,
          "utf8",
        );
        response.writeHead(202).end();
        return;
      }

      response.writeHead(404).end();
    } catch (error) {
      response.writeHead(500, { "content-type": "application/json" });
      response.end(`${JSON.stringify({ error: error.message })}\n`);
    }
  });
}

function main() {
  const options = parseArgs(process.argv.slice(2));
  const server = createCallbackServer(options);
  server.listen(options.port, options.bind, () => {
    process.stdout.write(
      `${JSON.stringify({ ready: true, bind: options.bind, port: options.port, root: options.root })}\n`,
    );
  });
}

if (
  process.argv[1] &&
  import.meta.url.endsWith(process.argv[1].replaceAll("\\", "/"))
) {
  main();
}
