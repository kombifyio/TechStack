// Managed Day-2 lifecycle evidence (TS-2): stop, start, reconnect and the
// generation-bound owner SSH grant on one real managed node, observed only
// through the owner-scoped product API. Every step records the ledger
// operation or journal receipt it produced; nothing is inferred from UI state.

export interface ManagedDay2PowerStep {
  requested_status: number;
  operation_id: string;
  desired_power_state: string;
  status: string;
  phase: string;
  receipt_sequence?: number;
  receipt_digest?: string;
  resource_generation_id?: string;
  reason_code?: string;
  converged_after_ms: number;
}

export interface ManagedDay2Evidence {
  lease_id: string;
  stack_id: string;
  stop: ManagedDay2PowerStep;
  start: ManagedDay2PowerStep;
  reconnect: { attempts: number; agent_restarted: boolean; connection_state?: string };
  ssh_disable: Record<string, unknown>;
  ssh_enable: Record<string, unknown>;
  status_after: { ssh_enabled?: boolean; observed_state?: string; desired_state?: string };
  journal: Array<{ event_type: string; status: string; created_at?: string }>;
}

interface Day2Args {
  token: string;
  apiUrl: (path: string) => string;
  leaseId: string;
  stackId: string;
  log: (message: string, fields: Record<string, unknown>) => void;
  powerTimeoutMs?: number;
}

type MonthlyRuntimePayload = {
  desired_state?: string;
  observed_state?: string;
  ssh_enabled?: boolean;
  power?: {
    operation_id: string;
    desired_power_state: string;
    status: string;
    phase: string;
    reason_code?: string;
    receipt_sequence?: number;
    receipt_digest?: string;
    resource_generation_id?: string;
  } | null;
  ssh_access?: Record<string, unknown> | null;
  reconnect?: { agent_restarted?: boolean; connection_state?: string } | null;
};

type Day2Denial = {
  error?: { details?: { reason_code?: string; retryable?: boolean } };
};

const delay = (ms: number) => new Promise((resolve) => setTimeout(resolve, ms));

async function runtimeCall<T>(
  args: Day2Args,
  method: "GET" | "POST",
  suffix: string,
  accepted: number[] = [200],
): Promise<{ status: number; data: T }> {
  const path = `/api/v1/monthly-runtimes/${encodeURIComponent(args.leaseId)}${suffix}`;
  const response = await fetch(args.apiUrl(path), {
    method,
    headers: { Authorization: `Bearer ${args.token}` },
    signal: AbortSignal.timeout(110_000),
  });
  const text = await response.text();
  const json = text ? JSON.parse(text) : {};
  if (!accepted.includes(response.status)) {
    throw new Error(`Managed Day-2 ${method} ${suffix || "status"} failed with HTTP ${response.status}: ${text}`);
  }
  return { status: response.status, data: (json.data ?? json) as T };
}

async function convergePower(
  args: Day2Args,
  action: "stop" | "start",
): Promise<ManagedDay2PowerStep> {
  const started = Date.now();
  const requested = await runtimeCall<MonthlyRuntimePayload>(args, "POST", `/${action}`, [200, 202]);
  const operationId = requested.data.power?.operation_id ?? "";
  if (!operationId) {
    throw new Error(`Managed ${action} returned no power operation: ${JSON.stringify(requested.data)}`);
  }
  args.log(`managed ${action} accepted`, { operation_id: operationId, http_status: requested.status });
  const deadline = started + (args.powerTimeoutMs ?? 720_000);
  let power = requested.data.power ?? undefined;
  while (power?.status !== "succeeded") {
    if (power?.status === "failed") {
      throw new Error(`Managed ${action} failed: ${JSON.stringify(power)}`);
    }
    if (Date.now() > deadline) {
      throw new Error(`Managed ${action} did not converge: ${JSON.stringify(power)}`);
    }
    await delay(5_000);
    const status = await runtimeCall<MonthlyRuntimePayload>(args, "GET", "");
    if (status.data.power?.operation_id === operationId) {
      power = status.data.power;
    }
  }
  return {
    requested_status: requested.status,
    operation_id: operationId,
    desired_power_state: power.desired_power_state,
    status: power.status,
    phase: power.phase,
    receipt_sequence: power.receipt_sequence,
    receipt_digest: power.receipt_digest,
    resource_generation_id: power.resource_generation_id,
    reason_code: power.reason_code,
    converged_after_ms: Date.now() - started,
  };
}

