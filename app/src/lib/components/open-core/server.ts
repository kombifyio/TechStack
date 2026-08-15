import type { Component } from "svelte";
import type { ServiceMetric } from "./service";

export type ServerStatusKind =
  | "healthy"
  | "degraded"
  | "offline"
  | "pending"
  | "unknown";

export type ServerMetric = ServiceMetric;
export type ServerIcon = Component<{ class?: string }>;
export type ServerNoteTone = "info" | "warning" | "error";

export interface ServerKitInfo {
  name: string;
  detail?: string;
}

export interface ServerFact {
  label: string;
  value: string;
  testId?: string;
}
