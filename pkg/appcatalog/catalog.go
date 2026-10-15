// Package appcatalog recognizes well-known self-hosted applications from what
// a host actually reports (image, container name, compose service, systemd
// unit) and decides which Application a discovered runtime unit belongs to.
//
// # Authority
//
// The catalog is a data-driven hint table, not a source of truth about what is
// running: it only labels something the agent already measured. A unit that
// matches no entry is never guessed; it is either its own application (a plain
// container) or part of the System bucket.
//
// # Shared use
//
// The Guard agent and the control plane call the same Group function, so a new
// agent reports the right application key and an already persisted row that
// carries the old `system` key is reclassified on read without an agent update.
//
// # Side effects
//
// Matching and grouping are pure. Only UpdateChecker performs network I/O.
package appcatalog

import (
	"regexp"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
)

const (
	// SystemKey is the bucket for infrastructure and unrecognized units.
	SystemKey = "system"
	// SystemDisplayName is the only name the System bucket ever carries.
	SystemDisplayName = "System Services"

	platformDocker  = "docker"
	platformSystemd = "systemd"

	categoryDatabase      = "database"
	platformProjectPrefix = "stackkit-"
)

// Entry is one recognized application. The JSON shape is the `catalog` object
// of the service read model.
type Entry struct {
	ID          string                              `json:"id"`
	DisplayName string                              `json:"display_name"`
	Category    string                              `json:"category"`
	BrandDomain string                              `json:"brand_domain,omitempty"`
	IconURL     string                              `json:"icon_url"`
	WebPort     int                                 `json:"web_port,omitempty"`
	GuideURL    string                              `json:"guide_url,omitempty"`
	Clients     []stackkitrelease.ApplicationClient `json:"clients,omitempty"`
	Vendor      *stackkitrelease.ApplicationVendor  `json:"vendor,omitempty"`
	// Infra entries are plumbing (proxy, updater, agent) and stay in the System
	// bucket instead of becoming an Application.
	Infra bool `json:"-"`

	// images match the image repository, either whole ("grafana/grafana") or by
	// its last path segment ("grafana"). names match contiguous name tokens of a
	// container, compose service or systemd unit.
	images []string
	names  []string
}

