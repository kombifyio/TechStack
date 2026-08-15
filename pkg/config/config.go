// Package config handles configuration loading for kombifyTechstack
package config

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const hostedRuntimeKeyEnv = "TECHSTACK_SAAS_BUILD_PROOF"

// hostedRuntimeKeySHA256 is intentionally empty in public/self-host builds.
// Private Kombify SaaS builds set it via:
//
//	-X github.com/kombifyio/techstack/pkg/config.hostedRuntimeKeySHA256=<sha256>
var hostedRuntimeKeySHA256 string

// Config holds all configuration for kombifyTechstack
type Config struct {
	Edition             Edition               `yaml:"edition"`
	DeploymentMode      DeploymentMode        `yaml:"deployment_mode"`
	ProviderCatalog     ProviderCatalogConfig `yaml:"provider_catalog"`
	EdgeAuthSecret      string                `yaml:"edge_auth_secret"`       // Primary Edge identity secret (TECHSTACK_EDGE_AUTH_SECRET).
	EdgeAuthNextSecret  string                `yaml:"edge_auth_secret_next"`  // Rotation successor (TECHSTACK_EDGE_AUTH_SECRET_NEXT).
	EdgeAuthKeyID       string                `yaml:"edge_auth_key_id"`       // Primary identity key ID (TECHSTACK_EDGE_AUTH_KEY_ID).
	EdgeAuthNextKeyID   string                `yaml:"edge_auth_key_id_next"`  // Rotation identity key ID (TECHSTACK_EDGE_AUTH_KEY_ID_NEXT).
	EdgeFlagsSecret     string                `yaml:"edge_flags_secret"`      // Request-decision secret (TECHSTACK_EDGE_FLAGS_SECRET).
	EdgeFlagsNextSecret string                `yaml:"edge_flags_secret_next"` // Rotation decision secret (TECHSTACK_EDGE_FLAGS_SECRET_NEXT).
	EdgeFlagsKeyID      string                `yaml:"edge_flags_key_id"`      // Primary decision key ID (TECHSTACK_EDGE_FLAGS_KEY_ID).
	EdgeFlagsNextKeyID  string                `yaml:"edge_flags_key_id_next"` // Rotation decision key ID (TECHSTACK_EDGE_FLAGS_KEY_ID_NEXT).
	Server              ServerConfig          `yaml:"server"`
	Database            DatabaseConfig        `yaml:"database"`
	Logging             LoggingConfig         `yaml:"logging"`
	Monitoring          MonitoringConfig      `yaml:"monitoring"`
	Backup              BackupConfig          `yaml:"backup"`
	Drift               DriftConfig           `yaml:"drift"`
	StackKitsDir        string                `yaml:"stackkits_dir"` // Path to StackKits directory (TECHSTACK_STACKKITS_DIR)
}

// DriftConfig holds drift detection scheduler settings.
type DriftConfig struct {
	Enabled   bool   `yaml:"enabled"`   // Enable periodic drift checks (TECHSTACK_DRIFT_ENABLED)
	Interval  string `yaml:"interval"`  // Check interval e.g. "6h", "1h", "30m" (TECHSTACK_DRIFT_INTERVAL)
	Retention int    `yaml:"retention"` // Max drift results to keep per stack (TECHSTACK_DRIFT_RETENTION, default 50)
}

// DriftIntervalDuration returns the drift check interval as a time.Duration.
// Defaults to 6h if the interval cannot be parsed.
func (c *DriftConfig) DriftIntervalDuration() time.Duration {
	d, err := time.ParseDuration(c.Interval)
	if err != nil {
		return 6 * time.Hour
	}
	return d
}

