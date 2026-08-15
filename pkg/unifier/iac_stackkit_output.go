package unifier

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

var baseKitServiceEnableVars = []string{
	"enable_traefik",
	"enable_tinyauth",
	"enable_pocketid",
	"enable_dokploy",
	"enable_dokploy_apps",
	"enable_komodo",
	"enable_dockge",
	"enable_uptime_kuma",
	"enable_whoami",
	"enable_vaultwarden",
	"enable_jellyfin",
	"enable_immich",
	"enable_files",
	"enable_cloudreve",
	"enable_nextcloud",
	"enable_dashboard",
	"enable_homepage",
	"enable_kombify_point",
	"enable_coolify",
}

// GenerateBaseKitTFVars generates terraform.tfvars.json for base-kit StackKit.
func (g *IaCGenerator) GenerateBaseKitTFVars(config *IaCConfig) (*BaseKitTFVars, error) {
	vars := &BaseKitTFVars{
		AccessMode:  "ports",
		Variant:     iacDefaultVariant,
		ComputeTier: iacDefaultComputeTier,

		TraefikDashboardPort: 8080,
		DozzlePort:           8888,
		UptimeKumaPort:       3001,
		BeszelPort:           8090,
		DockgePort:           5001,
		PortainerPort:        9000,
		NetdataPort:          19999,
		WhoamiPort:           9080,

		StacksDir:   "/opt/stacks",
		BindAddress: "0.0.0.0",

		EnableTraefik:     true,
		EnableTinyAuth:    true,
		EnablePocketID:    true,
		EnableUptimeKuma:  true,
		EnableWhoami:      true,
		EnableVaultwarden: true,
		EnableImmich:      true,
		EnableFiles:       true,
		EnableCloudreve:   true,
		EnableDashboard:   true,
		EnableHomepage:    true,
		EnableCoolify:     true,
		FilesProvider:     "cloudreve",
	}
	applyBaseKitServiceEnablement(vars, config.BaseKitServiceEnablement)
	normalizeBaseKitFilesProvider(vars)

	if config.Variant != "" {
		vars.Variant = config.Variant
	}
	if config.ComputeTier != "" {
		vars.ComputeTier = config.ComputeTier
	}
	if config.DockerHost != "" {
		vars.DockerHost = config.DockerHost
	}
	if config.EnablePlatformFallback {
		vars.EnablePlatformFallback = true
		vars.PlatformFallbackMode = firstNonEmptyString(config.PlatformFallbackMode, "standalone-compose")
	} else {
		vars.PlatformFallbackMode = firstNonEmptyString(config.PlatformFallbackMode, "disabled")
	}

	if config.Domain != "" {
		vars.AccessMode = "proxy"
		vars.Domain = config.Domain
		vars.EnableHTTPS = true
	}
	if config.SubdomainPrefix != "" {
		vars.SubdomainPrefix = config.SubdomainPrefix
	}

	applyBaseKitTLSMode(vars, config)

	if config.WorkerIP != "" && config.Domain == "" {
		vars.AdvertiseHost = config.WorkerIP
	}

	if err := populateBaseKitIdentityTFVars(vars, config); err != nil {
		return nil, err
	}

	return vars, nil
}

func applyBaseKitTLSMode(vars *BaseKitTFVars, config *IaCConfig) {
	if vars == nil || config == nil {
		return
	}

	domain := strings.ToLower(strings.TrimSpace(config.Domain))
	if domain == "kombify.me" {
		vars.TLSProvider = "step-ca"
		vars.StepCAEnabled = true
		if vars.ACMEEmail == "" {
			vars.ACMEEmail = baseKitACMEEmail(config)
		}
		if vars.ACMEChallenge == "" {
			vars.ACMEChallenge = "tls"
		}
		return
	}

	acmeRequestedWithoutDomain := domain == "" && (config.TLSMode == iacTLSModeACME || strings.TrimSpace(config.ACMEEmail) != "")
	if isCustomPublicDomain(domain) || acmeRequestedWithoutDomain {
		vars.EnableHTTPS = true
		vars.EnableLetsEnc = true
		vars.TLSProvider = "letsencrypt"
		vars.StepCAEnabled = false
		vars.ACMEEmail = baseKitACMEEmail(config)
		if vars.ACMEChallenge == "" {
			vars.ACMEChallenge = "tls"
		}
	}
}

