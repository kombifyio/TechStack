import { fetchApi } from "./client";

export interface StorageForecast {
  state: string;
  reason?: string;
  notes: string[];
  used_bytes?: number;
  capacity_bytes?: number;
  free_bytes?: number;
  latest_at?: string;
  observed_from?: string;
  observed_to?: string;
  sample_count: number;
  bucket_count: number;
  growth_bytes_per_day?: number;
  growth_low_bytes_per_day?: number;
  growth_high_bytes_per_day?: number;
  exhaustion?: { earliest_days: number; latest_days?: number };
  horizon_days: number;
  capacity_changed_at?: string;
}

export interface StorageForecastResource {
  id: string;
  labels: Record<string, string>;
  forecast: StorageForecast;
}

export interface StorageForecastReport {
  generated_at: string;
  window_start: string;
  window_end: string;
  resources: StorageForecastResource[];
}

export async function getStorageForecast(
  serverId?: string,
): Promise<StorageForecastReport> {
  const query = serverId ? `?server_id=${encodeURIComponent(serverId)}` : "";
  const response = await fetchApi<StorageForecastReport>(
    `/api/v1/monitor/storage-forecast${query}`,
  );
  if (!response.data || !Array.isArray(response.data.resources)) {
    throw new Error("Invalid storage forecast report");
  }
  return response.data;
}
