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

// PreviewSubjectAdmitted restricts engineering preview to the existing canonical
// RIL customer fixture. The subject comes from its registered TEST_*_ID input,
// never a browser hint, role, arbitrary email or owner session.
func PreviewSubjectAdmitted(subject string) bool {
	if ReleaseChannel() != "preview" {
		return true
	}
	fixture := strings.TrimSpace(os.Getenv("TEST_RIL_PRO_USER_ID"))
	return fixture != "" && strings.TrimSpace(subject) == fixture
}

// BackgroundExecutionEnabled keeps preview out of the live database's stable
// queues, recovery, timers and provider reconcilers until owned-work admission
// exists. A service credential does not inherit a browser user's fixture scope.
func BackgroundExecutionEnabled() bool { return ReleaseChannel() != "preview" }

// PreviewExecution records installed boundaries on this boot. Environment
// configuration alone never supplies a qualified health receipt.
type PreviewExecution struct {
	mu                                                     sync.RWMutex
	httpInstalled, startupSuppressed, backgroundSuppressed bool
	fixture                                                string
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
func (s *PreviewExecution) BackgroundSuppressed() {
	if s != nil {
		s.mu.Lock()
		defer s.mu.Unlock()
		s.backgroundSuppressed = true
	}
}
func (s *PreviewExecution) Snapshot() map[string]string {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	scope := map[string]string{"http": "unqualified", "scheduled": "unqualified", "queues": "unqualified", "startup": "unqualified"}
	if s.httpInstalled && s.fixture != "" {
		scope["http"] = "shared-test-identities"
	}
	if s.startupSuppressed {
		scope["startup"] = "verify-only"
	}
	if s.startupSuppressed && s.backgroundSuppressed {
		scope["scheduled"] = "disabled"
		scope["queues"] = "disabled"
	}
	if s.fixture != "" {
		digest := sha256.Sum256([]byte(s.fixture))
		scope["fixture_subject_sha256"] = hex.EncodeToString(digest[:])
	}
	return scope
}
