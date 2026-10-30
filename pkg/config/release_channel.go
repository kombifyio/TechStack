package config

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"sync"
)

const EnvReleaseChannel = "TECHSTACK_RELEASE_CHANNEL"

// ReleaseChannel is shared by database startup, HTTP admission and runtime
// lifecycle: "preview", "early-access" or, by default, "stable".
func ReleaseChannel() string {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(EnvReleaseChannel))) {
	case "preview":
		return "preview"
	case "early-access":
		return "early-access"
	}
	return "stable"
}

// SecondaryChannel reports a deployment beside stable. Preview uses Stage:
// Early Access is served by the edge under the same links to the
// people who opted in (CI-CD-PLATFORM-STANDARD §4.6 "Channel model"). Both
// serve signed-in people over HTTP only; shared, homelab, machine and
// migration work stays on stable.
func SecondaryChannel() bool { return ReleaseChannel() != "stable" }

// RunsSharedWork reports whether this deployment runs the work that is
// nobody's homelab: agent connections, monitoring, posture, outboxes, global
// boot writes. Only stable runs it (pkg/releasechannel).
func RunsSharedWork() bool { return !SecondaryChannel() }

// AgentOriginOverride is the origin Guards and runtime agents call when it is
// not this deployment's own: Preview and Early Access run no agent routes, so
// agents of every tenant talk to stable. Empty on stable.
func AgentOriginOverride() string {
	if !SecondaryChannel() {
		return ""
	}
	return strings.TrimRight(strings.TrimSpace(os.Getenv("TECHSTACK_STABLE_ORIGIN")), "/")
}

// PreviewExecution records installed boundaries on this boot. Environment
// configuration alone never supplies a qualified health receipt.
type PreviewExecution struct {
	mu                                                      sync.RWMutex
	httpInstalled, startupSuppressed, backgroundPartitioned bool
	fixture                                                 string
}

func NewPreviewExecution() *PreviewExecution {
	if !SecondaryChannel() {
		return nil
	}
	return &PreviewExecution{fixture: strings.TrimSpace(os.Getenv("TEST_RIL_PRO_USER_ID"))}
}
func (s *PreviewExecution) HTTPInstalled() {
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.httpInstalled = true
	}
}
func (s *PreviewExecution) StartupSuppressed() {
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.startupSuppressed = true
	}
}

// BackgroundPartitioned records that homelab work runs only for tenants
// whose owner chose Preview.
func (s *PreviewExecution) BackgroundPartitioned() {
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.backgroundPartitioned = true
	}
}
func (s *PreviewExecution) Snapshot() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope := map[string]string{"http": "unqualified", "scheduled": "unqualified", "queues": "unqualified", "startup": "unqualified"}
	if s.httpInstalled {
		scope["http"] = "opted-in-users"
	}
	if s.startupSuppressed {
		scope["startup"] = "verify-only"
	}
	if s.startupSuppressed && s.backgroundPartitioned {
		scope["scheduled"] = "owned-tenants"
		scope["queues"] = "owned-tenants"
	}
	if s.fixture != "" {
		digest := sha256.Sum256([]byte(s.fixture))
		scope["fixture_subject_sha256"] = hex.EncodeToString(digest[:])
	}
	return scope
}
