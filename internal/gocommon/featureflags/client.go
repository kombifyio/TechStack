package featureflags

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/internal/gocommon/servicecall"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ClientConfig configures the Admin API feature flag client.
type ClientConfig struct {
	BaseURL           string        // e.g. "https://admin.kombify.io/api/v1"
	APIKey            string        // Legacy: shared bearer token (being retired)
	DatabaseURL       string        // DB-first runtime projection URL (preferred for SaaS server runtimes)
	Tier              string        // Subscription tier (FREE, PRO, etc.)
	Category          string        // Tool category (kombify-sim, kombify-ai, etc.)
	Mode              string        // Runtime mode (saas_standalone, selfhosted, preview, etc.)
	Roles             []string      // Principal roles used by the Admin evaluator
	UserID            string        // Product user ID alias for subject targeting
	TargetingKey      string        // Stable OpenFeature-style targeting key
	SubjectID         string        // Auth/user subject ID
	AgentID           string        // Optional agent principal ID
	ClientID          string        // Optional OAuth/client principal ID
	AuthorizedParty   string        // Optional azp/client alias
	Anonymous         bool          // Anonymous/public principal evaluation
	PreviewOptIn      bool          // Preview feature opt-in for DB projection evaluation
	DeploymentChannel string        // production, staging, development; default production
	CacheTTL          time.Duration // Cache duration (default 5 min)
	MaxRetries        int           // Retry count after first attempt (default 0)
	HTTPClient        *http.Client  // Optional custom HTTP client
	DatabaseTimeout   time.Duration // Per DB projection query timeout (default 2s)
	Logger            *slog.Logger  // Optional structured logger

	// Service-auth (preferred, retires APIKey once rolled out).
	// When ServiceAuthSecret is set, each request carries a short-lived HS256
	// JWT in X-Kombify-Service-Auth (see kombify-go-common/servicecall).
	ServiceAuthSecret string // HS256 shared secret (SERVICE_AUTH_SECRET)
	// SigningKey/SigningKeyID are this caller's per-caller Ed25519 identity
	// (servicecall.LoadEd25519FromEnv). When set the token is signed EdDSA and
	// ServiceAuthSecret is not needed; the HS256 secret remains the fallback.
	SigningKey       ed25519.PrivateKey
	SigningKeyID     string
	ServiceName      string // This caller's service name (e.g. "techstack", "sim")
	AdminServiceName string // Target admin service name (default "administration")
}

// EvaluationContext is the runtime identity sent to the Admin API evaluator.
// Blank fields are omitted from requests. When Anonymous is true and Tier is
// blank, the client sends tier=ANONYMOUS so older tier-only rules still work.
type EvaluationContext struct {
	Tier              string
	Mode              string
	Roles             []string
	UserID            string
	TargetingKey      string
	SubjectID         string
	AgentID           string
	ClientID          string
	AuthorizedParty   string
	Anonymous         bool
	PreviewOptIn      bool
	DeploymentChannel string
}

// tierCache holds cached flags for a single evaluation context.
type tierCache struct {
	flags      map[string]AdminFeatureFlag
	fetchedAt  time.Time
	refreshing bool
	available  bool
}

// Client fetches feature flags from the kombify Administration API.
// Maintains separate caches per evaluation context so context switches are
// instantaneous when that context was previously cached.
// Implements stale-while-revalidate: stale data is returned immediately
// while a background goroutine refreshes the cache.
type Client struct {
	cfg    ClientConfig
	client *http.Client
	db     *pgxpool.Pool
	log    *slog.Logger

	mu         sync.RWMutex
	context    EvaluationContext
	tierCaches map[string]*tierCache
}

