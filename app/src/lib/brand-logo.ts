/**
 * Brand icons by vendor domain. The tools the wizard and inventory show ship
 * as local assets under /brand-logos (mostly homarr-labs/dashboard-icons,
 * Apache-2.0; provider and kombify icons from their own sources), so a real
 * logo never depends on a third-party quota. context.dev Logo Link remains the
 * fallback for domains without a local asset; its public client has a request
 * quota and answers 402 `quota_exceeded` once it is spent.
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
  // Immich variants and add-ons share the Immich mark.
  "immich-lite": "immich.app",
  "immich-kiosk": "immich.app",
  "immich-power-tools": "immich.app",
  "immich-public-proxy": "immich.app",
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
  paperwork: "paperwork.kombify.io",
  "ente-photos": "ente.io",
  ente: "ente.io",
  "euro-office": "euro-office.eu",
  eurooffice: "euro-office.eu",
  onlyoffice: "onlyoffice.com",
  pterodactyl: "pterodactyl.io",
  roundcube: "roundcube.net",
  mailcow: "mailcow.email",
  n8n: "n8n.io",
  activepieces: "activepieces.com",
  // Hermes Agent (Nous Research); its gateway and web UI units share the mark.
  hermes: "nousresearch.com",
  "hermes-agent": "nousresearch.com",
  forgejo: "forgejo.org",
  passbolt: "passbolt.com",
  emby: "emby.media",
  navidrome: "navidrome.org",
  audiobookshelf: "audiobookshelf.org",
  esphome: "esphome.io",
  mosquitto: "mosquitto.org",
  zigbee2mqtt: "zigbee2mqtt.io",
  pelican: "pelican.dev",
  "pelican-panel": "pelican.dev",
};

/**
 * Generic aliases that map a StackKits component id to a vendor ("id" is
 * Pocket ID). They only count as an exact identity, never as a fragment left
 * after stripping a runtime name, so "home-server" does not become Homepage.
 */
const EXACT_ONLY_TOKENS = new Set([
  "id",
  "auth",
  "home",
  "files",
  "base",
  "dashboard",
  "hub",
  "whoami",
]);

/** Unit suffixes a host runtime appends (`hermes-gateway.service`). */
const UNIT_SUFFIX = /\.(service|socket|container|scope|timer)$/;
/** Component role suffixes around the product name (`hermes-webui`). */
const ROLE_SUFFIX =
  /-(webui|web|ui|gateway|server|app|frontend|backend|worker|api|proxy|db|database|redis|postgres|ml|machine-learning)$/;
/** Replica or version tails (`immich-server-1`, `gitea-v1.22`). */
const VERSION_SUFFIX = /-v?\d+(?:\.\d+)*$/;

/** Bundled icons by vendor domain, served from app/static/brand-logos. */
const LOCAL_BRAND_LOGOS: Record<string, string> = {
  "activepieces.com": "activepieces.svg",
  "audiobookshelf.org": "audiobookshelf.svg",
  "emby.media": "emby.svg",
  "esphome.io": "esphome.svg",
  "forgejo.org": "forgejo.svg",
  "mosquitto.org": "mosquitto.svg",
  "navidrome.org": "navidrome.svg",
  "passbolt.com": "passbolt.svg",
  "pelican.dev": "pelican-panel.svg",
  "zigbee2mqtt.io": "zigbee2mqtt.svg",
  "adguard.com": "adguard-home.svg",
  "centron.de": "centron.svg",
  "cloudreve.org": "cloudreve.png",
  "coolify.io": "coolify.svg",
  "crowdsec.net": "crowdsec.svg",
  "docs.paperless-ngx.com": "paperless-ngx.svg",
  "dokploy.com": "dokploy.svg",
  "dozzle.dev": "dozzle.svg",
  "ente.io": "ente-photos.png",
  "euro-office.eu": "euro-office.svg",
  "kombify.io": "kombify.png",
  "nousresearch.com": "hermes.svg",
  "gethomepage.dev": "homepage.png",
  "gitea.com": "gitea.svg",
  "guacamole.apache.org": "apache-guacamole.svg",
  "home-assistant.io": "home-assistant.svg",
  "immich.app": "immich.svg",
  "ionos.com": "ionos.svg",
  "jellyfin.org": "jellyfin.svg",
  "komo.do": "komodo.png",
  "lldap.dev": "lldap.svg",
  "mailcow.email": "mailcow.svg",
  "n8n.io": "n8n.svg",
  "nextcloud.com": "nextcloud.svg",
  "nlnetlabs.nl": "unbound.svg",
  "ollama.com": "ollama.svg",
  "onlyoffice.com": "onlyoffice.svg",
  "openwebui.com": "open-webui.svg",
  "paperwork.kombify.io": "paperwork.png",
  "pocket-id.org": "pocket-id.svg",
  "pterodactyl.io": "pterodactyl.svg",
  "roundcube.net": "roundcube.svg",
  "smallstep.com": "step-ca.svg",
  "stalw.art": "stalwart.svg",
  "tinyauth.app": "tinyauth.svg",
  "traefik.io": "traefik.svg",
  "uptime.kuma.pet": "uptime-kuma.svg",
  "vaultwarden.org": "vaultwarden.svg",
};