func baseKitACMEEmail(config *IaCConfig) string {
	if config == nil {
		return baseKitDefaultAdminEmail
	}
	if email := strings.TrimSpace(config.ACMEEmail); email != "" {
		return email
	}
	if email := strings.TrimSpace(config.AdminEmail); email != "" {
		return email
	}
	return baseKitDefaultAdminEmail
}

func applyBaseKitServiceEnablement(vars *BaseKitTFVars, flags map[string]bool) {
	if vars == nil || len(flags) == 0 {
		return
	}
	for key, enabled := range flags {
		switch key {
		case "enable_traefik":
			vars.EnableTraefik = enabled
		case "enable_tinyauth":
			vars.EnableTinyAuth = enabled
		case "enable_pocketid":
			vars.EnablePocketID = enabled
		case "enable_dokploy":
			vars.EnableDokploy = enabled
		case "enable_dokploy_apps":
			vars.EnableDokployApps = enabled
		case "enable_komodo":
			vars.EnableKomodo = enabled
		case "enable_dockge":
			vars.EnableDockge = enabled
		case "enable_uptime_kuma":
			vars.EnableUptimeKuma = enabled
		case "enable_whoami":
			vars.EnableWhoami = enabled
		case "enable_vaultwarden":
			vars.EnableVaultwarden = enabled
		case "enable_jellyfin":
			vars.EnableJellyfin = enabled
		case "enable_immich":
			vars.EnableImmich = enabled
		case "enable_files":
			vars.EnableFiles = enabled
			if !enabled {
				vars.EnableCloudreve = false
				vars.EnableNextcloud = false
			}
		case "enable_cloudreve":
			vars.EnableCloudreve = enabled
			if enabled {
				vars.EnableFiles = true
				vars.EnableNextcloud = false
			}
		case "enable_nextcloud":
			vars.EnableNextcloud = enabled
			if enabled {
				vars.EnableFiles = true
				vars.EnableCloudreve = false
			}
		case "enable_dashboard":
			vars.EnableDashboard = enabled
		case "enable_homepage":
			vars.EnableHomepage = enabled
		case "enable_kombify_point":
			vars.EnableKombifyPoint = enabled
		case "enable_coolify":
			vars.EnableCoolify = enabled
		}
	}
}

func normalizeBaseKitFilesProvider(vars *BaseKitTFVars) {
	if vars == nil {
		return
	}
	if vars.FilesProvider == "" {
		vars.FilesProvider = "cloudreve"
	}
	if !vars.EnableFiles {
		vars.EnableCloudreve = false
		vars.EnableNextcloud = false
		return
	}
	if vars.EnableNextcloud && !vars.EnableCloudreve {
		vars.FilesProvider = "nextcloud"
		return
	}
	vars.FilesProvider = "cloudreve"
	vars.EnableCloudreve = true
	vars.EnableNextcloud = false
}

// GenerateBaseKitOutput generates base-kit output from authoritative StackKits templates.
// It renders templated .tf/.tpl files without falling back to embedded HCL.
func (g *IaCGenerator) GenerateBaseKitOutput(config *IaCConfig, stackkitsDir string) (*IaCOutput, error) {
	return g.generateSingleEnvironmentKitOutput(config, stackkitsDir, StackKitBase)
}

// GenerateCloudKitOutput generates cloud-kit output from authoritative StackKits templates.
func (g *IaCGenerator) GenerateCloudKitOutput(config *IaCConfig, stackkitsDir string) (*IaCOutput, error) {
	return g.generateSingleEnvironmentKitOutput(config, stackkitsDir, StackKitCloud)
}

func (g *IaCGenerator) generateSingleEnvironmentKitOutput(config *IaCConfig, stackkitsDir, stackKit string) (*IaCOutput, error) {
	output := &IaCOutput{Files: make(map[string]string), Mode: g.Mode, OutputDir: g.OutputDir}

	templateDir := filepath.Join(stackkitsDir, stackKit, "templates", iacModeSimple)
	if g.Mode == iacModeAdvanced {
		templateDir = filepath.Join(stackkitsDir, stackKit, "templates", iacModeAdvanced)
	}

	if _, err := os.Stat(templateDir); os.IsNotExist(err) {
		return nil, fmt.Errorf("%s templates not found at %s; set TECHSTACK_STACKKITS_DIR or STACKKITS_REPO to an external StackKits checkout", stackKit, templateDir)
	}

	config.StackKit = stackKit
	tplData := g.toStackKitTemplateData(config)
	if err := g.loadBaseKitTemplateDir(templateDir, tplData, output); err != nil {
		return nil, err
	}

	if g.Mode != iacModeAdvanced {
		if _, ok := output.Files["main.tf"]; !ok {
			return nil, fmt.Errorf("main.tf not found in StackKits template directory: %s", templateDir)
		}
	}

	vars, err := g.GenerateBaseKitTFVars(config)
	if err != nil {
		return nil, fmt.Errorf("failed to generate tfvars: %w", err)
	}

	tfvarsJSON, err := json.MarshalIndent(vars, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal tfvars: %w", err)
	}
	output.Files["terraform.tfvars.json"] = string(tfvarsJSON)
	output.TFVarsJSON = tfvarsJSON

	return output, nil
}

