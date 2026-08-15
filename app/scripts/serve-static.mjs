#!/usr/bin/env node
/**
 * Minimal static file server for the adapter-static SPA build (ADR-033 OQ2).
 *
 * Serves `build-static/` with an index.html SPA fallback and reverse-proxies
 * backend paths (/api/*, /install.sh, /install.ps1, /metrics, health probes)
 * to TECHSTACK_API_URL. This keeps the docker-compose `app` service (frontend
 * on its own port, backend on another) working without the retired
 * adapter-node SSR process. Production deployments do not use this server —
 * the Go binary serves the embedded SPA itself.
 */

import { createServer, request as httpRequest } from "node:http";
import { request as httpsRequest } from "node:https";
import { createReadStream, existsSync, statSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const rootDir = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const staticDir = process.env.TECHSTACK_STATIC_DIR
  ? path.resolve(process.env.TECHSTACK_STATIC_DIR)
  : path.join(rootDir, "build-static");
const host = process.env.HOST || "0.0.0.0";
const port = Number(process.env.PORT || 5261);
const backendBase = (
  process.env.TECHSTACK_API_URL ||
  process.env.API_URL ||
  ""
).replace(/\/+$/, "");

if (!existsSync(path.join(staticDir, "index.html"))) {
  console.error(
    `[serve-static] no index.html in ${staticDir}; run pnpm build first`,
  );
  process.exit(1);
}

const contentTypes = new Map([
  [".html", "text/html; charset=utf-8"],
  [".js", "text/javascript; charset=utf-8"],
  [".mjs", "text/javascript; charset=utf-8"],
  [".css", "text/css; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".svg", "image/svg+xml"],
  [".png", "image/png"],
  [".jpg", "image/jpeg"],
  [".jpeg", "image/jpeg"],
  [".gif", "image/gif"],
  [".ico", "image/x-icon"],
  [".webp", "image/webp"],
  [".woff", "font/woff"],
  [".woff2", "font/woff2"],
  [".ttf", "font/ttf"],
  [".txt", "text/plain; charset=utf-8"],
  [".webmanifest", "application/manifest+json"],
  [".map", "application/json"],
  [".wasm", "application/wasm"],
]);

function isBackendPath(pathname) {
  if (pathname.startsWith("/api/") || pathname === "/api") return true;
  // Go-owned root redirects (web convergence). Exact paths only — other
  // /auth/* routes (oidc-complete, sso, cloud-link-complete) are SPA pages.
  if (pathname === "/docs" || pathname.startsWith("/docs/")) return true;
  return [
    "/auth/callback",
    "/auth/logout",
    "/install.sh",
    "/install.ps1",
    "/metrics",
    "/health",
    "/healthz",
    "/livez",
    "/readyz",
    "/openapi.json",
  ].includes(pathname);
}

function proxy(req, res, url) {
  if (!backendBase) {
    res.statusCode = 502;
    res.setHeader("Content-Type", "text/plain; charset=utf-8");
    res.end(
      "Backend base URL is not configured. Set TECHSTACK_API_URL for the static frontend proxy.",
    );
    return;
  }
  const target = new URL(backendBase + url.pathname + url.search);
  const requestFn = target.protocol === "https:" ? httpsRequest : httpRequest;
  const headers = { ...req.headers };
  delete headers.host;
  delete headers.connection;

  const upstream = requestFn(
    target,
    { method: req.method, headers },
    (upstreamRes) => {
      const responseHeaders = { ...upstreamRes.headers };
      delete responseHeaders["transfer-encoding"];
      res.writeHead(upstreamRes.statusCode ?? 502, responseHeaders);
      upstreamRes.pipe(res);
    },
  );
  upstream.on("error", (error) => {
    res.statusCode = 502;
    res.setHeader("Content-Type", "text/plain; charset=utf-8");
    res.end(`Upstream request failed: ${error.message}`);
  });
  req.pipe(upstream);
}

function resolveStaticFile(pathname) {
  const decoded = decodeURIComponent(pathname);
  const clean = path.normalize(decoded).replace(/^([/\\])+/, "");
  const candidate = path.join(staticDir, clean);
  if (!candidate.startsWith(staticDir)) {
    return null; // path traversal
  }
  if (existsSync(candidate) && statSync(candidate).isFile()) {
    return candidate;
  }
  return null;
}

function sendFile(res, filePath, status = 200) {
  res.statusCode = status;
  res.setHeader(
    "Content-Type",
    contentTypes.get(path.extname(filePath).toLowerCase()) ??
      "application/octet-stream",
  );
  createReadStream(filePath).pipe(res);
}

const server = createServer((req, res) => {
  const url = new URL(req.url ?? "/", `http://${req.headers.host ?? "local"}`);

  if (isBackendPath(url.pathname)) {
    proxy(req, res, url);
    return;
  }

  if (req.method !== "GET" && req.method !== "HEAD") {
    res.statusCode = 405;
    res.end();
    return;
  }

  const filePath =
    url.pathname === "/" ? null : resolveStaticFile(url.pathname);
  if (filePath) {
    sendFile(res, filePath);
    return;
  }

  // SPA fallback: any extension-less path renders the app shell.
  if (path.extname(url.pathname) === "") {
    sendFile(res, path.join(staticDir, "index.html"));
    return;
  }

  res.statusCode = 404;
  res.end("Not found");
});

server.listen(port, host, () => {
  console.log(
    `[serve-static] serving ${staticDir} on http://${host}:${port}` +
      (backendBase
        ? ` (backend proxy -> ${backendBase})`
        : " (no backend proxy)"),
  );
});