// ServerConfig holds HTTP/gRPC server settings
type ServerConfig struct {
	ListenAddr     string   `yaml:"listen_addr"`
	GRPCAddr       string   `yaml:"grpc_addr"`
	CORSOrigins    []string `yaml:"cors_origins"`
	Environment    string   `yaml:"environment"`      // development, staging, production, local
	RateLimitRPS   float64  `yaml:"rate_limit_rps"`   // requests per second (default: 10)
	RateLimitBurst int      `yaml:"rate_limit_burst"` // max burst size (default: 20)
	DataDir        string   `yaml:"data_dir"`         // Path to data directory (default: pb_data)

	// gRPC Queue Backpressure Settings (S7)
	GRPCQueueMaxSize          int    `yaml:"grpc_queue_max_size"`          // Max commands in queue (default: 1000)
	GRPCQueueOverflowStrategy string `yaml:"grpc_queue_overflow_strategy"` // "reject" or "drop-oldest" (default: reject)
	GRPCQueueWarningThreshold int    `yaml:"grpc_queue_warning_threshold"` // Percentage for warning logs (default: 80)
	GRPCTofuQueueMaxSize      int    `yaml:"grpc_tofu_queue_max_size"`     // Max tofu commands in queue (default: 100)
	RuntimeLogPath            string `yaml:"runtime_log_path"`             // JSONL runtime log spool path (TECHSTACK_RUNTIME_LOG_PATH)
	RuntimeLogMaxEntries      int    `yaml:"runtime_log_max_entries"`      // In-memory runtime log query buffer (default: 5000)
}

// DatabaseConfig holds database settings
type DatabaseConfig struct {
	Path string `yaml:"path"`
}

// LoggingConfig holds logging settings
type LoggingConfig struct {
	Level  string `yaml:"level"`  // debug, info, warn, error
	Format string `yaml:"format"` // json, text
}

// MonitoringConfig holds monitoring storage and query backend settings.
type MonitoringConfig struct {
	TSDBDir                   string `yaml:"tsdb_dir"`                // Embedded TSDB data directory (TECHSTACK_TSDB_DIR)
	QueryBackendURL           string `yaml:"query_backend_url"`       // Optional PromQL-compatible remote backend (TECHSTACK_MONITOR_QUERY_BACKEND_URL)
	CollectorMode             string `yaml:"collector_mode"`          // Operator-visible collector topology (TECHSTACK_MONITOR_COLLECTOR_MODE)
	AlertInterval             string `yaml:"alert_interval"`          // Alert evaluation interval, e.g. "30s" (TECHSTACK_ALERT_INTERVAL)
	IngestFreshnessTTL        string `yaml:"ingest_freshness_ttl"`    // Maximum age of an ingest success (TECHSTACK_MONITOR_INGEST_FRESHNESS_TTL)
	OTLPLaneRequirement       string `yaml:"otlp_lane_requirement"`   // required, optional, disabled
	LegacyPushLaneRequirement string `yaml:"legacy_lane_requirement"` // required, optional, disabled
}

// AlertIntervalDuration returns the alert evaluation interval.
// Defaults to 30s when unset or invalid.
func (c MonitoringConfig) AlertIntervalDuration() time.Duration {
	if d, err := time.ParseDuration(c.AlertInterval); err == nil && d > 0 {
		return d
	}
	return 30 * time.Second
}

// IngestFreshnessTTLDuration returns the maximum accepted age of a successful
// ingest observation. Monitoring is Day-2 evidence, so an old success must not
// remain healthy forever.
func (c MonitoringConfig) IngestFreshnessTTLDuration() time.Duration {
	if d, err := time.ParseDuration(c.IngestFreshnessTTL); err == nil && d > 0 {
		return d
	}
	return 90 * time.Second
}

// BackupConfig holds backup and S3 settings
type BackupConfig struct {
	// Backup scheduling
	Enabled   bool   `yaml:"enabled"`   // Enable automated backups (TECHSTACK_BACKUP_ENABLED)
	Interval  string `yaml:"interval"`  // Backup interval e.g. "24h", "12h", "1h" (TECHSTACK_BACKUP_INTERVAL)
	Retention int    `yaml:"retention"` // Number of backups to keep (TECHSTACK_BACKUP_RETENTION)
	BackupDir string `yaml:"backup_dir"`

	// S3 configuration
	S3Enabled   bool   `yaml:"s3_enabled"`    // Enable S3 upload (TECHSTACK_S3_ENABLED)
	S3Bucket    string `yaml:"s3_bucket"`     // S3 bucket name (TECHSTACK_S3_BUCKET)
	S3Endpoint  string `yaml:"s3_endpoint"`   // S3/MinIO endpoint (TECHSTACK_S3_ENDPOINT)
	S3AccessKey string `yaml:"s3_access_key"` // S3 access key (TECHSTACK_S3_ACCESS_KEY)
	S3SecretKey string `yaml:"s3_secret_key"` // S3 secret key (TECHSTACK_S3_SECRET_KEY)
	S3Region    string `yaml:"s3_region"`     // S3 region (TECHSTACK_S3_REGION)
	S3Prefix    string `yaml:"s3_prefix"`     // S3 key prefix for backups (TECHSTACK_S3_PREFIX)
}