func (g *IaCGenerator) loadBaseKitTemplateDir(templateDir string, tplData *StackKitTemplateData, output *IaCOutput) error {
	entries, err := os.ReadDir(templateDir)
	if err != nil {
		return fmt.Errorf("failed to read StackKits template directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			if g.Mode == iacModeAdvanced {
				if subdirErr := g.loadStackKitSubdir(filepath.Join(templateDir, entry.Name()), entry.Name(), tplData, output); subdirErr != nil {
					output.Warnings = append(output.Warnings, fmt.Sprintf("skipped subdir %s: %v", entry.Name(), subdirErr))
				}
			}
			continue
		}

		name := entry.Name()

		if isInertTemplateEntry(entry) {
			continue
		}

		content, readErr := os.ReadFile(filepath.Join(templateDir, name))
		if readErr != nil {
			return fmt.Errorf("failed to read %s from StackKits: %w", name, readErr)
		}

		contentString := string(content)
		if strings.HasSuffix(name, ".tpl") {
			outputName := templateOutputName(name)
			rendered, renderErr := g.renderStackKitTemplate(outputName, contentString, tplData)
			if renderErr != nil {
				output.Warnings = append(output.Warnings, fmt.Sprintf("failed to render %s: %v", name, renderErr))
				continue
			}
			output.Files[outputName] = rendered
		} else if strings.HasSuffix(name, ".tf") {
			if strings.Contains(contentString, "{{") {
				rendered, renderErr := g.renderStackKitTemplate(name, contentString, tplData)
				if renderErr != nil {
					output.Warnings = append(output.Warnings, fmt.Sprintf("failed to render %s: %v", name, renderErr))
					continue
				}
				output.Files[name] = rendered
				continue
			}
			output.Files[name] = contentString
		}
	}

	return nil
}

func (g *IaCGenerator) toStackKitTemplateData(config *IaCConfig) *StackKitTemplateData {
	data := &StackKitTemplateData{
		StackName:   config.StackName,
		StackID:     config.StackName,
		Environment: "production",
	}
	data.Network.VPNEnabled = config.VPNType != ""
	data.Network.Subnet = config.Subnet
	data.Network.Domain = config.Domain
	data.Catalog = stackKitDefaultCatalog()
	data.Domains = stackKitDefaultDomains(data.Catalog)

	for _, n := range config.Nodes {
		data.Nodes = append(data.Nodes, struct {
			Name     string
			IP       string
			Role     string
			Provider string
		}{
			Name:     n.Name,
			IP:       n.IP,
			Role:     n.Role,
			Provider: "ssh",
		})
	}

	if len(data.Nodes) == 0 && config.WorkerIP != "" {
		data.Nodes = append(data.Nodes, struct {
			Name     string
			IP       string
			Role     string
			Provider string
		}{
			Name:     "primary",
			IP:       config.WorkerIP,
			Role:     iacNodeRoleStandalone,
			Provider: "ssh",
		})
	}

	for _, s := range config.Services {
		svcType := "application"
		if strings.Contains(s.Image, "traefik") {
			svcType = "proxy"
		} else if strings.Contains(s.Image, "prometheus") || strings.Contains(s.Image, "grafana") || strings.Contains(s.Image, "uptime-kuma") {
			svcType = "monitoring"
		}

		nodeName := ""
		if len(data.Nodes) > 0 {
			nodeName = data.Nodes[0].Name
		}

		data.Services = append(data.Services, struct {
			Name  string
			Type  string
			Node  string
			Port  int
			Image string
		}{
			Name:  s.Name,
			Type:  svcType,
			Node:  nodeName,
			Port:  s.Port,
			Image: s.Image,
		})
	}

	return data
}

