/**
 * Session tracker for governed service actions.
 *
 * Every governed mutation is accepted by the control plane as a job, and the
 * job receipt (not the POST acknowledgement) is the result. The tracker owns
 * that lifecycle for every surface that runs actions - the Services board and
 * the application detail page - so a button renders the same executing /
 * verifying truth everywhere:
 *
 *   queueing -> queued/running (job) -> completed -> verifying (inventory
 *   catches up past the revision the action was issued against) -> done.
 *
 * Log pages ride the same job mechanism but are collected into `logs`.
 */
import { getJob, type JobDetail } from "../api/jobs";
import {
  SERVICE_LOCK_ACTIONS,
  getManagedServiceLogs,
  runManagedServiceAction,
  type ManagedServiceAction,
  type ManagedServiceLogEntry,
} from "../api/services";
import { tr } from "../i18n.svelte";

export interface TrackedServiceAction {
  action: ManagedServiceAction;
  jobId: string;
  state: string;
  detail?: string;
  logCursor?: string;
  expectedInventoryRevision: number;
  converging?: boolean;
  convergenceDeadline?: number;
  reasonCode?: string;
  retryable?: boolean;
  historyId?: string;
}

export interface TrackedServiceLogs {
  loading: boolean;
  entries: ManagedServiceLogEntry[];
  nextCursor?: string;
  error?: string;
}

/** One governed action run in this browser session (never persisted). */
export interface ServiceActionHistoryEntry {
  id: string;
  serviceId: string;
  serviceName: string;
  action: ManagedServiceAction;
  jobId: string;
  state: string;
  detail?: string;
  startedAt: string;
  updatedAt: string;
}

/** The slice of a canonical service the tracker needs to run an action. */
export interface TrackableService {
  id: string;
  name: string;
  inventory_revision: number;
}

export interface ServiceActionTrackerHooks {
  /** Re-read the service inventory after a mutation finished. */
  refresh: () => Promise<void>;
  /** The newest inventory revision the caller has observed for a service. */
  observedRevision: (serviceId: string) => number | undefined;
}

const TERMINAL_STATES = [
  "completed",
  "failed",
  "error",
  "cancelled",
  "canceled",
];
const HISTORY_LIMIT = 50;

export function normalizeJobState(state: string | undefined): string {
  return (state ?? "").trim().toLowerCase() || "queued";
}

export function isActionTerminal(state: string): boolean {
  return TERMINAL_STATES.includes(normalizeJobState(state));
}

/** A job still in flight, or one whose effect inventory has not confirmed yet. */
export function isActionActive(job: TrackedServiceAction | undefined): boolean {
  if (!job) return false;
  return Boolean(job.converging) || !isActionTerminal(job.state);
}

export function actionFailed(job: TrackedServiceAction | undefined): boolean {
  return (
    Boolean(job) &&
    isActionTerminal(job!.state) &&
    normalizeJobState(job!.state) !== "completed"
  );
}

function jobOutcome(result: JobDetail["result"]): {
  reasonCode?: string;
  retryable?: boolean;
} {
  if (!result || typeof result !== "object") return {};
  const root = result as Record<string, unknown>;
  const records: Record<string, unknown>[] = [root];
  for (const value of [root.service_action_receipt, root.command_result]) {
    if (value && typeof value === "object") {
      records.push(value as Record<string, unknown>);
    }
  }
  const commandResult = root.command_result;
  if (commandResult && typeof commandResult === "object") {
    const data = (commandResult as Record<string, unknown>).data;
    if (data && typeof data === "object") {
      const dataRecord = data as Record<string, unknown>;
      records.push(dataRecord);
      if (typeof dataRecord.output === "string") {
        try {
          const output = JSON.parse(dataRecord.output);
          if (output && typeof output === "object") {
            records.push(output as Record<string, unknown>);
          }
        } catch {
          // Human output is valid but has no structured outcome metadata.
        }
      }
    }
  }
  for (const record of records) {
    const reasonCode =
      typeof record.reason_code === "string"
        ? record.reason_code
        : typeof record.reasonCode === "string"
          ? record.reasonCode
          : undefined;
    const retryable =
      typeof record.retryable === "boolean" ? record.retryable : undefined;
    if (reasonCode || retryable !== undefined) return { reasonCode, retryable };
  }
  return {};
}

export class ServiceActionTracker {
  jobs = $state<Record<string, TrackedServiceAction>>({});
  logs = $state<Record<string, TrackedServiceLogs>>({});
  history = $state<ServiceActionHistoryEntry[]>([]);

  #hooks: ServiceActionTrackerHooks;
  #active = false;
  #polling = false;
  #sequence = 0;