// DefaultCORSOrigins returns secure default CORS origins for development.
// In production, these should be explicitly configured via TECHSTACK_CORS_ORIGINS.
var DefaultCORSOrigins = []string{
	"http://localhost:5173", // SvelteKit dev server
	"http://localhost:5261", // kombifyTechstack UI (docker-compose/local)
	"http://localhost:8090", // PocketBase default
	"http://localhost:5260", // kombifyTechstack API
	"http://127.0.0.1:5173",
	"http://127.0.0.1:5261",
	"http://127.0.0.1:8090",
	"http://127.0.0.1:5260",
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *Config {
	edition := DefaultEdition()
	return &Config{
		Edition:         edition,
		DeploymentMode:  edition.DeploymentMode(),
		ProviderCatalog: DefaultProviderCatalogConfig(),
		Server: ServerConfig{
			ListenAddr:     ":5260",
			GRPCAddr:       ":5263",
			CORSOrigins:    DefaultCORSOrigins,
			Environment:    "development",
			RateLimitRPS:   10, // 10 requests per second
			RateLimitBurst: 20, // burst of 20 requests
			DataDir:        "pb_data",
			// gRPC Queue Backpressure defaults (S7)
			GRPCQueueMaxSize:          1000,     // 1000 commands max
			GRPCQueueOverflowStrategy: "reject", // reject new commands when full
			GRPCQueueWarningThreshold: 80,       // warn at 80% capacity
			GRPCTofuQueueMaxSize:      100,      // 100 tofu commands max
			RuntimeLogMaxEntries:      5000,     // bounded local runtime log query buffer
		},
		Database: DatabaseConfig{
			Path: "/data/techstack.db",
		},
		Logging: LoggingConfig{
			Level:  "info",
			Format: "json",
		},
		Monitoring: MonitoringConfig{
			TSDBDir:                   "pb_data/monitoring/tsdb",
			CollectorMode:             "direct",
			AlertInterval:             "30s",
			IngestFreshnessTTL:        "90s",
			OTLPLaneRequirement:       "required",
			LegacyPushLaneRequirement: "optional",
		},
		Backup: BackupConfig{
			Enabled:   false,
			Interval:  "24h",
			Retention: 7,
			BackupDir: "./backups",
			S3Enabled: false,
			S3Region:  "us-east-1",
			S3Prefix:  "techstack-backups/",
		},
		Drift: DriftConfig{
			Enabled:   false,
			Interval:  "6h",
			Retention: 50,
		},
	}
}

// Load reads configuration from file and environment variables
// Priority: Environment > Config File > Defaults
func Load(configPath string) (*Config, error) {
	cfg := DefaultConfig()

	// Try to load from file if it exists
	if configPath != "" {
		if err := cfg.loadFromFile(configPath); err != nil {
			return nil, fmt.Errorf("failed to load config file: %w", err)
		}
	} else {
		// Try default locations
		for _, path := range []string{
			"techstack.yaml",
			"techstack.yml",
			"/etc/techstack/config.yaml",
			filepath.Join(os.Getenv("HOME"), ".techstack", "config.yaml"),
		} {
			if _, err := os.Stat(path); err == nil {
				if err := cfg.loadFromFile(path); err != nil {
					return nil, fmt.Errorf("failed to load config file %s: %w", path, err)
				}
				break
			}
		}
	}

	// Override with environment variables
	if err := cfg.loadFromEnv(); err != nil {
		return nil, err
	}

	return cfg, nil
}

func (c *Config) loadFromFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return yaml.Unmarshal(data, c)
}