// NewClient creates a new Admin API feature flag client.
// Returns nil if neither cfg.DatabaseURL nor cfg.BaseURL is configured,
// signaling the caller to skip this layer. When DatabaseURL is set, the
// client reads the kombify-DB runtime projection first and only falls back to
// Admin HTTP when BaseURL is also configured.
func NewClient(cfg ClientConfig) *Client {
	if cfg.DatabaseURL == "" && cfg.BaseURL == "" {
		return nil
	}
	httpClient := cfg.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 2 * time.Second}
	}
	if cfg.CacheTTL == 0 {
		cfg.CacheTTL = 5 * time.Minute
	}
	if cfg.DatabaseTimeout == 0 {
		cfg.DatabaseTimeout = 2 * time.Second
	}
	logger := cfg.Logger
	if logger == nil {
		logger = slog.Default()
	}
	cfg.Roles = copyStrings(cfg.Roles)
	var db *pgxpool.Pool
	if strings.TrimSpace(cfg.DatabaseURL) != "" {
		pool, err := pgxpool.New(context.Background(), cfg.DatabaseURL)
		if err != nil {
			logger.Warn("featureflags: DB projection disabled; failed to initialize pool", "error", err)
			if cfg.BaseURL == "" {
				return nil
			}
		} else {
			db = pool
		}
	}
	return &Client{
		cfg:        cfg,
		client:     httpClient,
		db:         db,
		log:        logger,
		context:    evaluationContextFromConfig(cfg).normalized(),
		tierCaches: make(map[string]*tierCache),
	}
}

// Close releases DB resources held by the client. It is safe to call multiple times.
func (c *Client) Close() {
	if c == nil || c.db == nil {
		return
	}
	c.db.Close()
}

// IsEnabled checks if a flag is enabled via the cached Admin API data.
// Returns (enabled, found). found=false when the Admin API is unavailable
// or does not know the flag.
func (c *Client) IsEnabled(key string) (bool, bool) {
	flag, found, available := c.lookupFlag(key)
	if !available || !found {
		return false, false
	}
	return flag.Enabled, true
}

// GetFlag returns the full Admin API flag payload when available.
func (c *Client) GetFlag(key string) (AdminFeatureFlag, bool) {
	flag, found, available := c.lookupFlag(key)
	if !available || !found {
		return AdminFeatureFlag{}, false
	}
	return flag, true
}

// IsEnabledForContext checks a flag for the supplied evaluation context
// without mutating the client's active context.
func (c *Client) IsEnabledForContext(key string, ctx EvaluationContext) (bool, bool) {
	flag, found, available := c.lookupFlagForContext(key, ctx)
	if !available || !found {
		return false, false
	}
	return flag.Enabled, true
}

// GetFlagForContext returns the full Admin API flag payload for the supplied
// evaluation context without mutating the client's active context.
func (c *Client) GetFlagForContext(key string, ctx EvaluationContext) (AdminFeatureFlag, bool) {
	flag, found, available := c.lookupFlagForContext(key, ctx)
	if !available || !found {
		return AdminFeatureFlag{}, false
	}
	return flag, true
}

// SetTier switches the active tier for subsequent lookups.
// Other evaluation context fields remain unchanged.
func (c *Client) SetTier(tier string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx := c.context
	ctx.Tier = tier
	c.context = ctx.normalized()
}

// ActiveTier returns the current tier under read lock.
func (c *Client) ActiveTier() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.context.Tier
}

// SetContext switches the active evaluation context for subsequent lookups.
// Cached values are isolated per normalized context, so changing roles,
// anonymous state, subject, mode, or tier cannot reuse the wrong flags.
func (c *Client) SetContext(ctx EvaluationContext) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.context = ctx.normalized()
}

// ActiveContext returns a defensive copy of the current evaluation context.
func (c *Client) ActiveContext() EvaluationContext {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.context.copy()
}

// Refresh forces a synchronous cache refresh for the active evaluation context.
func (c *Client) Refresh() error {
	ctx := c.ActiveContext()
	flags, err := c.fetchFlagsForContext(ctx)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.tierCaches[ctx.cacheKey()] = &tierCache{
		flags:     flags,
		fetchedAt: time.Now(),
		available: true,
	}
	return nil
}

// Available reports whether the Admin API has been successfully reached
// at least once for the active evaluation context.
func (c *Client) Available() bool {
	ctx := c.ActiveContext()
	c.mu.RLock()
	defer c.mu.RUnlock()
	entry := c.tierCaches[ctx.cacheKey()]
	return entry != nil && entry.available
}

// getFlags returns cached flags for the active evaluation context, refreshing when stale.
// Uses stale-while-revalidate: returns stale cache immediately and
// refreshes in the background so callers never block on the Admin API
// (except for the very first request per context).
func (c *Client) getFlags() (map[string]AdminFeatureFlag, bool) {
	return c.getFlagsForContext(c.ActiveContext())
}

