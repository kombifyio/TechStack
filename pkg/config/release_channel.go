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
// lifecycle. The default deployment always serves stable.
func ReleaseChannel() string {
	if strings.EqualFold(strings.TrimSpace(os.Getenv(EnvReleaseChannel)), "preview") {
		return "preview"
	}
	return "stable"
}

// RunsSharedWork reports whether this deployment runs the work that is
// nobody's homelab: agent connections, monitoring, posture, outboxes, global
// boot writes. Stable runs it for every tenant; Preview runs only the homelab
// work of people who chose Preview (pkg/releasechannel).
func RunsSharedWork() bool { return ReleaseChannel() != "preview" }

// AgentOriginOverride is the origin Guards and runtime agents call when it is
// not this deployment's own: Preview runs no agent routes, so agents of every
// tenant talk to stable. Empty on stable.
func AgentOriginOverride() string {
	if ReleaseChannel() != "preview" {
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
	if ReleaseChannel() != "preview" {
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
