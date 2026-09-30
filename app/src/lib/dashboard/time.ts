import { formatDateTime, tr } from "#lib/i18n.svelte.js";

/** "5 min ago" style relative time for owner-facing timestamps. */
export function formatRelative(value: string, now = Date.now()): string {
  const at = Date.parse(value);
  if (Number.isNaN(at)) return tr("ui.time.notReported");
  const seconds = Math.round((now - at) / 1000);
  if (seconds < 60) return tr("ui.time.justNow");
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return tr("ui.time.minutesAgo", { count: minutes });
  const hours = Math.round(minutes / 60);
  if (hours < 48) return tr("ui.time.hoursAgo", { count: hours });
  return formatDateTime(at, { dateStyle: "medium" });
}

/** "14:05" in the active locale. */
export function formatClock(value: string | number): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return "";
  return formatDateTime(date, { hour: "2-digit", minute: "2-digit" });
}

/** "1 h 05" style duration for monitoring windows and downtime episodes. */
export function formatDuration(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined) return "—";
  if (seconds < 60)
    return tr("ui.time.seconds", { count: Math.round(seconds) });
  const minutes = Math.round(seconds / 60);
  if (minutes < 60) return tr("ui.time.minutes", { count: minutes });
  const hours = Math.floor(minutes / 60);
  const rest = minutes % 60;
  if (hours < 24) {
    return rest
      ? tr("ui.time.hoursMinutes", {
          hours,
          minutes: String(rest).padStart(2, "0"),
        })
      : tr("ui.time.hours", { count: hours });
  }
  return tr("ui.time.daysHours", {
    days: Math.floor(hours / 24),
    hours: hours % 24,
  });
}