// envStr returns the environment variable value if set, otherwise returns the current value.
func envStr(key string, current string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return current
}

// envBool returns true if the environment variable is "true" or "1".
func envBool(key string) bool {
	v := os.Getenv(key)
	return v == "true" || v == "1"
}

// envFloat returns the environment variable value as float64 if set and valid,
// otherwise returns the current value.
func envFloat(key string, current float64) float64 {
	if v := os.Getenv(key); v != "" {
		var f float64
		if _, err := fmt.Sscanf(v, "%f", &f); err == nil && f > 0 {
			return f
		}
	}
	return current
}

// envInt returns the environment variable value as int if set and valid,
// otherwise returns the current value.
func envInt(key string, current int) int {
	if v := os.Getenv(key); v != "" {
		var i int
		if _, err := fmt.Sscanf(v, "%d", &i); err == nil && i > 0 {
			return i
		}
	}
	return current
}

func (c *Config) loadFromEnv() error {
	if err := c.resolveEditionFromEnv(); err != nil {
		return err
	}
	if err := c.loadProviderCatalogFromEnv(); err != nil {
		return err
	}

	// Edge identity and request-decision keyrings. Decision keys may be
	// dedicated; the middleware falls back to the identity key only when the
	// decision key is intentionally absent.
	c.EdgeAuthSecret = envStr("TECHSTACK_EDGE_AUTH_SECRET", c.EdgeAuthSecret)
	c.EdgeAuthNextSecret = envStr("TECHSTACK_EDGE_AUTH_SECRET_NEXT", c.EdgeAuthNextSecret)
	c.EdgeAuthKeyID = envStr("TECHSTACK_EDGE_AUTH_KEY_ID", c.EdgeAuthKeyID)
	c.EdgeAuthNextKeyID = envStr("TECHSTACK_EDGE_AUTH_KEY_ID_NEXT", c.EdgeAuthNextKeyID)
	c.EdgeFlagsSecret = envStr("TECHSTACK_EDGE_FLAGS_SECRET", c.EdgeFlagsSecret)
	c.EdgeFlagsNextSecret = envStr("TECHSTACK_EDGE_FLAGS_SECRET_NEXT", c.EdgeFlagsNextSecret)
	c.EdgeFlagsKeyID = envStr("TECHSTACK_EDGE_FLAGS_KEY_ID", c.EdgeFlagsKeyID)
	c.EdgeFlagsNextKeyID = envStr("TECHSTACK_EDGE_FLAGS_KEY_ID_NEXT", c.EdgeFlagsNextKeyID)

	// Server settings
	c.Server.ListenAddr = envStr("TECHSTACK_LISTEN_ADDR", c.Server.ListenAddr)
	c.Server.GRPCAddr = envStr("TECHSTACK_GRPC_ADDR", c.Server.GRPCAddr)
	c.Server.Environment = envStr("TECHSTACK_ENV", c.Server.Environment)
	if v := os.Getenv("TECHSTACK_CORS_ORIGINS"); v != "" {
		c.Server.CORSOrigins = SplitOriginList(v)
	}

	// Rate limiting settings
	c.Server.RateLimitRPS = envFloat("TECHSTACK_RATE_LIMIT_RPS", c.Server.RateLimitRPS)
	c.Server.RateLimitBurst = envInt("TECHSTACK_RATE_LIMIT_BURST", c.Server.RateLimitBurst)

	// gRPC Queue Backpressure settings (S7)
	c.Server.GRPCQueueMaxSize = envInt("TECHSTACK_GRPC_QUEUE_MAX_SIZE", c.Server.GRPCQueueMaxSize)
	c.Server.GRPCQueueOverflowStrategy = envStr("TECHSTACK_GRPC_QUEUE_OVERFLOW_STRATEGY", c.Server.GRPCQueueOverflowStrategy)
	c.Server.GRPCQueueWarningThreshold = envInt("TECHSTACK_GRPC_QUEUE_WARNING_THRESHOLD", c.Server.GRPCQueueWarningThreshold)
	c.Server.GRPCTofuQueueMaxSize = envInt("TECHSTACK_GRPC_TOFU_QUEUE_MAX_SIZE", c.Server.GRPCTofuQueueMaxSize)
	c.Server.RuntimeLogMaxEntries = envInt("TECHSTACK_RUNTIME_LOG_MAX_ENTRIES", c.Server.RuntimeLogMaxEntries)

	// Database settings
	c.Database.Path = envStr("TECHSTACK_DB_PATH", c.Database.Path)

	// Logging settings
	c.Logging.Level = envStr("TECHSTACK_LOG_LEVEL", c.Logging.Level)
	c.Logging.Format = envStr("TECHSTACK_LOG_FORMAT", c.Logging.Format)

	// Monitoring settings
	c.Monitoring.TSDBDir = envStr("TECHSTACK_TSDB_DIR", c.Monitoring.TSDBDir)
	c.Monitoring.QueryBackendURL = envStr("TECHSTACK_MONITOR_QUERY_BACKEND_URL", c.Monitoring.QueryBackendURL)
	c.Monitoring.CollectorMode = envStr("TECHSTACK_MONITOR_COLLECTOR_MODE", c.Monitoring.CollectorMode)
	c.Monitoring.AlertInterval = envStr("TECHSTACK_ALERT_INTERVAL", c.Monitoring.AlertInterval)
	c.Monitoring.IngestFreshnessTTL = envStr("TECHSTACK_MONITOR_INGEST_FRESHNESS_TTL", c.Monitoring.IngestFreshnessTTL)
	c.Monitoring.OTLPLaneRequirement = envStr("TECHSTACK_MONITOR_OTLP_REQUIREMENT", c.Monitoring.OTLPLaneRequirement)
	c.Monitoring.LegacyPushLaneRequirement = envStr("TECHSTACK_MONITOR_LEGACY_REQUIREMENT", c.Monitoring.LegacyPushLaneRequirement)

	// Simulation removed (cleanup plan 2026-04-27 phase 3.1).

	// Backup settings
	if envBool("TECHSTACK_BACKUP_ENABLED") {
		c.Backup.Enabled = true
	}
	c.Backup.Interval = envStr("TECHSTACK_BACKUP_INTERVAL", c.Backup.Interval)
	c.Backup.Retention = envInt("TECHSTACK_BACKUP_RETENTION", c.Backup.Retention)
	c.Backup.BackupDir = envStr("TECHSTACK_BACKUP_DIR", c.Backup.BackupDir)

	// S3 settings
	if envBool("TECHSTACK_S3_ENABLED") {
		c.Backup.S3Enabled = true
	}
	c.Backup.S3Bucket = envStr("TECHSTACK_S3_BUCKET", c.Backup.S3Bucket)
	c.Backup.S3Endpoint = envStr("TECHSTACK_S3_ENDPOINT", c.Backup.S3Endpoint)
	c.Backup.S3AccessKey = envStr("TECHSTACK_S3_ACCESS_KEY", c.Backup.S3AccessKey)
	c.Backup.S3SecretKey = envStr("TECHSTACK_S3_SECRET_KEY", c.Backup.S3SecretKey)
	c.Backup.S3Region = envStr("TECHSTACK_S3_REGION", c.Backup.S3Region)
	c.Backup.S3Prefix = envStr("TECHSTACK_S3_PREFIX", c.Backup.S3Prefix)

	// Server data directory
	c.Server.DataDir = envStr("TECHSTACK_DATA_DIR", c.Server.DataDir)
	c.Server.RuntimeLogPath = envStr("TECHSTACK_RUNTIME_LOG_PATH", c.Server.RuntimeLogPath)
	if c.Server.RuntimeLogPath == "" {
		c.Server.RuntimeLogPath = filepath.Join(c.Server.DataDir, "runtime-logs.jsonl")
	}
	if c.Backup.BackupDir == "" || c.Backup.BackupDir == "./backups" {
		c.Backup.BackupDir = filepath.Join(c.Server.DataDir, "backups")
	}

	// StackKits directory
	c.StackKitsDir = envStr("TECHSTACK_STACKKITS_DIR", c.StackKitsDir)

	// Drift detection scheduler settings
	if envBool("TECHSTACK_DRIFT_ENABLED") {
		c.Drift.Enabled = true
	}
	c.Drift.Interval = envStr("TECHSTACK_DRIFT_INTERVAL", c.Drift.Interval)
	c.Drift.Retention = envInt("TECHSTACK_DRIFT_RETENTION", c.Drift.Retention)

	return nil
}

