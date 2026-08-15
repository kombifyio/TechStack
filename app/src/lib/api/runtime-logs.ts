import { API_BASE, get } from "$lib/api/client";

export interface RuntimeLogEntry {
  id?: string;
  timestamp: string;
  ingested_at?: string;
  level: "debug" | "info" | "warn" | "error" | string;
  message: string;
  source?: string;
  agent_id?: string;
  stack_id?: string;
  job_id?: string;
  server_id?: string;
  service_id?: string;
  fields?: Record<string, string>;
}

export interface RuntimeLogScope {
  agentId?: string;
  serverId?: string;
}

function runtimeLogQuery(scope: RuntimeLogScope, limit = 500): string {
  const query = new URLSearchParams({ limit: String(limit) });
  if (scope.agentId?.trim()) query.set("agent_id", scope.agentId.trim());
  else if (scope.serverId?.trim())
    query.set("server_id", scope.serverId.trim());
  return query.toString();
}

export function getRuntimeLogs(
  scope: RuntimeLogScope,
): Promise<RuntimeLogEntry[]> {
  return get(`/api/v1/runtime/logs?${runtimeLogQuery(scope)}`);
}

export function streamRuntimeLogs(
  scope: RuntimeLogScope,
  onEntries: (entries: RuntimeLogEntry[]) => void,
  onError: () => void,
): () => void {
  let source: EventSource | null = null;
  try {
    source = new EventSource(
      `${API_BASE}/api/v1/runtime/logs/stream?${runtimeLogQuery(scope)}`,
      { withCredentials: true },
    );
  } catch {
    onError();
    return () => {};
  }
  const read = (event: Event) => {
    try {
      const parsed = JSON.parse((event as MessageEvent<string>).data);
      onEntries(Array.isArray(parsed) ? parsed : [parsed]);
    } catch {
      onError();
    }
  };
  source.addEventListener("history", read);
  source.addEventListener("log", read);
  source.onerror = onError;
  return () => {
    source?.close();
    source = null;
  };
}
