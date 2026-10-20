import type {
  LayoutAssignmentView,
  LayoutNodeView,
  LayoutRecommendationView,
} from "#lib/api/layout.js";

export interface LayoutNodeGroup {
  node: LayoutNodeView;
  assignments: LayoutAssignmentView[];
}

/**
 * The layout grouped per node, in the server's node order with servers that
 * receive nothing new last, so the first group is where something happens.
 */
export function layoutNodeGroups(
  view: LayoutRecommendationView,
): LayoutNodeGroup[] {
  const groups = view.nodes.map((node) => ({
    node,
    assignments: view.assignments.filter(
      (assignment) => assignment.node_id === node.id,
    ),
  }));
  return [
    ...groups.filter((group) => group.assignments.length > 0),
    ...groups.filter((group) => group.assignments.length === 0),
  ];
}

/** A workload id as words ("home-assistant" -> "Home assistant"). */
export function appLabel(workload: string): string {
  const words = workload.replace(/[-_]+/g, " ").trim();
  return words ? words[0]!.toUpperCase() + words.slice(1) : workload;
}

/** Memory in use of a node's capacity, or undefined while the node has not reported its size. */
export function memoryHint(
  node: LayoutNodeView,
): { used: number; total: number } | undefined {
  const total = node.capacity?.ramGB ?? 0;
  if (!(total > 0)) return undefined;
  const used = Math.min(Math.max(node.reserved?.ramGB ?? 0, 0), total);
  return { used, total };
}