func (c *Config) resolveEditionFromEnv() error {
	if err := c.resolveEditionValueFromEnv(); err != nil {
		return err
	}
	return c.validateHostedEditionGate()
}

// resolveEditionValueFromEnv resolves the edition and deployment mode with the
// boot precedence (KOMBIFY_EDITION first, legacy DEPLOYMENT_MODE second)
// without enforcing the hosted-build gate. Full configuration loading remains
// responsible for the gate.
func (c *Config) resolveEditionValueFromEnv() error {
	if editionEnv := strings.TrimSpace(os.Getenv("KOMBIFY_EDITION")); editionEnv != "" {
		edition := Edition(editionEnv)
		if !edition.IsValid() {
			return fmt.Errorf("invalid KOMBIFY_EDITION %q (valid: %s)", editionEnv, strings.Join(ValidEditionValues(), ", "))
		}
		c.Edition = edition
		c.DeploymentMode = edition.DeploymentMode()
		return nil
	}

	if strings.TrimSpace(string(c.Edition)) == "" {
		c.Edition = DefaultEdition()
	}
	if !c.Edition.IsValid() {
		return fmt.Errorf("invalid edition %q (valid: %s)", c.Edition, strings.Join(ValidEditionValues(), ", "))
	}

	if modeEnv := strings.TrimSpace(os.Getenv("DEPLOYMENT_MODE")); modeEnv != "" {
		if edition, ok := editionFromLegacyDeploymentMode(DeploymentMode(modeEnv)); ok {
			fmt.Fprintf(os.Stderr, "warning: DEPLOYMENT_MODE=%s is legacy; set KOMBIFY_EDITION=%s instead\n", modeEnv, edition)
			c.Edition = edition
		}
		c.DeploymentMode = c.Edition.DeploymentMode()
		return nil
	}

	if c.Edition == DefaultEdition() && c.DeploymentMode.IsValid() && c.DeploymentMode != c.Edition.DeploymentMode() {
		if edition, ok := editionFromLegacyDeploymentMode(c.DeploymentMode); ok {
			c.Edition = edition
		}
	}
	c.DeploymentMode = c.Edition.DeploymentMode()
	return nil
}