async function convergeSSHAccess(
  args: Day2Args,
  action: "disable" | "enable",
): Promise<MonthlyRuntimePayload> {
  // A just-restarted node may report a Guard connection before its SSH
  // management channel is ready. Retry only the product's explicit transient
  // denial; every other refusal remains terminal evidence.
  const suffix = action === "disable" ? "/disable-ssh" : "/enable-ssh";
  for (let attempt = 1; attempt <= 4; attempt++) {
    const result = await runtimeCall<MonthlyRuntimePayload & Day2Denial>(
      args, "POST", suffix, [200, 503],
    );
    if (result.status === 200) return result.data;
    const details = result.data.error?.details;
    if (details?.reason_code !== "managed_node_channel_unavailable" || details.retryable !== true) {
      throw new Error(`Managed SSH ${action} was denied without retryable node-channel guidance`);
    }
    args.log(`managed SSH ${action} waiting for node channel`, { attempt, http_status: result.status });
    if (attempt === 4) break;
    await delay(15_000);
  }
  throw new Error(`Managed SSH ${action} node channel remained unavailable after 4 attempts`);
}

export async function runManagedDay2Evidence(args: Day2Args): Promise<ManagedDay2Evidence> {
  const stop = await convergePower(args, "stop");
  const start = await convergePower(args, "start");

  // Right after power-on Guard may not have re-established its session; a
  // reconnect either proves it (no restart) or restarts Guard over the
  // execution channel. A bounded number of owner attempts covers boot time.
  let reconnect: MonthlyRuntimePayload["reconnect"] = undefined;
  let attempts = 0;
  while (!reconnect && attempts < 4) {
    attempts++;
    const result = await runtimeCall<MonthlyRuntimePayload>(args, "POST", "/reconnect", [200, 409, 503]);
    if (result.status === 200) {
      reconnect = result.data.reconnect ?? { agent_restarted: false };
      break;
    }
    args.log("managed reconnect not yet proven", { attempt: attempts, http_status: result.status });
    await delay(30_000);
  }
  if (!reconnect) {
    throw new Error(`Managed reconnect never proved a Guard connection after ${attempts} attempts`);
  }

  const disable = await convergeSSHAccess(args, "disable");
  if (disable.ssh_access?.enabled !== false) {
    throw new Error(`Managed SSH disable did not report a disabled grant: ${JSON.stringify(disable)}`);
  }
  const enable = await convergeSSHAccess(args, "enable");
  if (enable.ssh_access?.enabled !== true) {
    throw new Error(`Managed SSH enable did not report an enabled grant: ${JSON.stringify(enable)}`);
  }
  const statusAfter = await runtimeCall<MonthlyRuntimePayload>(args, "GET", "");
  const journal = await runtimeCall<Array<{ event_type: string; status: string; created_at?: string }>>(
    args, "GET", "/operations?limit=20",
  );
  return {
    lease_id: args.leaseId,
    stack_id: args.stackId,
    stop,
    start,
    reconnect: {
      attempts,
      agent_restarted: Boolean(reconnect.agent_restarted),
      connection_state: reconnect.connection_state,
    },
    ssh_disable: disable.ssh_access ?? {},
    ssh_enable: enable.ssh_access ?? {},
    status_after: {
      ssh_enabled: statusAfter.data.ssh_enabled,
      observed_state: statusAfter.data.observed_state,
      desired_state: statusAfter.data.desired_state,
    },
    journal: (Array.isArray(journal.data) ? journal.data : []).map((event) => ({
      event_type: event.event_type, status: event.status, created_at: event.created_at,
    })),
  };
}