func (c *Client) getFlagsForContext(ctx EvaluationContext) (map[string]AdminFeatureFlag, bool) {
	ctx = ctx.normalized()
	cacheKey := ctx.cacheKey()

	c.mu.RLock()
	entry := c.tierCaches[cacheKey]
	var cached map[string]AdminFeatureFlag
	var stale, refreshing, available bool
	if entry != nil {
		cached = entry.flags
		stale = entry.flags == nil || time.Since(entry.fetchedAt) >= c.cfg.CacheTTL
		refreshing = entry.refreshing
		available = entry.available
	} else {
		stale = true
	}
	c.mu.RUnlock()

	// Cache is fresh — fast path.
	if !stale {
		return cached, available
	}

	// Cache exists but is stale — return it immediately, refresh in background.
	if cached != nil && !refreshing {
		c.mu.Lock()
		if tc := c.tierCaches[cacheKey]; tc != nil {
			tc.refreshing = true
		}
		c.mu.Unlock()
		go func(refreshCtx EvaluationContext, refreshKey string) {
			flags, err := c.fetchFlagsForContext(refreshCtx)
			c.mu.Lock()
			defer c.mu.Unlock()
			tc := c.tierCaches[refreshKey]
			if tc == nil {
				tc = &tierCache{}
				c.tierCaches[refreshKey] = tc
			}
			tc.refreshing = false
			if err != nil {
				c.log.Debug("admin API background refresh failed", "tier", refreshCtx.Tier, "anonymous", refreshCtx.Anonymous, "error", err)
				tc.available = false
				tc.fetchedAt = time.Now()
				return
			}
			tc.flags = flags
			tc.fetchedAt = time.Now()
			tc.available = true
		}(ctx, cacheKey)
		return cached, available
	}

	// No cache at all (first request for this context) — must block.
	if cached == nil {
		flags, err := c.fetchFlagsForContext(ctx)
		if err != nil {
			c.log.Warn("admin API initial fetch failed", "tier", ctx.Tier, "anonymous", ctx.Anonymous, "error", err)
			flags = map[string]AdminFeatureFlag{}
			c.mu.Lock()
			c.tierCaches[cacheKey] = &tierCache{
				flags:     flags,
				fetchedAt: time.Now(),
				available: false,
			}
			c.mu.Unlock()
			return flags, false
		}
		c.mu.Lock()
		c.tierCaches[cacheKey] = &tierCache{
			flags:     flags,
			fetchedAt: time.Now(),
			available: true,
		}
		c.mu.Unlock()
		return flags, true
	}

	// Stale cache + refresh already in progress — return stale.
	return cached, available
}

func (c *Client) lookupFlag(key string) (AdminFeatureFlag, bool, bool) {
	flags, available := c.getFlags()
	if flags == nil {
		return AdminFeatureFlag{}, false, available
	}
	flag, ok := flags[key]
	return flag, ok, available
}

func (c *Client) lookupFlagForContext(key string, ctx EvaluationContext) (AdminFeatureFlag, bool, bool) {
	flags, available := c.getFlagsForContext(ctx)
	if flags == nil {
		return AdminFeatureFlag{}, false, available
	}
	flag, ok := flags[key]
	return flag, ok, available
}

// fetchFlagsForContext calls GET /feature-flags on the Admin API with retry.
func (c *Client) fetchFlagsForContext(ctx EvaluationContext) (map[string]AdminFeatureFlag, error) {
	ctx = ctx.normalized()
	if c.db != nil {
		flags, err := c.fetchFlagsFromDB(ctx)
		if err == nil {
			return flags, nil
		}
		if c.cfg.BaseURL == "" {
			return nil, fmt.Errorf("DB feature projection unavailable: %w", err)
		}
		c.log.Warn("featureflags: DB projection failed; falling back to Admin HTTP", "tier", ctx.Tier, "anonymous", ctx.Anonymous, "error", err)
	}

	if c.cfg.BaseURL == "" {
		return nil, fmt.Errorf("no feature flag backend configured")
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if attempt > 0 {
			time.Sleep(time.Duration(attempt) * 200 * time.Millisecond)
		}
		flags, err := c.doFetch(ctx)
		if err == nil {
			return flags, nil
		}
		lastErr = err
		c.log.Debug("admin API attempt failed", "attempt", attempt+1, "tier", ctx.Tier, "anonymous", ctx.Anonymous, "error", err)
	}
	return nil, fmt.Errorf("admin API unavailable after %d attempts: %w", c.cfg.MaxRetries+1, lastErr)
}