func (g *IaCGenerator) renderStackKitTemplate(name, tmplStr string, data *StackKitTemplateData) (string, error) {
	t, err := template.New(name).Funcs(stackKitTemplateFuncMap()).Parse(tmplStr)
	if err != nil {
		return "", fmt.Errorf("failed to parse template %s: %w", name, err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("failed to execute template %s: %w", name, err)
	}
	return buf.String(), nil
}

func (g *IaCGenerator) loadStackKitSubdir(dir, prefix string, data *StackKitTemplateData, output *IaCOutput) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		entryPath := filepath.Join(dir, entry.Name())
		if entry.IsDir() {
			subPrefix := filepath.Join(prefix, entry.Name())
			if err := g.loadStackKitSubdir(entryPath, subPrefix, data, output); err != nil {
				return err
			}
			continue
		}

		name := entry.Name()
		if isInertTemplateEntry(entry) {
			continue
		}

		content, err := os.ReadFile(entryPath)
		if err != nil {
			return fmt.Errorf("failed to read %s: %w", entryPath, err)
		}

		contentString := string(content)
		if strings.HasSuffix(name, ".tpl") {
			outputName := filepath.Join(prefix, templateOutputName(name))
			rendered, err := g.renderStackKitTemplate(outputName, contentString, data)
			if err != nil {
				return fmt.Errorf("failed to render %s: %w", name, err)
			}
			output.Files[outputName] = rendered
		} else if strings.HasSuffix(name, ".tf") {
			outputName := filepath.Join(prefix, name)
			if strings.Contains(contentString, "{{") {
				rendered, err := g.renderStackKitTemplate(outputName, contentString, data)
				if err != nil {
					return fmt.Errorf("failed to render %s: %w", name, err)
				}
				output.Files[outputName] = rendered
				continue
			}
			output.Files[outputName] = contentString
		}
	}

	return nil
}

func stackKitTemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		"lower":                strings.ToLower,
		"upper":                strings.ToUpper,
		"trim":                 strings.TrimSpace,
		"replace":              strings.ReplaceAll,
		"contains":             strings.Contains,
		"hasPrefix":            strings.HasPrefix,
		"hasSuffix":            strings.HasSuffix,
		"join":                 strings.Join,
		"split":                strings.Split,
		"default":              stackKitDefaultValue,
		"quote":                stackKitQuote,
		"indent":               stackKitIndent,
		"toJson":               stackKitToJSON,
		"toJsonPretty":         stackKitToJSONPretty,
		"catalogSection":       stackKitCatalogSection,
		"hasEnableVar":         stackKitHasEnableVar,
		"htmlText":             stackKitHTMLText,
		"htmlAttr":             stackKitHTMLAttr,
		"publicGuideURL":       stackKitPublicGuideURL,
		"serviceHint":          stackKitServiceHint,
		"setupActionAvailable": stackKitSetupActionAvailable,
	}
}

func stackKitDefaultValue(def, val any) any {
	if val == nil {
		return def
	}
	if s, ok := val.(string); ok && strings.TrimSpace(s) == "" {
		return def
	}
	return val
}

func stackKitQuote(s string) string {
	return fmt.Sprintf("%q", s)
}

func stackKitIndent(spaces int, s string) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(s, "\n")
	for i, line := range lines {
		if line != "" {
			lines[i] = pad + line
		}
	}
	return strings.Join(lines, "\n")
}

func stackKitToJSON(v any) string {
	if v == nil {
		return "null"
	}
	data, err := json.Marshal(v)
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(data)
}

func stackKitToJSONPretty(v any) string {
	if v == nil {
		return "null"
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Sprintf(`{"error": %q}`, err.Error())
	}
	return string(data)
}

func stackKitCatalogSection(section string, catalog []StackKitCatalogEntry) []StackKitCatalogEntry {
	result := make([]StackKitCatalogEntry, 0, len(catalog))
	for _, entry := range catalog {
		if entry.Section == section {
			result = append(result, entry)
		}
	}
	return result
}

func stackKitHasEnableVar(entry StackKitCatalogEntry) bool {
	return entry.EnableVar != ""
}

func stackKitHTMLText(value string) string {
	escaped := html.EscapeString(value)
	escaped = strings.ReplaceAll(escaped, "${", "$&#123;")
	escaped = strings.ReplaceAll(escaped, "%{", "%&#123;")
	return escaped
}