  constructor(hooks: ServiceActionTrackerHooks) {
    this.#hooks = hooks;
  }

  /** Mark the owning view as mounted; late responses of a closed view are dropped. */
  start(): void {
    this.#active = true;
  }

  stop(): void {
    this.#active = false;
  }

  hasActiveJobs(): boolean {
    return Object.values(this.jobs).some(
      (job) => Boolean(job.jobId) && !isActionTerminal(job.state),
    );
  }

  hasConverging(): boolean {
    return Object.values(this.jobs).some((job) => job.converging);
  }

  /** Poll jobs and inventory convergence; call from the owner's interval. */
  async tick(): Promise<void> {
    if (this.hasActiveJobs()) await this.#pollJobs();
    if (this.hasConverging()) await this.#checkConvergence();
  }

  /**
   * Run one governed action. Returns the failure message, or null when the
   * control plane accepted it (its job is then tracked).
   */
  async run(
    service: TrackableService,
    action: ManagedServiceAction,
    logCursor?: string,
  ): Promise<string | null> {
    const historyId =
      action === "logs" ? undefined : `h${Date.now()}-${(this.#sequence += 1)}`;
    this.jobs = {
      ...this.jobs,
      [service.id]: {
        action,
        jobId: "",
        state: "queueing",
        logCursor,
        expectedInventoryRevision: service.inventory_revision,
        historyId,
      },
    };
    if (historyId) {
      const now = new Date().toISOString();
      this.history = [
        {
          id: historyId,
          serviceId: service.id,
          serviceName: service.name,
          action,
          jobId: "",
          state: "queueing",
          startedAt: now,
          updatedAt: now,
        },
        ...this.history,
      ].slice(0, HISTORY_LIMIT);
    }
    if (action === "logs") {
      const previous = this.logs[service.id];
      this.logs = {
        ...this.logs,
        [service.id]: {
          loading: true,
          entries: previous?.entries ?? [],
          nextCursor: previous?.nextCursor,
        },
      };
    }
    try {
      const result = await runManagedServiceAction(
        service.id,
        action,
        service.inventory_revision,
        crypto.randomUUID(),
        action === "logs" ? { cursor: logCursor } : undefined,
      );
      if (!this.#active) return null;
      // A guardrail change is applied by the control plane before it answers,
      // so there is no job to follow - only a read model to re-read.
      if (SERVICE_LOCK_ACTIONS.has(action)) {
        const { [service.id]: _cleared, ...rest } = this.jobs;
        this.jobs = rest;
        this.#touchHistory(historyId, { state: "completed" });
        await this.#hooks.refresh();
        return null;
      }
      if (!result.job_id) {
        throw new Error(tr("ui.services.serviceActionDidNotReturn"));
      }
      this.jobs = {
        ...this.jobs,
        [service.id]: {
          action,
          jobId: result.job_id,
          // The POST is only an acknowledgement; the job receipt is the result.
          state: "queued",
          logCursor,
          expectedInventoryRevision: service.inventory_revision,
          historyId,
        },
      };
      this.#touchHistory(historyId, { jobId: result.job_id, state: "queued" });
      void this.#pollJobs();
      return null;
    } catch (err) {
      if (!this.#active) return null;
      const error =
        err instanceof Error
          ? err.message
          : tr("ui.services.serviceActionFailed");
      this.jobs = {
        ...this.jobs,
        [service.id]: {
          action,
          jobId: "",
          state: "failed",
          detail: error,
          logCursor,
          expectedInventoryRevision: service.inventory_revision,
          historyId,
        },
      };
      this.#touchHistory(historyId, { state: "failed", detail: error });
      if (action === "logs") {
        const previous = this.logs[service.id];
        this.logs = {
          ...this.logs,
          [service.id]: {
            loading: false,
            entries: previous?.entries ?? [],
            nextCursor: previous?.nextCursor,
            error,
          },
        };
      }
      return error;
    }
  }

  /** Fetch the next older page of logs once the previous collection settled. */
  loadMoreLogs(service: TrackableService): void {
    const nextCursor = this.logs[service.id]?.nextCursor;
    if (!nextCursor || isActionActive(this.jobs[service.id])) return;
    void this.run(service, "logs", nextCursor);
  }

  #touchHistory(
    historyId: string | undefined,
    patch: Partial<ServiceActionHistoryEntry>,
  ): void {
    if (!historyId) return;
    this.history = this.history.map((entry) =>
      entry.id === historyId
        ? { ...entry, ...patch, updatedAt: new Date().toISOString() }
        : entry,
    );
  }

  async #loadLogs(serviceId: string, cursor: string | undefined) {
    const previous = this.logs[serviceId];
    try {
      const page = await getManagedServiceLogs(serviceId, { cursor });
      if (!this.#active) return;
      this.logs = {
        ...this.logs,
        [serviceId]: {
          loading: false,
          entries: cursor
            ? [...(previous?.entries ?? []), ...page.entries]
            : page.entries,
          nextCursor: page.next_cursor,
        },
      };
    } catch (err) {
      if (!this.#active) return;
      const error =
        err instanceof Error
          ? err.message
          : tr("ui.services.serviceLogsCouldNotBe");
      this.logs = {
        ...this.logs,
        [serviceId]: {
          loading: false,
          entries: previous?.entries ?? [],
          nextCursor: previous?.nextCursor,
          error,
        },
      };
    }
  }

  async #pollJobs(): Promise<void> {
    if (this.#polling) return;
    const activeActions = Object.entries(this.jobs).filter(
      ([, job]) => Boolean(job.jobId) && !isActionTerminal(job.state),
    );
    if (activeActions.length === 0) return;
    this.#polling = true;
    try {
      const updates = await Promise.all(
        activeActions.map(async ([serviceId, action]) => {
          try {
            return { serviceId, action, job: await getJob(action.jobId) };
          } catch {
            // Retain the job as pollable after a transient read failure.
            return null;
          }
        }),
      );
      if (!this.#active) return;
      const next = { ...this.jobs };
      const completedLogs: Array<{ serviceId: string; cursor?: string }> = [];
      let refreshInventory = false;
      for (const update of updates) {
        if (!update) continue;
        const current = next[update.serviceId];
        if (!current || current.jobId !== update.action.jobId) continue;
        const state = normalizeJobState(update.job.state);
        const terminal = isActionTerminal(state);
        const outcome = jobOutcome(update.job.result);
        const known =
          this.#hooks.observedRevision(update.serviceId) !== undefined;
        const convergenceWindowSeconds = known ? 90 : 60;
        const detail =
          state === "completed"
            ? update.job.message ||
              tr("ui.services.actionCompletedInventoryIsRefreshing")
            : terminal
              ? update.job.error_details ||
                update.job.error ||
                update.job.message ||
                tr("ui.services.serviceActionDidNotComplete")
              : update.job.message || update.job.current_step;
        next[update.serviceId] = {
          ...current,
          state,
          converging: state === "completed" && current.action !== "logs",
          convergenceDeadline:
            state === "completed" && current.action !== "logs"
              ? Date.now() + convergenceWindowSeconds * 1000
              : undefined,
          reasonCode: outcome.reasonCode,
          retryable: outcome.retryable,
          detail,
        };
        this.#touchHistory(current.historyId, { state, detail });
        if (state === "completed") {
          if (current.action === "logs") {
            completedLogs.push({
              serviceId: update.serviceId,
              cursor: current.logCursor,
            });
          } else {
            refreshInventory = true;
          }
        } else if (terminal && current.action === "logs") {
          const previousLogs = this.logs[update.serviceId];
          this.logs = {
            ...this.logs,
            [update.serviceId]: {
              loading: false,
              entries: previousLogs?.entries ?? [],
              nextCursor: previousLogs?.nextCursor,
              error:
                next[update.serviceId].detail ??
                tr("ui.services.serviceLogCollectionDidNot"),
            },
          };
        }
      }
      this.jobs = next;
      if (refreshInventory) void this.#hooks.refresh();
      for (const log of completedLogs) {
        void this.#loadLogs(log.serviceId, log.cursor);
      }
    } finally {
      this.#polling = false;
    }
  }

  async #checkConvergence(): Promise<void> {
    await this.#hooks.refresh();
    if (!this.#active) return;
    const next = { ...this.jobs };
    for (const [serviceId, action] of Object.entries(next)) {
      if (!action.converging) continue;
      if (
        action.convergenceDeadline !== undefined &&
        Date.now() >= action.convergenceDeadline
      ) {
        const detail = tr("ui.services.actionCompletedButNoNewer");
        next[serviceId] = {
          ...action,
          converging: false,
          reasonCode: "inventory_observation_timeout",
          retryable: true,
          detail,
        };
        this.#touchHistory(action.historyId, { detail });
        continue;
      }
      const observed = this.#hooks.observedRevision(serviceId);
      if (
        observed !== undefined &&
        observed > action.expectedInventoryRevision
      ) {
        const detail = tr("ui.services.actionCompletedAndFreshInventory");
        next[serviceId] = { ...action, converging: false, detail };
        this.#touchHistory(action.historyId, { detail });
      }
    }
    this.jobs = next;
  }
}
