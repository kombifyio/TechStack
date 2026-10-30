package appcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/fulllocal"
)

const (
	updateCheckTagTTL      = 6 * time.Hour
	updateCheckFailureTTL  = 10 * time.Minute
	updateCheckRequestTime = 2 * time.Second
	updateCheckTotalTime   = 45 * time.Second
	updateCheckPageSize    = 1000
	updateCheckMaxPages    = 60
	updateCheckMaxBody     = 8 << 20
	updateCheckMaxCache    = 512
	updateCheckConcurrency = 4
)

// registryStatusError is a non-200 registry answer; a 401, 403 or 404 means
// the image is private or not published there, not that the registry is down.
type registryStatusError int

func (e registryStatusError) Error() string { return fmt.Sprintf("registry status %d", int(e)) }

// UpdateResult is what the registry says about the newest comparable tag.
type UpdateResult struct {
	// Latest is empty unless a real registry check succeeded.
	Latest    string
	Newer     bool
	CheckedAt time.Time
	// ReasonCode is set exactly when Latest is empty.
	ReasonCode string
}

// UpdateChecker answers "is a newer tag of this image published" from the
// registry's anonymous OCI distribution API. It never blocks a caller on the
// network: an unknown repository starts one background fetch and reports
// ReasonPending until the result is cached, and failures are cached briefly so
// an unreachable registry is not hammered.
type UpdateChecker struct {
	client *http.Client
	now    func() time.Time
	// endpoints resolves the API and token endpoints of a registry; a test seam.
	endpoints func(registry string) (registryEndpoints, bool)

	mu    sync.Mutex
	cache map[string]*tagCacheEntry
	slots chan struct{}
}

type registryEndpoints struct {
	api      string
	tokenURL string // contains one %s for the repository
}

type tagCacheEntry struct {
	tags    []string
	fetched time.Time
	failure string
	loading bool
}

// ttl is how long this entry's answer is served before a background refresh.
func (e *tagCacheEntry) ttl() time.Duration {
	if len(e.tags) == 0 && e.failure != "" {
		return updateCheckFailureTTL
	}
	return updateCheckTagTTL
}

// NewUpdateChecker returns a checker with strict per-request timeouts. Under
// Full Local it returns nil: a registry lookup would send image names to a
// public registry, so every comparison reports update_check_disabled instead.
func NewUpdateChecker() *UpdateChecker {
	if fulllocal.Active() {
		return nil
	}
	return &UpdateChecker{
		client:    &http.Client{Timeout: updateCheckRequestTime},
		now:       func() time.Time { return time.Now().UTC() },
		endpoints: publicRegistryEndpoints,
		cache:     map[string]*tagCacheEntry{},
		slots:     make(chan struct{}, updateCheckConcurrency),
	}
}

func publicRegistryEndpoints(registry string) (registryEndpoints, bool) {
	switch registry {
	case "docker.io":
		return registryEndpoints{
			api:      "https://registry-1.docker.io",
			tokenURL: "https://auth.docker.io/token?service=registry.docker.io&scope=repository:%s:pull",
		}, true
	case "ghcr.io", "lscr.io":
		// lscr.io is LinuxServer's alias of ghcr.io/linuxserver.
		return registryEndpoints{
			api:      "https://ghcr.io",
			tokenURL: "https://ghcr.io/token?service=ghcr.io&scope=repository:%s:pull",
		}, true
	default:
		return registryEndpoints{}, false
	}
}

var repoPattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*(?:/[a-z0-9]+(?:[._-][a-z0-9]+)*)*$`)

// Check reports the newest comparable tag for a running version. It returns
// immediately from the cache; see UpdateChecker for the pending semantics.
func (c *UpdateChecker) Check(reference, current string) UpdateResult {
	image := ParseImage(reference)
	if c == nil || image.Repo == "" || !repoPattern.MatchString(image.Repo) {
		return UpdateResult{ReasonCode: ReasonRegistryUnsupported}
	}
	registry := image.Registry
	if registry == "lscr.io" {
		registry = "ghcr.io"
	}
	if _, ok := c.endpoints(registry); !ok {
		return UpdateResult{ReasonCode: ReasonRegistryUnsupported}
	}
	key := registry + "/" + image.Repo

	c.mu.Lock()
	entry := c.cache[key]
	now := c.now()
	stale := entry == nil || (!entry.fetched.IsZero() && now.Sub(entry.fetched) > entry.ttl())
	if stale && (entry == nil || !entry.loading) {
		if entry == nil {
			if len(c.cache) >= updateCheckMaxCache {
				for evicted := range c.cache {
					delete(c.cache, evicted)
					break
				}
			}
			entry = &tagCacheEntry{}
			c.cache[key] = entry
		}
		entry.loading = true
		go c.refresh(key, registry, image.Repo)
	}
	tags, fetched, failure := entry.tags, entry.fetched, entry.failure
	c.mu.Unlock()

	if len(tags) > 0 {
		latest, newer, ok := SelectLatest(current, tags)
		if !ok {
			return UpdateResult{CheckedAt: fetched, ReasonCode: ReasonNoComparableTags}
		}
		return UpdateResult{Latest: latest, Newer: newer, CheckedAt: fetched}
	}
	if failure != "" {
		return UpdateResult{CheckedAt: fetched, ReasonCode: failure}
	}
	if !fetched.IsZero() {
		return UpdateResult{CheckedAt: fetched, ReasonCode: ReasonNoComparableTags}
	}
	return UpdateResult{ReasonCode: ReasonPending}
}

func (c *UpdateChecker) refresh(key, registry, repo string) {
	c.slots <- struct{}{}
	defer func() { <-c.slots }()
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTotalTime)
	defer cancel()
	tags, err := c.fetchTags(ctx, registry, repo)

	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.cache[key]
	if entry == nil {
		return
	}
	entry.loading = false
	entry.fetched = c.now()
	if err != nil {
		// Keep stale tags rather than dropping a known answer on a blip.
		if len(entry.tags) == 0 {
			entry.failure = ReasonRegistryUnreachable
			var status registryStatusError
			if errors.As(err, &status) && (status == http.StatusUnauthorized || status == http.StatusForbidden || status == http.StatusNotFound) {
				entry.failure = ReasonRegistryDenied
			}
		}
		return
	}
	entry.tags, entry.failure = tags, ""
}

func (c *UpdateChecker) fetchTags(ctx context.Context, registry, repo string) ([]string, error) {
	endpoints, ok := c.endpoints(registry)
	if !ok {
		return nil, fmt.Errorf("registry %q is not supported", registry)
	}
	api, err := url.Parse(endpoints.api)
	if err != nil {
		return nil, err
	}
	var token struct {
		Token       string `json:"token"`
		AccessToken string `json:"access_token"`
	}
	if err := c.getJSON(ctx, fmt.Sprintf(endpoints.tokenURL, url.QueryEscape(repo)), "", &token, nil); err != nil {
		return nil, fmt.Errorf("registry token: %w", err)
	}
	bearer := token.Token
	if bearer == "" {
		bearer = token.AccessToken
	}
	// Only version-looking tags are kept: a repository can hold tens of
	// thousands of "git-<sha>" tags that can never be an update candidate.
	var tags []string
	next := fmt.Sprintf("%s/v2/%s/tags/list?n=%d", endpoints.api, repo, updateCheckPageSize)
	for page := 0; next != "" && page < updateCheckMaxPages; page++ {
		var body struct {
			Tags []string `json:"tags"`
		}
		var link string
		if err := c.getJSON(ctx, next, bearer, &body, &link); err != nil {
			return nil, fmt.Errorf("registry tags: %w", err)
		}
		for _, tag := range body.Tags {
			if _, ok := parseVersion(tag); ok {
				tags = append(tags, tag)
			}
		}
		next = nextPageURL(api, link)
	}
	return tags, nil
}

func (c *UpdateChecker) getJSON(ctx context.Context, rawURL, bearer string, out any, linkHeader *string) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	request.Header.Set("Accept", "application/json")
	if bearer != "" {
		request.Header.Set("Authorization", "Bearer "+bearer)
	}
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4<<10))
		return registryStatusError(response.StatusCode)
	}
	if linkHeader != nil {
		*linkHeader = response.Header.Get("Link")
	}
	return json.NewDecoder(io.LimitReader(response.Body, updateCheckMaxBody)).Decode(out)
}

// nextPageURL follows the registry's `Link: <...>; rel="next"` header, but only
// within the registry's own host so the bearer token never leaves it.
func nextPageURL(api *url.URL, link string) string {
	start, end := strings.Index(link, "<"), strings.Index(link, ">")
	if start < 0 || end <= start || !strings.Contains(link[end:], `rel="next"`) {
		return ""
	}
	target, err := api.Parse(link[start+1 : end])
	if err != nil || target.Host != api.Host {
		return ""
	}
	return target.String()
}