func editionFromLegacyDeploymentMode(mode DeploymentMode) (Edition, bool) {
	switch mode {
	case ModeSelfHosted:
		return EditionSelfHostOSS, true
	case ModeSaaS:
		return EditionSaaSStandalone, true
	default:
		return "", false
	}
}

func (c *Config) validateHostedEditionGate() error {
	if !c.Edition.IsSaaS() {
		return nil
	}

	expectedHash := strings.ToLower(strings.TrimSpace(hostedRuntimeKeySHA256))
	if expectedHash == "" {
		return fmt.Errorf("KOMBIFY_EDITION=%s requires a private Kombify hosted build; public/self-host builds can only run %s", c.Edition, EditionSelfHostOSS)
	}
	if len(expectedHash) != sha256.Size*2 {
		return fmt.Errorf("KOMBIFY_EDITION=%s requires a valid private Kombify hosted build gate", c.Edition)
	}

	runtimeKey := strings.TrimSpace(os.Getenv(hostedRuntimeKeyEnv))
	if runtimeKey == "" {
		return fmt.Errorf("KOMBIFY_EDITION=%s requires %s", c.Edition, hostedRuntimeKeyEnv)
	}

	sum := sha256.Sum256([]byte(runtimeKey))
	actualHash := hex.EncodeToString(sum[:])
	if subtle.ConstantTimeCompare([]byte(actualHash), []byte(expectedHash)) != 1 {
		return fmt.Errorf("KOMBIFY_EDITION=%s failed private Kombify hosted runtime proof", c.Edition)
	}
	return nil
}