// entries is ordered by preference: on a tie in match specificity the earlier
// entry wins, so databases come last and a container called `immich-postgres`
// resolves to Immich, not PostgreSQL.
var entries = []Entry{
	{ID: "hermes", WebPort: 8787,
		images: []string{"nousresearch/hermes-agent", "hermes-agent", "hermes-webui"},
		names:  []string{"hermes-webui", "hermes-agent", "hermes"}},
	{ID: "open-webui", WebPort: 8080,
		images: []string{"open-webui/open-webui", "open-webui"}, names: []string{"open-webui"}},
	{ID: "ollama", WebPort: 11434,
		images: []string{"ollama/ollama", "ollama"}, names: []string{"ollama"}},
	{ID: "home-assistant", WebPort: 8123,
		images: []string{"home-assistant/home-assistant", "homeassistant/home-assistant", "home-assistant"},
		names:  []string{"home-assistant", "homeassistant"}},
	{ID: "nextcloud", WebPort: 80,
		images: []string{"nextcloud", "nextcloud/all-in-one"}, names: []string{"nextcloud"}},
	{ID: "immich", WebPort: 2283,
		images: []string{"immich-app/immich-server", "immich-server", "immich-machine-learning"},
		names:  []string{"immich-server", "immich"}},
	{ID: "jellyfin", WebPort: 8096,
		images: []string{"jellyfin/jellyfin", "jellyfin"}, names: []string{"jellyfin"}},
	{ID: "plex", DisplayName: "Plex", Category: "media", BrandDomain: "plex.tv", WebPort: 32400,
		images: []string{"plexinc/pms-docker", "plex"}, names: []string{"plex", "plexmediaserver"}},
	{ID: "vaultwarden", WebPort: 80,
		images: []string{"vaultwarden/server", "vaultwarden"}, names: []string{"vaultwarden"}},
	{ID: "n8n", DisplayName: "n8n", Category: "productivity", BrandDomain: "n8n.io", WebPort: 5678,
		images: []string{"n8nio/n8n", "n8n"}, names: []string{"n8n"}},
	{ID: "grafana", DisplayName: "Grafana", Category: "monitoring", BrandDomain: "grafana.com", WebPort: 3000,
		images: []string{"grafana/grafana", "grafana-oss", "grafana-enterprise"}, names: []string{"grafana", "grafana-server"}},
	{ID: "prometheus", DisplayName: "Prometheus", Category: "monitoring", BrandDomain: "prometheus.io", WebPort: 9090,
		images: []string{"prom/prometheus", "prometheus"}, names: []string{"prometheus"}},
	{ID: "uptime-kuma", DisplayName: "Uptime Kuma", Category: "monitoring", BrandDomain: "uptime.kuma.pet", WebPort: 3001,
		images: []string{"louislam/uptime-kuma", "uptime-kuma"}, names: []string{"uptime-kuma", "uptimekuma"}},
	{ID: "portainer", DisplayName: "Portainer", Category: "dev", BrandDomain: "portainer.io", WebPort: 9000,
		images: []string{"portainer/portainer-ce", "portainer/portainer-ee", "portainer"}, names: []string{"portainer"}},
	{ID: "pi-hole", DisplayName: "Pi-hole", Category: "network", BrandDomain: "pi-hole.net", WebPort: 80,
		images: []string{"pihole/pihole", "pihole"}, names: []string{"pihole", "pi-hole"}},
	{ID: "adguard-home", DisplayName: "AdGuard Home", Category: "network", BrandDomain: "adguard.com", WebPort: 3000,
		images: []string{"adguard/adguardhome", "adguardhome"}, names: []string{"adguardhome", "adguard-home", "adguard"}},
	{ID: "paperless-ngx", WebPort: 8000,
		images: []string{"paperless-ngx/paperless-ngx", "paperless-ngx"}, names: []string{"paperless-ngx", "paperless"}},
	{ID: "gitea", WebPort: 3000,
		images: []string{"gitea/gitea", "gitea"}, names: []string{"gitea"}},
	{ID: "forgejo", WebPort: 3000,
		images: []string{"forgejo/forgejo", "forgejo"}, names: []string{"forgejo"}},
	{ID: "syncthing", DisplayName: "Syncthing", Category: "files", BrandDomain: "syncthing.net", WebPort: 8384,
		images: []string{"syncthing/syncthing", "syncthing"}, names: []string{"syncthing"}},
	{ID: "nginx-proxy-manager", DisplayName: "Nginx Proxy Manager", Category: "network", BrandDomain: "nginxproxymanager.com", WebPort: 81,
		images: []string{"jc21/nginx-proxy-manager", "nginx-proxy-manager"}, names: []string{"nginx-proxy-manager"}},
	{ID: "code-server", DisplayName: "code-server", Category: "dev", BrandDomain: "coder.com", WebPort: 8080,
		images: []string{"codercom/code-server", "coder/code-server", "code-server"}, names: []string{"code-server"}},
	{ID: "librechat", DisplayName: "LibreChat", Category: "ai", BrandDomain: "librechat.ai", WebPort: 3080,
		images: []string{"danny-avila/librechat", "danny-avila/librechat-dev", "librechat/librechat", "librechat"},
		names:  []string{"librechat"}},
	{ID: "pocket-id", DisplayName: "Pocket ID", Category: "security", BrandDomain: "pocket-id.org", WebPort: 1411,
		images: []string{"pocket-id/pocket-id", "stonith404/pocket-id", "pocket-id"}, names: []string{"pocket-id", "pocketid"}},
	{ID: "homepage", DisplayName: "Homepage", Category: "dashboard", BrandDomain: "gethomepage.dev", WebPort: 3000,
		images: []string{"gethomepage/homepage", "benphelps/homepage"}, names: []string{"homepage"}},
	{ID: "minio", DisplayName: "MinIO", Category: "files", BrandDomain: "min.io", WebPort: 9001,
		images: []string{"minio/minio", "minio"}, names: []string{"minio"}},

	// Infrastructure: recognized so it stays in the System bucket.
	{ID: "traefik", DisplayName: "Traefik", Category: "infra", BrandDomain: "traefik.io", Infra: true,
		images: []string{"traefik"}, names: []string{"traefik"}},
	{ID: "caddy", DisplayName: "Caddy", Category: "infra", BrandDomain: "caddyserver.com", Infra: true,
		images: []string{"caddy"}, names: []string{"caddy"}},
	{ID: "dozzle", DisplayName: "Dozzle", Category: "infra", Infra: true,
		images: []string{"amir20/dozzle", "dozzle"}, names: []string{"dozzle"}},
	{ID: "watchtower", DisplayName: "Watchtower", Category: "infra", Infra: true,
		images: []string{"containrrr/watchtower", "nickfedor/watchtower", "watchtower"}, names: []string{"watchtower"}},
	{ID: "cups", DisplayName: "CUPS", Category: "infra", Infra: true,
		images: []string{"cups"}, names: []string{"cups", "cupsd"}},
	{ID: "cloudflared", DisplayName: "Cloudflare Tunnel", Category: "infra", Infra: true,
		images: []string{"cloudflare/cloudflared", "cloudflared"}, names: []string{"cloudflared"}},
	{ID: "tailscale", DisplayName: "Tailscale", Category: "infra", Infra: true,
		images: []string{"tailscale/tailscale", "tailscale"}, names: []string{"tailscale", "tailscaled"}},
	{ID: "kombify-agent", DisplayName: "Kombify Agent", Category: "infra", Infra: true,
		names: []string{"techstack-agent", "kombify-guard", "kombify-agent"}},
	{ID: "portainer-agent", DisplayName: "Portainer Agent", Category: "infra", Infra: true,
		images: []string{"portainer/agent"}, names: []string{"portainer-agent"}},
	{ID: "cadvisor", DisplayName: "cAdvisor", Category: "infra", Infra: true,
		images: []string{"cadvisor"}, names: []string{"cadvisor"}},
	{ID: "node-exporter", DisplayName: "Node Exporter", Category: "infra", Infra: true,
		images: []string{"prom/node-exporter", "node-exporter"}, names: []string{"node-exporter"}},
	{ID: "tinyauth", DisplayName: "Tinyauth", Category: "infra", BrandDomain: "tinyauth.app", Infra: true,
		images: []string{"steveiliop56/tinyauth", "tinyauth"}, names: []string{"tinyauth"}},
	{ID: "crowdsec", DisplayName: "CrowdSec", Category: "infra", BrandDomain: "crowdsec.net", Infra: true,
		images: []string{"crowdsecurity/crowdsec", "crowdsec"}, names: []string{"crowdsec"}},
	{ID: "whoami", DisplayName: "whoami", Category: "infra", Infra: true,
		images: []string{"traefik/whoami", "whoami"}, names: []string{"whoami"}},
	{ID: "step-ca", DisplayName: "step-ca", Category: "infra", BrandDomain: "smallstep.com", Infra: true,
		images: []string{"smallstep/step-ca", "step-ca"}, names: []string{"step-ca"}},
	{ID: "lldap", DisplayName: "LLDAP", Category: "infra", BrandDomain: "lldap.dev", Infra: true,
		images: []string{"lldap/lldap", "nitnelave/lldap", "lldap"}, names: []string{"lldap"}},
	{ID: "unbound", DisplayName: "Unbound", Category: "infra", BrandDomain: "nlnetlabs.nl", Infra: true,
		images: []string{"mvance/unbound", "klutchell/unbound", "unbound"}, names: []string{"unbound"}},
	{ID: "kombify-hub", DisplayName: "kombify Hub", Category: "infra", BrandDomain: "kombify.io", Infra: true,
		names: []string{"stackkit-hub", "node-hub", "kombify-hub", "kombify-point"}},
	{ID: "docker-socket-proxy", DisplayName: "Docker Socket Proxy", Category: "infra", Infra: true,
		images: []string{"tecnativa/docker-socket-proxy", "docker-socket-proxy"}, names: []string{"docker-socket-proxy"}},

	// Databases last: see the ordering note on entries.
	{ID: "postgres", DisplayName: "PostgreSQL", Category: "database", BrandDomain: "postgresql.org",
		images: []string{"postgres", "postgresql", "pgvector/pgvector", "postgis/postgis"}, names: []string{"postgres", "postgresql"}},
	{ID: "mariadb", DisplayName: "MariaDB", Category: "database", BrandDomain: "mariadb.org",
		images: []string{"mariadb"}, names: []string{"mariadb"}},
	{ID: "mysql", DisplayName: "MySQL", Category: "database", BrandDomain: "mysql.com",
		images: []string{"mysql", "mysql/mysql-server"}, names: []string{"mysql"}},
	{ID: "redis", DisplayName: "Redis", Category: "database", BrandDomain: "redis.io",
		images: []string{"redis", "redis/redis-stack"}, names: []string{"redis"}},
}