/** The bundled icon for a vendor domain, or "" when none ships. */
export function localBrandLogoUrl(domain: string | undefined | null): string {
  const file = LOCAL_BRAND_LOGOS[normalizePublicDomain(domain)];
  return file ? `/brand-logos/${file}` : "";
}

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
 * The name variants of one runtime identity, most specific first. A container
 * image contributes its repository name and its owner (`ghcr.io/immich-app/
 * immich-server:v1` gives `immich-server` and `immich-app`); a compose or
 * unit name loses its stack prefix, unit suffix, role suffix and version.
 */
function toolTokenVariants(value: string): string[] {
  const raw = value.trim().toLowerCase();
  if (!raw) return [];
  const bases: string[] = [];
  if (raw.includes("/")) {
    const path = raw.replace(/@sha256:[a-f0-9]+$/, "").replace(/:[^/]+$/, "");
    const segments = path.split("/").filter(Boolean);
    const repo = segments.at(-1);
    if (repo) bases.push(repo);
    // The first segment of a registry path is the registry host.
    const owner = segments.length >= 3 ? segments.at(-2) : segments.at(0);
    if (owner && owner !== repo && segments.length >= 2) bases.push(owner);
  } else {
    bases.push(raw.replace(/:[^:]+$/, ""));
  }
  const variants: string[] = [];
  const add = (token: string) => {
    if (token && !variants.includes(token)) variants.push(token);
  };
  const stripped: string[] = [];
  for (const base of bases) {
    let token = normalizeToolToken(base).replace(UNIT_SUFFIX, "");
    stripped.push(token);
    for (let step = 0; step < 3; step += 1) {
      const next = token.replace(VERSION_SUFFIX, "").replace(ROLE_SUFFIX, "");
      if (next === token) break;
      token = next;
      stripped.push(token);
    }
  }
  stripped.forEach(add);
  // Compose names carry the stack in front (`media-jellyfin`).
  for (const token of stripped) {
    const parts = token.split("-");
    for (let start = 1; start < parts.length; start += 1) {
      add(parts.slice(start).join("-"));
    }
  }
  return variants;
}

function domainForToken(token: string, exact: boolean): string {
  if (token === "kombify" || token.startsWith("kombify-")) return "kombify.io";
  if (!exact && EXACT_ONLY_TOKENS.has(token)) return "";
  return TOOL_BRAND_DOMAINS[token] ?? "";
}

/**
 * Map a StackKits application/service identity to the vendor domain used by
 * Logo Link. Candidates are tried in order: first as exact identities, then
 * through their normalized name variants. Empty when we do not know a durable
 * public domain for the tool.
 */
export function brandDomainForTool(
  ...candidates: Array<string | undefined | null>
): string {
  const values = candidates.map((candidate) => (candidate || "").trim());
  for (const value of values) {
    const mapped = value && domainForToken(normalizeToolToken(value), true);
    if (mapped) return mapped;
  }
  for (const value of values) {
    for (const token of toolTokenVariants(value)) {
      const mapped = domainForToken(token, false);
      if (mapped) return mapped;
    }
  }
  return "";
}

/** Compact illustrations show one known logo; unknown tools keep their identity. */
export function distinctToolLogos<T extends { id: string; name: string }>(
  tools: readonly T[],
  preferredId: string,
): T[] {
  const seen = new Set<string>();
  const ordered = [...tools].sort(
    (a, b) => Number(b.id === preferredId) - Number(a.id === preferredId),
  );
  return ordered.filter((tool) => {
    const domain = brandDomainForTool(tool.id, tool.name);
    const key = domain ? `brand:${domain}` : `tool:${tool.id}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
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
