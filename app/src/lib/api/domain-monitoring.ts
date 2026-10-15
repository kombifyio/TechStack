import { fetchApi } from "./client";

export interface ExpiryEvidence {
  state: "known" | "unknown";
  source?: string;
  observed_at?: string;
  last_attempt_at?: string;
  fresh_until?: string;
  freshness: "fresh" | "stale" | "unknown";
  expires_at?: string;
  remaining_seconds?: number;
  reason_code?: string;
}

export interface DriftEvidence {
  state: "matching" | "drifted" | "unknown";
  source?: string;
  observed_at?: string;
  last_attempt_at?: string;
  fresh_until?: string;
  detected_at?: string;
  freshness: "fresh" | "stale" | "unknown";
  differences: {
    field: string;
    expected: string[];
    observed: string[];
    state?: "matching" | "drifted" | "unknown";
    reason_code?: string;
  }[];
  reason_code?: string;
  expected_source?: string;
}

export interface DomainCondition {
  id: string;
  kind: string;
  state: "active" | "resolved";
  severity: string;
  first_detected_at: string;
  last_observed_at: string;
  resolved_at?: string;
  evidence_ref?: string;
  freshness?: "fresh" | "stale" | "unknown";
}

export interface DomainMonitoringRow {
  id: string;
  domain: string;
  registered_domain?: string;
  server_id?: string;
  services: { id: string; name: string }[];
  tls_port?: number;
  routing_revision?: number;
  registration: ExpiryEvidence;
  tls: ExpiryEvidence & {
    subject?: string;
    issuer?: string;
    fingerprint_sha256?: string;
    valid?: boolean;
  };
  dns: DriftEvidence;
  tls_drift: DriftEvidence;
  conditions: DomainCondition[];
}

export interface DomainMonitoringReport {
  generated_at: string;
  domains: DomainMonitoringRow[];
}

export async function getDomainMonitoring(
  serverId?: string,
  signal?: AbortSignal,
): Promise<DomainMonitoringReport> {
  const query = serverId ? `?server_id=${encodeURIComponent(serverId)}` : "";
  const response = await fetchApi<DomainMonitoringReport>(
    `/api/v1/monitor/domains${query}`,
    { signal },
  );
  if (!response.data || !Array.isArray(response.data.domains)) {
    throw new Error("Invalid domain monitoring report");
  }
  return response.data;
}