func stackKitHTMLAttr(value string) string {
	return stackKitHTMLText(value)
}

func stackKitPublicGuideURL(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "https://docs.kombify.io/") {
		return stackKitHTMLAttr(value)
	}
	return ""
}

func stackKitServiceHint(entry StackKitCatalogEntry) string {
	if entry.Key == "base" && entry.IdentityPolicy == "none" {
		return "Bootstrap open"
	}
	switch entry.IdentityPolicy {
	case "provider":
		return "Identity provider"
	case "oidc":
		return "SSO entry"
	case "forwardauth":
		return "Protected by TinyAuth"
	case "self-auth":
		return "App login"
	default:
		return "Local service"
	}
}

func stackKitSetupActionAvailable(entry StackKitCatalogEntry) bool {
	return entry.SetupPolicy == "on_demand"
}

func stackKitDefaultDomains(catalog []StackKitCatalogEntry) []StackKitCatalogEntry {
	return append([]StackKitCatalogEntry(nil), catalog...)
}

func stackKitDefaultCatalog() []StackKitCatalogEntry {
	entries := []StackKitCatalogEntry{
		{
			Key: "base", Name: "base", DisplayName: "Node Hub",
			Description: "StackKits node hub with bootstrap warnings, onboarding, recovery, and local service links.",
			ToolName:    "dashboard", ModuleSlug: "dashboard", LocalSlug: "base", PublicSlug: "base",
			IdentityPolicy: "none", OwnerProvisioningPolicy: "none",
			Icon: "&#128421;", Badge: "L2 - Node Hub", Section: "Platform", Order: -1,
			EnableVar: "enable_dashboard", GuideURL: "https://docs.kombify.io/guides/stackkits/node-hub", Default: true,
		},
		{
			Key: "home", Name: "home", DisplayName: "Homepage",
			Description: "IaC-managed homelab start dashboard generated from the StackKits service catalog.",
			ToolName:    "homepage", ModuleSlug: "homepage", LocalSlug: "home", PublicSlug: "home",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#8962;", Badge: "L2 - Start", Section: "Platform", Order: 0,
			EnableVar: "enable_homepage", GuideURL: "https://docs.kombify.io/guides/stackkits/services/homepage", Default: true,
		},
		{
			Key: "id", Name: "id", DisplayName: "PocketID",
			Description: "OIDC identity provider with passkey authentication.",
			ToolName:    "pocketid", ModuleSlug: "pocketid", LocalSlug: "id", PublicSlug: "id",
			IdentityPolicy: "provider", OwnerProvisioningPolicy: "required",
			Icon: "&#128100;", Badge: "L1 - IdP", Section: "Platform", Order: 10,
			EnableVar: "enable_pocketid", GuideURL: "https://docs.kombify.io/guides/stackkits/services/pocketid", Default: true,
		},
		{
			Key: "kuma", Name: "kuma", DisplayName: "Uptime Kuma",
			Description: "Service uptime monitoring and status pages.",
			ToolName:    "uptime-kuma", ModuleSlug: "uptime-kuma", LocalSlug: "kuma", PublicSlug: "kuma",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#128202;", Badge: "L2 - Monitoring", Section: "Platform", Order: 10,
			EnableVar: "enable_uptime_kuma", GuideURL: "https://docs.kombify.io/guides/stackkits/services/uptime-kuma", SetupPolicy: "automatic", Default: true,
		},
		{
			Key: "auth", Name: "auth", DisplayName: "TinyAuth",
			Description: "ForwardAuth gateway backed by PocketID.",
			ToolName:    "tinyauth", ModuleSlug: "tinyauth", LocalSlug: "auth", PublicSlug: "auth",
			IdentityPolicy: "oidc", OwnerProvisioningPolicy: "required",
			Icon: "&#128274;", Badge: "L1 - ForwardAuth", Section: "Platform", Order: 20,
			EnableVar: "enable_tinyauth", GuideURL: "https://docs.kombify.io/guides/stackkits/services/tinyauth", Default: true,
		},
		{
			Key: "whoami", Name: "whoami", DisplayName: "Whoami",
			Description: "TinyAuth-protected HTTP echo service for routing diagnostics.",
			ToolName:    "whoami", ModuleSlug: "whoami", LocalSlug: "whoami", PublicSlug: "whoami",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#129302;", Badge: "L2 - Routing test", Section: "Platform", Order: 20,
			EnableVar: "enable_whoami", GuideURL: "https://docs.kombify.io/guides/stackkits/services/whoami", SetupPolicy: "automatic", Default: true,
		},
		{
			Key: "traefik", Name: "traefik", DisplayName: "Traefik",
			Description: "Routes all service traffic.",
			ToolName:    "traefik", ModuleSlug: "traefik", LocalSlug: "traefik", PublicSlug: "traefik",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#9889;", Badge: "L2 - Reverse Proxy", Section: "Platform", Order: 30,
			EnableVar: "enable_traefik", GuideURL: "https://docs.kombify.io/guides/stackkits/services/traefik", Default: true,
		},
		{
			Key: "point", Name: "point", DisplayName: "kombify Point DNS",
			Description: "Local LAN DNS resolver for home service names.",
			ToolName:    "kombify-point", ModuleSlug: "kombify-point", LocalSlug: "point", PublicSlug: "point",
			IdentityPolicy: "none", OwnerProvisioningPolicy: "none",
			Icon: "&#127760;", Badge: "L1 - DNS", Section: "Platform", Order: 35,
			EnableVar: "enable_kombify_point", GuideURL: "https://docs.kombify.io/guides/stackkits/services/kombify-point",
		},
		{
			Key: "dokploy", Name: "dokploy", DisplayName: "Dokploy",
			Description: "Self-hosted PaaS for deploying applications.",
			ToolName:    "dokploy", ModuleSlug: "dokploy", LocalSlug: "dokploy", PublicSlug: "dokploy",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#128640;", Badge: "L2 - PaaS", Section: "Platform", Order: 40,
			EnableVar: "enable_dokploy", GuideURL: "https://docs.kombify.io/guides/stackkits/services/dokploy",
		},
		{
			Key: "komodo", Name: "komodo", DisplayName: "Komodo",
			Description: "Programmable self-hosted PaaS for Compose stack deployment through API keys.",
			ToolName:    "komodo", ModuleSlug: "komodo", LocalSlug: "komodo", PublicSlug: "komodo",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#9881;", Badge: "L2 - PaaS", Section: "Platform", Order: 41,
			EnableVar: "enable_komodo", GuideURL: "https://docs.kombify.io/guides/stackkits/services/komodo",
		},
		{
			Key: "coolify", Name: "coolify", DisplayName: "Coolify",
			Description: "Self-hosted deployment platform.",
			ToolName:    "coolify", ModuleSlug: "coolify", LocalSlug: "coolify", PublicSlug: "coolify",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#128171;", Badge: "L2 - PaaS", Section: "Platform", Order: 42,
			EnableVar: "enable_coolify", GuideURL: "https://docs.kombify.io/guides/stackkits/services/coolify",
		},
		{
			Key: "dockge", Name: "dockge", DisplayName: "Dockge",
			Description: "Docker Compose stack manager.",
			ToolName:    "dockge", ModuleSlug: "dockge", LocalSlug: "dockge", PublicSlug: "dockge",
			IdentityPolicy: "forwardauth", OwnerProvisioningPolicy: "required",
			Icon: "&#128230;", Badge: "L2 - Compose Manager", Section: "Platform", Order: 43,
			EnableVar: "enable_dockge", GuideURL: "https://docs.kombify.io/guides/stackkits/services/dockge",
		},
		{
			Key: "vault", Name: "vault", DisplayName: "Vaultwarden",
			Description: "Bitwarden-compatible password vault with its own app login.",
			ToolName:    "vaultwarden", ModuleSlug: "vaultwarden", LocalSlug: "vault", PublicSlug: "vault",
			IdentityPolicy: "self-auth", OwnerProvisioningPolicy: "none",
			Icon: "&#128272;", Badge: "L3 - App login", Section: "Applications", Order: 30,
			EnableVar: "enable_vaultwarden", GuideURL: "https://docs.kombify.io/guides/stackkits/services/vaultwarden", Default: true,
		},
		{
			Key: "media", Name: "media", DisplayName: "Jellyfin",
			Description: "Media server with its own app login.",
			ToolName:    "jellyfin", ModuleSlug: "jellyfin", LocalSlug: "media", PublicSlug: "media",
			IdentityPolicy: "self-auth", OwnerProvisioningPolicy: "none",
			Icon: "&#127916;", Badge: "L3 - App login", Section: "Applications", Order: 40,
			EnableVar: "enable_jellyfin", GuideURL: "https://docs.kombify.io/guides/stackkits/services/jellyfin",
		},
		{
			Key: "photos", Name: "photos", DisplayName: "Immich",
			Description: "Photo and video management with its own app login.",
			ToolName:    "immich", ModuleSlug: "immich", LocalSlug: "photos", PublicSlug: "photos",
			IdentityPolicy: "self-auth", OwnerProvisioningPolicy: "required",
			Icon: "&#128247;", Badge: "L3 - App login", Section: "Applications", Order: 50,
			EnableVar: "enable_immich", GuideURL: "https://docs.kombify.io/guides/stackkits/services/immich",
			SetupPolicy: "on_demand", SetupActionLabel: "Do the setup for me", Default: true,
		},
		{
			Key: "files", Name: "files", DisplayName: "Files",
			Description: "Document management and file sharing, backed by Cloudreve by default.",
			ToolName:    "cloudreve", ModuleSlug: "cloudreve", LocalSlug: "files", PublicSlug: "files",
			IdentityPolicy: "self-auth", OwnerProvisioningPolicy: "required",
			Icon: "&#128193;", Badge: "L3 - Files", Section: "Applications", Order: 60,
			EnableVar: "enable_files", GuideURL: "https://docs.kombify.io/guides/stackkits/services/files",
			SetupPolicy: "automatic", SetupActionLabel: "Automatisierter Bootstrap", Default: true,
		},
	}
	for i := range entries {
		stackKitNormalizeCatalogEntry(&entries[i])
	}
	return entries
}

