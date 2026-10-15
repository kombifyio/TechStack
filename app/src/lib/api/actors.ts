/**
 * Agents and Offices for the Actors page, read through the Gateway with the
 * user's own Auth0 token (the SPA's, or the embedding portal's in Cloud):
 *   - `GET /v1/ai/agents`   the agents the caller may use (AI Platform)
 *   - `GET /v1/ai/offices`  the caller's Offices, each inside one Factory
 *
 * Factories have no list of their own here: an agent's harness is its
 * Factory, and every Office names the Factory it belongs to.
 *
 * Every outcome is typed, so the page says which one it got instead of
 * drawing an empty list:
 *   - `ready`        the list, possibly empty
 *   - `unavailable`  the route or its auth does not exist for this caller
 *   - `error`        the route exists but this read failed
 */
import { resolveGatewayBearer } from "#lib/api/client.js";
import { connectGatewayOrigin } from "#lib/api/connectDevices.js";

export interface ActorAgent {
  id: string;
  name: string;
  description?: string;
  /** Factory the agent works in (its harness), when the catalogue names one. */
  factoryId?: string;
  factoryName?: string;
  /** "orchestrator" or "expert" from the harness. */
  role?: string;
  status?: "available" | "unavailable" | "disabled";
  tier?: string;
  /** Catalogue group, e.g. "ai", "server" or "support". */
  group?: string;
}

export interface ActorOffice {
  id: string;
  name: string;
  factoryId: string;
  /** What the Office narrows to, e.g. a mailbox or a server. */
  contextKey?: string;
  createdAt?: string;
}

export type ActorReadResult<T> =
  | { state: "ready"; items: T[] }
  | { state: "unavailable"; reason: string }
  | { state: "error"; reason: string };

function asString(value: unknown): string {
  return typeof value === "string" ? value.trim() : "";
}

function asRecord(value: unknown): Record<string, unknown> {
  return value && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : {};
}

function listOf(body: unknown, key: string): unknown[] | null {
  if (Array.isArray(body)) return body;
  const list = asRecord(body)[key];
  return Array.isArray(list) ? list : null;
}

/** Validate the agent list at the boundary; rows without an id are dropped. */
export function parseAgents(body: unknown): ActorAgent[] | null {
  const rows = listOf(body, "agents");
  if (!rows) return null;
  return rows.flatMap((entry): ActorAgent[] => {
    const raw = asRecord(entry);
    const id = asString(raw.id);
    if (!id) return [];
    const harness = asRecord(raw.harness);
    const status = asString(raw.executionStatus ?? raw.execution_status);
    return [
      {
        id,
        name: asString(raw.displayName ?? raw.display_name) || id,
        description: asString(raw.description) || undefined,
        factoryId: asString(harness.id) || undefined,
        factoryName: asString(harness.label) || undefined,
        role: asString(harness.role) || undefined,
        status:
          status === "available" ||
          status === "unavailable" ||
          status === "disabled"
            ? status
            : undefined,
        tier: asString(raw.agentTier ?? raw.agent_tier) || undefined,
        group: asString(raw.group) || undefined,
      },
    ];
  });
}

/** Validate the Office list; archived Offices are not workplaces anymore. */
export function parseOffices(body: unknown): ActorOffice[] | null {
  const rows = listOf(body, "offices");
  if (!rows) return null;
  return rows.flatMap((entry): ActorOffice[] => {
    const raw = asRecord(entry);
    const id = asString(raw.id);
    const factoryId = asString(raw.factoryId ?? raw.factory_id);
    if (!id || !factoryId) return [];
    if (asString(raw.archivedAt ?? raw.archived_at)) return [];
    return [
      {
        id,
        name: asString(raw.name) || id,
        factoryId,
        contextKey: asString(raw.contextKey ?? raw.context_key) || undefined,
        createdAt: asString(raw.createdAt ?? raw.created_at) || undefined,
      },
    ];
  });
}

async function reasonCode(response: Response): Promise<string> {
  try {
    const body = asRecord(await response.json());
    return (
      asString(body.reason_code) ||
      asString(body.error_code) ||
      asString(body.code) ||
      asString(asRecord(body.error).code)
    );
  } catch {
    return "";
  }
}

export async function classifyActorResponse<T>(
  response: Response,
  parse: (body: unknown) => T[] | null,
): Promise<ActorReadResult<T>> {
  if (response.ok) {
    let body: unknown;
    try {
      body = await response.json();
    } catch {
      return { state: "error", reason: "invalid_response" };
    }
    const items = parse(body);
    return items
      ? { state: "ready", items }
      : { state: "error", reason: "invalid_response" };
  }
  const code = await reasonCode(response);
  if ([401, 403, 404, 503].includes(response.status)) {
    return { state: "unavailable", reason: code || `http_${response.status}` };
  }
  return { state: "error", reason: code || `http_${response.status}` };
}

interface ReadOptions {
  fetchImpl?: typeof fetch;
  getToken?: () => Promise<string>;
  signal?: AbortSignal;
}

async function read<T>(
  path: string,
  parse: (body: unknown) => T[] | null,
  options: ReadOptions,
): Promise<ActorReadResult<T>> {
  const url = `${connectGatewayOrigin()}${path}`;
  const getToken = options.getToken ?? (() => resolveGatewayBearer(url));
  const fetchImpl = options.fetchImpl ?? fetch;
  let token: string;
  try {
    token = await getToken();
  } catch {
    return { state: "unavailable", reason: "gateway_auth_unavailable" };
  }
  let response: Response;
  try {
    response = await fetchImpl(url, {
      method: "GET",
      headers: { Authorization: `Bearer ${token}`, Accept: "application/json" },
      signal: options.signal,
    });
  } catch {
    return { state: "error", reason: "network" };
  }
  return classifyActorResponse(response, parse);
}

export function listAgents(
  options: ReadOptions = {},
): Promise<ActorReadResult<ActorAgent>> {
  return read("/v1/ai/agents", parseAgents, options);
}

export function listOffices(
  options: ReadOptions = {},
): Promise<ActorReadResult<ActorOffice>> {
  return read("/v1/ai/offices", parseOffices, options);
}
