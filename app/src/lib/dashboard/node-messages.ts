/**
 * What a Node card (or the list's status strip) has to say. A message is one
 * compact line, never the full guidance: the card shows `summary` in a fixed
 * status line and the details open only on click, as an overlay.
 */
export type NodeMessageTone = "error" | "warning" | "info";

export interface NodeMessage<Kind extends string = string> {
  /** Stable per message source, so a refresh keeps an open overlay. */
  id: string;
  kind: Kind;
  tone: NodeMessageTone;
  /** One localized line; the status line truncates it. */
  summary: string;
}

const SEVERITY: Record<NodeMessageTone, number> = {
  error: 0,
  warning: 1,
  info: 2,
};

/** Most severe first; equal severity keeps the order the sources gave. */
export function bySeverity<T extends NodeMessage>(messages: readonly T[]): T[] {
  return [...messages].sort(
    (left, right) => SEVERITY[left.tone] - SEVERITY[right.tone],
  );
}
