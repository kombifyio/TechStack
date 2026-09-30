/**
 * Public documentation links. One place for the docs origin - the app already
 * pointed at it from the footer and the dashboard - so a use case's guide is
 * built here and nowhere else.
 */
export const DOCS_ORIGIN = "https://docs.kombify.io";

/** The use-case overview, the honest fallback when a use case has no guide yet. */
export const USE_CASES_OVERVIEW_PATH = "/guides/stackkits/use-cases/overview";

/**
 * The guide for a use case: its own page when the StackKits catalog names one,
 * else the overview. Never an invented per-use-case URL.
 */
export function useCaseGuideUrl(docsPath?: string): string {
  const path = (docsPath ?? "").trim();
  return `${DOCS_ORIGIN}${path.startsWith("/") ? path : USE_CASES_OVERVIEW_PATH}`;
}

/** A docs page (with optional `#anchor`) as an absolute docs.kombify.io URL. */
export function docsUrl(path: string): string {
  return `${DOCS_ORIGIN}${path.startsWith("/") ? path : `/${path}`}`;
}

/**
 * Guides behind the Node step's info tips. The anchors are the pages' H2
 * slugs; a renamed heading still lands on the right page. Proxmox has no
 * public guide (the docs safety policy keeps it off docs.kombify.io), so its
 * tips point at the Node overview or carry no link.
 */
export const NODE_GUIDES = {
  sources: "/techstack/add-node#where-the-node-comes-from",
  system: "/techstack/add-node#operating-system",
  nodeConfiguration: "/techstack/add-node#node-configuration",
  pairing: "/techstack/pairing-command",
  sshHost: "/techstack/ssh-connection#host-or-ip-address",
  sshAuth: "/techstack/ssh-connection#ssh-key-or-password",
  sshKeyLabel: "/techstack/ssh-connection#ssh-key-label",
  sshDetails: "/techstack/ssh-connection#ssh-user-port-and-sudo",
  sshTest: "/techstack/ssh-connection#test-connection",
  managed: "/techstack/managed-servers",
  managedProviders: "/techstack/managed-servers#providers",
  basementKit: "/stackkits/kits/basement-kit",
  cloudKit: "/stackkits/kits/cloud-kit",
  roleFoundation: "/techstack/node-roles#foundation-node",
  roleWorker: "/techstack/node-roles#worker-node",
  roleStorage: "/techstack/node-roles#storage-node",
  joinStackKit: "/techstack/node-roles#joining-an-existing-stackkit",
  newStackKit: "/techstack/node-roles#starting-a-new-stackkit",
} as const;