func stackKitNormalizeCatalogEntry(entry *StackKitCatalogEntry) {
	if entry.Name == "" {
		entry.Name = entry.Key
	}
	if entry.ToolName == "" {
		entry.ToolName = entry.Key
	}
	if entry.ModuleSlug == "" {
		entry.ModuleSlug = entry.ToolName
	}
	if entry.LocalSlug == "" {
		entry.LocalSlug = entry.Key
	}
	if entry.PublicSlug == "" {
		entry.PublicSlug = entry.LocalSlug
	}
	if entry.Layer == "" {
		switch entry.Section {
		case "Platform":
			entry.Layer = "L2-platform"
		case "Applications":
			entry.Layer = "L3-application"
		}
	}
	if entry.SetupPolicy == "" {
		if entry.Section == "Platform" {
			entry.SetupPolicy = "automatic"
		} else {
			entry.SetupPolicy = "manual"
		}
	}
	if entry.LogoURL == "" {
		entry.LogoURL = stackKitLogoURLForKey(entry.Key, entry.ToolName)
	}
	entry.Nested = entry.LocalSlug
	entry.Flat = entry.PublicSlug
}

func stackKitLogoURLForKey(key, tool string) string {
	switch key {
	case "base":
		return "https://stackkit.cc/favicon.svg"
	case "home":
		return "https://cdn.simpleicons.org/homeassistant/ffffff"
	case "auth", "id":
		return "https://cdn.simpleicons.org/openid/ffffff"
	case "traefik":
		return "https://cdn.simpleicons.org/traefikproxy/ffffff"
	case "dokploy", "komodo", "dockge":
		return "https://cdn.simpleicons.org/docker/ffffff"
	case "coolify":
		return "https://cdn.simpleicons.org/coolify/ffffff"
	case "kuma":
		return "https://cdn.simpleicons.org/uptimekuma/ffffff"
	case "whoami":
		return "https://cdn.simpleicons.org/httpie/ffffff"
	case "vault":
		return "https://cdn.simpleicons.org/bitwarden/ffffff"
	case "media":
		return "https://cdn.simpleicons.org/jellyfin/ffffff"
	case "photos":
		return "https://cdn.simpleicons.org/immich/ffffff"
	case "point":
		return "https://cdn.simpleicons.org/cloudflare/ffffff"
	}
	switch tool {
	case "coolify":
		return "https://cdn.simpleicons.org/coolify/ffffff"
	case "jellyfin":
		return "https://cdn.simpleicons.org/jellyfin/ffffff"
	case "immich":
		return "https://cdn.simpleicons.org/immich/ffffff"
	case "vaultwarden":
		return "https://cdn.simpleicons.org/bitwarden/ffffff"
	default:
		return ""
	}
}
