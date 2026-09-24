/**
 * context.dev Logo Link: public, credit-free brand icons by vendor domain.
 * Same contract as kombify-Paperwork (`apps/client/src/lib/brand-logo.ts`).
 * Docs: https://docs.context.dev/guides/get-logo-from-url
 */

export const BRAND_LOGO_CONTEXT = "techstack-brand-logo-domain";

export interface BrandLogoContext {
  readonly domain: string;
}

const LOGO_LINK_HOST = "https://logos.context.dev/";

const TOOL_BRAND_DOMAINS: Record<string, string> = {
  coolify: "coolify.io",
  "pocket-id": "pocket-id.org",
  pocketid: "pocket-id.org",
  pocket_id: "pocket-id.org",
  id: "pocket-id.org",
  tinyauth: "tinyauth.app",
  auth: "tinyauth.app",
  homepage: "gethomepage.dev",
  home: "gethomepage.dev",
  traefik: "traefik.io",
  vaultwarden: "vaultwarden.org",
  jellyfin: "jellyfin.org",
  immich: "immich.app",
  nextcloud: "nextcloud.com",
  "uptime-kuma": "uptime.kuma.pet",
  kuma: "uptime.kuma.pet",
  dozzle: "dozzle.dev",
  "adguard-home": "adguard.com",
  adguard: "adguard.com",
  "home-assistant": "home-assistant.io",
  crowdsec: "crowdsec.net",
  cloudreve: "cloudreve.org",
  files: "cloudreve.org",
  dokploy: "dokploy.com",
  komodo: "komo.do",
  whoami: "traefik.io",
  base: "kombify.io",
  dashboard: "kombify.io",
  hub: "kombify.io",
  "node-hub": "kombify.io",
  "kombify-point": "kombify.io",
  "step-ca": "smallstep.com",
  lldap: "lldap.dev",
  unbound: "nlnetlabs.nl",
  // StackKits use-case catalog components (stackkits-use-case-catalog-v1.json)
  // that the wizard's use-case cards show. Keyed by the catalog component id.
  ollama: "ollama.com",
  "open-webui": "openwebui.com",
  gitea: "gitea.com",
  stalwart: "stalw.art",
  guacamole: "guacamole.apache.org",
  "paperless-ngx": "docs.paperless-ngx.com",
  paperwork: "kombify.io",
  pterodactyl: "pterodactyl.io",
  roundcube: "roundcube.net",
  mailcow: "mailcow.email",
  n8n: "n8n.io",
  activepieces: "activepieces.com",
};

function normalizeToolToken(value: string): string {
  return value
    .trim()
    .toLowerCase()
    .replace(/[\s_]+/g, "-");
}

function normalizePublicDomain(value: string | undefined | null): string {
  const domain = (value || "").trim().toLowerCase().replace(/\.$/, "");
  if (!domain || domain.length > 253) return "";

  const labels = domain.split(".");
  if (labels.length < 2) return "";
  if (
    labels.some(
      (label) =>
        !label ||
        label.length > 63 ||
        !/^[a-z0-9](?:[a-z0-9-]*[a-z0-9])?$/.test(label),
    )
  ) {
    return "";
  }

  // Logo Link expects a public company domain, never a URL, port, IP address,
  // or local hostname. The catalog map above is the authority for which
  // service owns that domain; this guard keeps malformed runtime input from
  // becoming a third-party image request.
  if (labels.every((label) => /^\d+$/.test(label))) return "";
  if (labels.at(-1) === "local") return "";
  return domain;
}

/**
 * Map a StackKits application/service identity to the vendor domain used by
 * Logo Link. Empty when we do not know a durable public domain for the tool.
 */
export function brandDomainForTool(
  ...candidates: Array<string | undefined | null>
): string {
  for (const candidate of candidates) {
    const token = normalizeToolToken(candidate || "");
    if (!token) continue;
    const mapped = TOOL_BRAND_DOMAINS[token];
    if (mapped) return mapped;
  }
  return "";
}

export function logoLinkUrl(
  domain: string | undefined | null,
  publicClientId: string | undefined | null,
  options: { theme?: "light" | "dark"; type?: "icon" | "wordmark" } = {},
): string {
  const d = normalizePublicDomain(domain);
  const id = (publicClientId || "").trim();
  // Kombify identities use our own product assets, never third-party enrichment.
  if (d === "kombify.io" || d.endsWith(".kombify.io")) return "";
  if (!d || !id) return "";
  const query = new URLSearchParams({
    publicClientId: id,
    domain: d,
    type: options.type || "icon",
  });
  if (options.theme) query.set("theme", options.theme);
  return `${LOGO_LINK_HOST}?${query.toString()}`;
}