// Subject is what one discovered runtime unit reports about itself.
type Subject struct {
	// Name is the container name or systemd unit file name.
	Name string
	// Image is the container image reference; empty for a systemd unit.
	Image string
	// Platform is "docker" or "systemd". Empty is derived from Image.
	Platform       string
	ComposeProject string
	ComposeService string
}

// Grouping is the application a unit belongs to.
type Grouping struct {
	Key         string
	DisplayName string
	// Entry is the catalog recognition of the unit itself, independent of which
	// bucket it landed in (a recognized container inside a compose project keeps
	// its catalog entry as a component label).
	Entry *Entry
}

// Group applies steps 2 to 5 of the application grouping rule. Step 1, an
// explicit non-system application key, is the caller's.
//
//  2. a compose project is the operator's own application boundary, except a
//     StackKit platform project, which bundles many applications;
//  3. a catalog match becomes that catalog application (infra stays in System);
//  4. any other docker container is its own application keyed by its name;
//  5. everything else (unrecognized systemd units, infra) is the System bucket.
func Group(subject Subject) Grouping {
	entry := Match(subject)
	project := strings.TrimSpace(subject.ComposeProject)
	if project != "" && IsPlatformProject(project) {
		return groupPlatformComponent(subject, entry)
	}
	if project != "" {
		// A compose project named after an application (hermes-webui,
		// librechat-k4es) is that catalog application as a whole: its database
		// and sidecars join the same card, and a unit of the same application
		// outside the project lands there too instead of standing next to it
		// under its folder name.
		if named := Match(Subject{Name: project}); named != nil && !named.Infra && named.Category != categoryDatabase {
			return Grouping{Key: named.ID, DisplayName: named.DisplayName, Entry: entry}
		}
		return Grouping{Key: strings.ToLower(project), DisplayName: project, Entry: entry}
	}
	if entry != nil {
		if entry.Infra {
			return Grouping{Key: SystemKey, DisplayName: SystemDisplayName, Entry: entry}
		}
		return Grouping{Key: entry.ID, DisplayName: entry.DisplayName, Entry: entry}
	}
	if isDocker(subject) {
		if key := ContainerKey(subject.Name); key != "" {
			return Grouping{Key: key, DisplayName: strings.TrimPrefix(strings.TrimSpace(subject.Name), "/")}
		}
	}
	return Grouping{Key: SystemKey, DisplayName: SystemDisplayName}
}