func (c *Client) fetchFlagsFromDB(evalCtx EvaluationContext) (map[string]AdminFeatureFlag, error) {
	queryCtx, cancel := context.WithTimeout(context.Background(), c.cfg.DatabaseTimeout)
	defer cancel()

	tx, err := c.db.Begin(queryCtx)
	if err != nil {
		return nil, fmt.Errorf("begin DB feature projection transaction: %w", err)
	}
	defer tx.Rollback(context.Background()) //nolint:errcheck

	for _, setting := range dbSessionSettings(c.cfg, evalCtx) {
		if _, settingErr := tx.Exec(queryCtx, `SELECT set_config($1, $2, true)`, setting.key, setting.value); settingErr != nil {
			return nil, fmt.Errorf("set DB feature projection context %s: %w", setting.key, settingErr)
		}
	}

	rows, err := tx.Query(
		queryCtx,
		`SELECT key, enabled, description, category, metadata FROM get_runtime_feature_flags($1)`,
		nullableString(c.cfg.Category),
	)
	if err != nil {
		return nil, fmt.Errorf("query DB feature projection: %w", err)
	}
	defer rows.Close()

	flags := make(map[string]AdminFeatureFlag)
	for rows.Next() {
		var flag AdminFeatureFlag
		var metadata []byte
		if err := rows.Scan(&flag.Key, &flag.Enabled, &flag.Description, &flag.Category, &metadata); err != nil {
			return nil, fmt.Errorf("scan DB feature projection row: %w", err)
		}
		if len(metadata) > 0 {
			if err := json.Unmarshal(metadata, &flag.Metadata); err != nil {
				return nil, fmt.Errorf("decode DB feature projection metadata: %w", err)
			}
		}
		flags[flag.Key] = flag
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate DB feature projection rows: %w", err)
	}
	if err := tx.Commit(queryCtx); err != nil {
		return nil, fmt.Errorf("commit DB feature projection transaction: %w", err)
	}
	return flags, nil
}

