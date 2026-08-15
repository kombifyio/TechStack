import type { Component } from "svelte";

export type ServicePlacement = "local" | "cloud" | "serverless" | "unknown";
export type ServiceStatusKind =
  | "running"
  | "stopped"
  | "migrating"
  | "update"
  | "frozen"
  | "error"
  | "pending"
  | "unknown";

export interface ServiceCardDisplay {
  statusLabel?: string;
  managementLabel?: string;
}

export interface ServiceMetric {
  label: string;
  value: string;
}

export interface ServiceMigration {
  to: ServicePlacement;
  progress: number;
  message?: string;
}

export interface ServiceUpdate {
  from: string;
  to: string;
  message?: string;
}

export interface ServiceCardActions {
  onOpen?: () => void;
  onLogs?: () => void;
  onEdit?: () => void;
  onFreeze?: () => void;
  onUnfreeze?: () => void;
  onUpdate?: () => void;
  onRestart?: () => void;
  onInfo?: () => void;
}

export type ServiceIcon = Component<{ class?: string }>;