// IsPlatformProject reports whether a compose project is one a StackKit rolls
// out for its platform (stackkit-cloud-core, stackkit-basement-core, ...).
// Such a project carries the proxy, identity and every selected application
// at once, so its name is never an application name.
func IsPlatformProject(project string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(project)), platformProjectPrefix)
}

// groupPlatformComponent places one unit of a StackKit platform project: a
// recognized application is that application, infrastructure and a bare
// database are System, and any other compose service is its own application
// named after the service.
func groupPlatformComponent(subject Subject, entry *Entry) Grouping {
	if entry != nil {
		if entry.Infra || entry.Category == categoryDatabase {
			return Grouping{Key: SystemKey, DisplayName: SystemDisplayName, Entry: entry}
		}
		return Grouping{Key: entry.ID, DisplayName: entry.DisplayName, Entry: entry}
	}
	service := strings.TrimSpace(subject.ComposeService)
	if key := ContainerKey(service); key != "" {
		return Grouping{Key: key, DisplayName: service}
	}
	return Grouping{Key: SystemKey, DisplayName: SystemDisplayName}
}

func isDocker(subject Subject) bool {
	switch strings.ToLower(strings.TrimSpace(subject.Platform)) {
	case platformDocker:
		return true
	case platformSystemd:
		return false
	}
	return strings.TrimSpace(subject.Image) != ""
}