func (c *Client) doFetch(ctx EvaluationContext) (map[string]AdminFeatureFlag, error) {
	ctx = ctx.normalized()
	endpoint, err := url.Parse(strings.TrimRight(c.cfg.BaseURL, "/") + "/feature-flags")
	if err != nil {
		return nil, err
	}
	query := endpoint.Query()
	for _, parameter := range []struct{ name, value string }{
		{"category", c.cfg.Category},
		{"tier", ctx.Tier},
		{"mode", ctx.Mode},
		{"roles", strings.Join(ctx.Roles, ",")},
		{"user_id", ctx.UserID},
		{"targeting_key", ctx.TargetingKey},
		{"subject_id", ctx.SubjectID},
	} {
		if parameter.value != "" {
			query.Set(parameter.name, parameter.value)
		}
	}
	if ctx.Anonymous {
		query.Set("anonymous", "true")
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequest("GET", endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	if c.cfg.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		req.Header.Set("X-Kombify-Service-Key", c.cfg.APIKey)
	}
	if (c.cfg.ServiceAuthSecret != "" || c.cfg.SigningKey != nil) && c.cfg.ServiceName != "" {
		target := c.cfg.AdminServiceName
		if target == "" {
			target = "administration"
		}
		tok, tokenErr := servicecall.IssueToken(
			servicecall.Config{
				ServiceName: c.cfg.ServiceName,
				Secret:      c.cfg.ServiceAuthSecret,
				SigningKey:  c.cfg.SigningKey, SigningKeyID: c.cfg.SigningKeyID,
				TokenTTL: 5 * time.Minute,
			},
			target,
			nil, // no OnBehalfOf: featureflags fetch is peer-to-peer, not per-user
			"",  // no request ID
		)
		if tokenErr == nil {
			req.Header.Set(servicecall.HeaderServiceAuth, tok)
		} else {
			c.log.Warn("featureflags: failed to issue service-auth token", "error", tokenErr)
		}
	}
	req.Header.Set("Accept", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("HTTP request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	var apiResp AdminFlagsResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if !apiResp.Success {
		return nil, fmt.Errorf("API error: success=false")
	}
	return apiResp.Data, nil
}

func evaluationContextFromConfig(cfg ClientConfig) EvaluationContext {
	return EvaluationContext{
		Tier:              cfg.Tier,
		Mode:              cfg.Mode,
		Roles:             cfg.Roles,
		UserID:            cfg.UserID,
		TargetingKey:      cfg.TargetingKey,
		SubjectID:         cfg.SubjectID,
		AgentID:           cfg.AgentID,
		ClientID:          cfg.ClientID,
		AuthorizedParty:   cfg.AuthorizedParty,
		Anonymous:         cfg.Anonymous,
		PreviewOptIn:      cfg.PreviewOptIn,
		DeploymentChannel: cfg.DeploymentChannel,
	}
}

func (ctx EvaluationContext) normalized() EvaluationContext {
	normalized := EvaluationContext{
		Tier:              strings.TrimSpace(ctx.Tier),
		Mode:              strings.TrimSpace(ctx.Mode),
		Roles:             normalizeRoles(ctx.Roles),
		UserID:            strings.TrimSpace(ctx.UserID),
		TargetingKey:      strings.TrimSpace(ctx.TargetingKey),
		SubjectID:         strings.TrimSpace(ctx.SubjectID),
		AgentID:           strings.TrimSpace(ctx.AgentID),
		ClientID:          strings.TrimSpace(ctx.ClientID),
		AuthorizedParty:   strings.TrimSpace(ctx.AuthorizedParty),
		Anonymous:         ctx.Anonymous,
		PreviewOptIn:      ctx.PreviewOptIn,
		DeploymentChannel: strings.TrimSpace(ctx.DeploymentChannel),
	}
	if normalized.DeploymentChannel == "" {
		normalized.DeploymentChannel = "production"
	}
	if normalized.Anonymous && normalized.Tier == "" {
		normalized.Tier = "ANONYMOUS"
	}
	if normalized.TargetingKey == "" {
		switch {
		case normalized.SubjectID != "":
			normalized.TargetingKey = normalized.SubjectID
		case normalized.UserID != "":
			normalized.TargetingKey = normalized.UserID
		}
	}
	return normalized
}

func (ctx EvaluationContext) copy() EvaluationContext {
	ctx.Roles = copyStrings(ctx.Roles)
	return ctx
}

func (ctx EvaluationContext) cacheKey() string {
	ctx = ctx.normalized()
	return strings.Join([]string{
		"tier=" + ctx.Tier,
		"mode=" + ctx.Mode,
		"roles=" + strings.Join(ctx.Roles, ","),
		"user_id=" + ctx.UserID,
		"targeting_key=" + ctx.TargetingKey,
		"subject_id=" + ctx.SubjectID,
		"agent_id=" + ctx.AgentID,
		"client_id=" + ctx.ClientID,
		"authorized_party=" + ctx.AuthorizedParty,
		fmt.Sprintf("anonymous=%t", ctx.Anonymous),
		fmt.Sprintf("preview_opt_in=%t", ctx.PreviewOptIn),
		"deployment_channel=" + ctx.DeploymentChannel,
	}, "|")
}

type dbSessionSetting struct {
	key   string
	value string
}

func dbSessionSettings(cfg ClientConfig, ctx EvaluationContext) []dbSessionSetting {
	ctx = ctx.normalized()
	mode := ctx.Mode
	if mode == "" {
		mode = "saas_standalone"
	}
	return []dbSessionSetting{
		{"app.user_id", ctx.UserID},
		{"app.subject_id", ctx.SubjectID},
		{"app.tier", strings.ToLower(ctx.Tier)},
		{"app.tiers", strings.ToLower(ctx.Tier)},
		{"app.roles", strings.ToLower(strings.Join(ctx.Roles, ","))},
		{"app.agent_id", ctx.AgentID},
		{"app.client_id", ctx.ClientID},
		{"app.authorized_party", ctx.AuthorizedParty},
		{"app.service_name", cfg.ServiceName},
		{"app.anonymous", boolString(ctx.Anonymous)},
		{"app.runtime_mode", mode},
		{"app.preview_opt_in", boolString(ctx.PreviewOptIn)},
		{"app.deployment_channel", ctx.DeploymentChannel},
	}
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func normalizeRoles(roles []string) []string {
	seen := make(map[string]struct{}, len(roles))
	normalized := make([]string, 0, len(roles))
	for _, role := range roles {
		role = strings.TrimSpace(role)
		if role == "" {
			continue
		}
		if _, ok := seen[role]; ok {
			continue
		}
		seen[role] = struct{}{}
		normalized = append(normalized, role)
	}
	return normalized
}

func copyStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	copied := make([]string, len(values))
	copy(copied, values)
	return copied
}
