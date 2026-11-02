// Managed kombify.me address evidence (kombify-Gateway-annz, -7apl): the
// addresses Techstack registered for one managed stack, read as the owner
// through the scoped Techstack read GET /api/v1/tunnel/subdomains (no
// operator key; Techstack answers only the caller's own owner binding), one
// registered first-party host answering HTTPS 200, and the same read after
// decommission showing none of the stack's addresses remain. Each step records
// what it observed, including a failed read; nothing here throws, so the
// address evidence never stops the rest of the managed lane.
// scripts/paid-journey-receipt.mjs maps it onto the steps kombify-me-registered,
// kombify-me-host-reachable and kombify-me-released.

import { authorizedFetch, type RuntimeBearer } from "./runtime-auth";

export interface ManagedKombifyMeRoute {
  id: string;
  fqdn: string;
  kind: string;
  status: string;
  owner_ref: string;
  stack_ref: string;
}

export interface ManagedKombifyMeEvidence {
  registered?: {
    read_at: string;
    stack_ref: string;
    routes: ManagedKombifyMeRoute[];
    error?: string;
  };
  host?: {
    fqdn: string;
    http_status: number | null;
    checked_at: string;
    attempts: Array<{ fqdn: string; http_status: number | null }>;
    error?: string;
  };
  released?: {
    read_at: string;
    stack_ref: string;
    destroy_job_id: string;
    remaining: ManagedKombifyMeRoute[];
    deregistration_status?: string;
    error?: string;
  };
}

interface ReadArgs {
  bearer: RuntimeBearer;
  apiUrl: (path: string) => string;
  stackId: string;
}

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));
const text = (value: unknown) => (typeof value === "string" ? value : "");

// Techstack binds a stack's routes to the lower-cased stack id
// (pkg/kombifyme/binding.go NewManagedAddress).
export function kombifyMeStackRef(stackId: string): string {
  return stackId.trim().toLowerCase();
}

async function readStackRoutes(
  args: ReadArgs,
  timeoutMs = 20_000,
): Promise<ManagedKombifyMeRoute[]> {
  const response = await authorizedFetch(
    args.bearer,
    args.apiUrl("/api/v1/tunnel/subdomains"),
    {
      signal: AbortSignal.timeout(Math.max(1_000, timeoutMs)),
    },
  );
  const body = await response.text();
  if (!response.ok) {
    throw new Error(
      `GET /api/v1/tunnel/subdomains answered HTTP ${response.status}: ${body.slice(0, 200)}`,
    );
  }
  const json = body ? JSON.parse(body) : {};
  const rows = Array.isArray(json) ? json : json?.data;
  if (!Array.isArray(rows))
    throw new Error("the scoped kombify.me read returned no route list");
  const stackRef = kombifyMeStackRef(args.stackId);
  return rows
    .filter((row) => text(row?.binding_stack_ref) === stackRef)
    .map((row) => ({
      id: text(row.id),
      fqdn: text(row.fqdn),
      kind: text(row.subdomain_kind),
      status: text(row.status),
      owner_ref: text(row.binding_owner_ref),
      stack_ref: text(row.binding_stack_ref),
    }));
}

/** After the rollout: the stack's registered addresses and one host answering 200. */
export async function kombifyMeRegistrationEvidence(
  args: ReadArgs & { reachabilityTimeoutMs?: number },
): Promise<ManagedKombifyMeEvidence> {
  const evidence: ManagedKombifyMeEvidence = {};
  const stackRef = kombifyMeStackRef(args.stackId);
  try {
    evidence.registered = {
      read_at: new Date().toISOString(),
      stack_ref: stackRef,
      routes: await readStackRoutes(args),
    };
  } catch (error) {
    evidence.registered = {
      read_at: new Date().toISOString(),
      stack_ref: stackRef,
      routes: [],
      error: String(error).slice(0, 300),
    };
    return evidence;
  }
  // Service hosts first (the install's auth host answers on its own), then the base.
  const hosts = evidence.registered.routes
    .filter(
      (route) =>
        route.status === "active" && route.fqdn.endsWith(".kombify.me"),
    )
    .sort(
      (a, b) =>
        Number(a.kind !== "service") - Number(b.kind !== "service") ||
        Number(!a.fqdn.startsWith("auth.")) -
          Number(!b.fqdn.startsWith("auth.")),
    )
    .map((route) => route.fqdn);
  const attempts: Array<{ fqdn: string; http_status: number | null }> = [];
  let last: { fqdn: string; http_status: number | null; error?: string } = {
    fqdn: hosts[0] ?? "",
    http_status: null,
    error: hosts.length ? undefined : "no active kombify.me route to open",
  };
  // DNS and the edge certificate of a fresh install can trail the rollout
  // briefly. Every wait is clamped to the deadline, so the caller's phase
  // budget holds even when each host hangs.
  const deadline = Date.now() + (args.reachabilityTimeoutMs ?? 180_000);
  const left = () => deadline - Date.now();
  while (hosts.length && left() > 1_000) {
    for (const fqdn of hosts) {
      if (left() <= 1_000) break;
      let status: number | null = null;
      let error: string | undefined;
      try {
        // The registered host itself must answer 200; a redirect elsewhere is not that proof.
        const response = await fetch(`https://${fqdn}/`, {
          redirect: "manual",
          signal: AbortSignal.timeout(Math.min(15_000, left())),
        });
        status = response.status;
        await response.body?.cancel().catch(() => undefined);
      } catch (cause) {
        error = String(cause).slice(0, 200);
      }
      attempts.push({ fqdn, http_status: status });
      last = { fqdn, http_status: status, error };
      if (status === 200) break;
    }
    if (last.http_status === 200 || left() <= 1_000) break;
    await delay(Math.min(15_000, left()));
  }
  evidence.host = {
    fqdn: last.fqdn,
    http_status: last.http_status,
    checked_at: new Date().toISOString(),
    attempts: attempts.slice(-20),
    ...(last.http_status === 200 || !last.error ? {} : { error: last.error }),
  };
  return evidence;
}

/** After decommission: none of the stack's addresses remain. */
export async function kombifyMeReleaseEvidence(
  // deadline: the epoch milliseconds by which the read must have finished,
  // so the cleanup still writes its receipt inside its own budget.
  args: ReadArgs & { destroyJob?: Record<string, unknown>; deadline: number },
): Promise<NonNullable<ManagedKombifyMeEvidence["released"]>> {
  const stackRef = kombifyMeStackRef(args.stackId);
  const result = args.destroyJob?.result as Record<string, unknown> | undefined;
  const deregistration = result?.kombify_me_deregistration as
    Record<string, unknown> | undefined;
  const base = {
    stack_ref: stackRef,
    destroy_job_id: text(args.destroyJob?.id),
    ...(text(deregistration?.status)
      ? { deregistration_status: text(deregistration?.status) }
      : {}),
  };
  let remaining: ManagedKombifyMeRoute[] = [];
  let error: string | undefined = "no cleanup time was left for the read";
  const left = () => args.deadline - Date.now();
  // The destroy job deregisters before it completes; two short rereads cover a lagging registry.
  for (let attempt = 0; attempt < 3; attempt += 1) {
    if (attempt) {
      if (left() < 15_000) break;
      await delay(10_000);
    }
    if (left() < 3_000) break;
    try {
      remaining = await readStackRoutes(args, Math.min(20_000, left()));
      error = undefined;
      if (remaining.length === 0) break;
    } catch (cause) {
      error = String(cause).slice(0, 300);
    }
  }
  return {
    read_at: new Date().toISOString(),
    ...base,
    remaining,
    ...(error ? { error } : {}),
  };
}