var containerKeyUnsafe = regexp.MustCompile(`[^a-z0-9._-]+`)

// ContainerKey normalizes a container name into a stable application key.
func ContainerKey(name string) string {
	key := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(name), "/")))
	key = containerKeyUnsafe.ReplaceAllString(key, "-")
	return strings.Trim(key, "-._")
}

// Lookup returns a catalog entry by id.
func Lookup(id string) *Entry {
	id = strings.ToLower(strings.TrimSpace(id))
	for i := range entries {
		if entries[i].ID == id {
			return copyEntry(&entries[i])
		}
	}
	return releasedCatalogEntry(id)
}

// Match recognizes a unit by image first (the most reliable evidence) and then
// by name. The most specific pattern wins; the earlier entry wins a tie.
func Match(subject Subject) *Entry {
	if repo := ParseImage(subject.Image).Repo; repo != "" {
		if entry := bestImageMatch(repo); entry != nil {
			return entry
		}
	}
	candidates := []string{subject.Name, subject.ComposeService}
	for _, candidate := range candidates {
		candidate = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(candidate)), ".service")
		if candidate == "" {
			continue
		}
		if released := Lookup(candidate); released != nil {
			return released
		}
		if entry := bestNameMatch(tokenize(candidate)); entry != nil {
			return entry
		}
	}
	return nil
}

func bestImageMatch(repo string) *Entry {
	repo = strings.TrimPrefix(repo, "library/")
	var best *Entry
	bestScore := 0
	for i := range entries {
		for _, pattern := range entries[i].images {
			if repo != pattern && !strings.HasSuffix(repo, "/"+pattern) {
				continue
			}
			if score := len(pattern); score > bestScore {
				best, bestScore = &entries[i], score
			}
		}
	}
	return copyEntry(best)
}

func bestNameMatch(name []string) *Entry {
	var best *Entry
	bestScore := 0
	for i := range entries {
		for _, pattern := range entries[i].names {
			tokens := tokenize(pattern)
			if !containsTokens(name, tokens) {
				continue
			}
			if score := len(tokens); score > bestScore {
				best, bestScore = &entries[i], score
			}
		}
	}
	return copyEntry(best)
}

func copyEntry(entry *Entry) *Entry {
	if entry == nil {
		return nil
	}
	cloned := *entry
	if released := releasedCatalogEntry(entry.ID); released != nil {
		cloned.DisplayName = released.DisplayName
		cloned.Category = released.Category
		cloned.BrandDomain = released.BrandDomain
	} else if cloned.DisplayName == "" {
		// Recognition is measured discovery, not released installation support.
		cloned.DisplayName = cloned.ID
		cloned.Category = "unknown"
	}
	attachReleasedApplicationPresentation(&cloned)
	return &cloned
}

var nameSplit = regexp.MustCompile(`[^a-z0-9]+`)

func tokenize(name string) []string {
	parts := nameSplit.Split(strings.ToLower(name), -1)
	tokens := parts[:0]
	for _, part := range parts {
		if part != "" {
			tokens = append(tokens, part)
		}
	}
	return tokens
}

// containsTokens reports whether pattern occurs as a contiguous run in name.
func containsTokens(name, pattern []string) bool {
	if len(pattern) == 0 || len(pattern) > len(name) {
		return false
	}
	for start := 0; start+len(pattern) <= len(name); start++ {
		matched := true
		for offset, token := range pattern {
			if name[start+offset] != token {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}