// ValidateCORSOrigins checks CORS configuration and returns validated origins.
// It warns if wildcard is used in production and returns secure defaults if needed.
func (c *Config) ValidateCORSOrigins() ([]string, []string) {
	var warnings []string
	origins := c.Server.CORSOrigins

	// Check for wildcard
	hasWildcard := false
	for _, origin := range origins {
		if origin == "*" {
			hasWildcard = true
			break
		}
	}

	// local is prod-hard for CORS: a wildcard is rejected the same way.
	isStrict := c.IsProduction() || c.IsLocal()

	if hasWildcard {
		if isStrict {
			warnings = append(warnings, "CRITICAL: CORS wildcard (*) in production/local is insecure! Set TECHSTACK_CORS_ORIGINS to specific origins.")
			// In production with wildcard, fall back to empty (most restrictive)
			// This prevents cross-site attacks but may break legitimate requests
			origins = []string{}
		} else {
			warnings = append(warnings, "CORS wildcard (*) detected. This is acceptable for development but must be configured for production.")
		}
	}

	origins = appendUniqueSanitizedOrigins(origins, AllowedFrameOriginsFromEnv())
	if c.DeploymentMode.IsSaaS() {
		origins = appendUniqueSanitizedOrigins(origins, DefaultSaaSFrameOrigins())
	}
	origins = dedupeOrigins(origins)

	// Validate origin format (basic check)
	for _, origin := range origins {
		if origin != "*" && !isValidOrigin(origin) {
			warnings = append(warnings, "Invalid CORS origin format: "+origin)
		}
	}

	return origins, warnings
}

func dedupeOrigins(origins []string) []string {
	out := make([]string, 0, len(origins))
	for _, origin := range origins {
		origin = strings.TrimSpace(origin)
		if origin == "" || containsString(out, origin) {
			continue
		}
		out = append(out, origin)
	}
	return out
}

// isValidOrigin performs basic validation of a CORS origin.
func isValidOrigin(origin string) bool {
	parsed, err := url.Parse(origin)
	if err != nil {
		return false
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return false
	}
	return parsed.Host != "" && parsed.User == nil && parsed.Path == "" && parsed.RawQuery == "" && parsed.Fragment == ""
}

// IsProduction returns true if the environment is set to production.
func (c *Config) IsProduction() bool {
	env := c.Server.Environment
	return env == "production" || env == "prod"
}

// IsLocal returns true if the environment is set to local — the
// loopback-sovereign desktop posture. local behaves like production except
// where loopback HTTP forces a difference (session/CSRF cookies are not
// Secure) and agent mTLS material is not required.
func (c *Config) IsLocal() bool {
	return c.Server.Environment == "local"
}

// ValidateLocalPosture enforces the fail-closed guard for the local
// environment: both listeners must stay on a loopback interface. The default
// ":5260"/":5263" (all interfaces) is rejected on purpose — local requires an
// explicit loopback bind.
func (c *Config) ValidateLocalPosture() error {
	if !c.IsLocal() {
		return nil
	}
	if err := requireLoopbackListen("TECHSTACK_LISTEN_ADDR", c.Server.ListenAddr); err != nil {
		return err
	}
	return requireLoopbackListen("TECHSTACK_GRPC_ADDR", c.Server.GRPCAddr)
}

func requireLoopbackListen(name, addr string) error {
	host, _, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		return fmt.Errorf("TECHSTACK_ENV=local requires a loopback %s (host:port), got %q: %w", name, addr, err)
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("TECHSTACK_ENV=local requires a loopback %s, got %q", name, addr)
	}
	return nil
}

// BackupIntervalDuration returns the backup interval as a time.Duration.
// Defaults to 24h if the interval cannot be parsed.
func (c *BackupConfig) BackupIntervalDuration() time.Duration {
	d, err := time.ParseDuration(c.Interval)
	if err != nil {
		return 24 * time.Hour
	}
	return d
}
