// Package homelabdigest reports Techstack's Homelab numbers to the Companion
// Feed digest (Feed contract v1 sections 2.4 and 2.5). The notifications engine
// composes the daily digest; this job only upserts one contribution per owner
// and local day, producer techstack.homelab, and only when the numbers change.
package homelabdigest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // the Europe/Berlin fallback must not depend on the host

	"github.com/kombifyio/techstack/internal/notifications"
)

const (
	// MinSendInterval is the contract's bound: a producer writes at most every
	// 15 minutes per user.
	MinSendInterval = 15 * time.Minute
	// DefaultPollInterval is how often the job looks for changed numbers.
	DefaultPollInterval = 5 * time.Minute
	// FallbackTimezone is the day boundary until a user timezone is known.
	FallbackTimezone = "Europe/Berlin"

	// MessageID is the language-neutral digest line of the Homelab row.
	MessageID = "techstack.digest.homelab"
	// DeepLink opens the Homelab view.
	DeepLink = "/dashboard"
)

// Facts are the Homelab numbers of one owner for the current day. ServersTotal
// and CriticalFindings shape the tones and are not sent as metrics.
type Facts struct {
	ServersOK        int
	ServersTotal     int
	BackupsOK        int
	BackupsFailed    int
	UpdatesReady     int
	OpenFindings     int
	CriticalFindings int
}

func (f Facts) empty() bool {
	return f.ServersTotal == 0 && f.ServersOK == 0 && f.BackupsOK == 0 && f.BackupsFailed == 0 && f.UpdatesReady == 0 && f.OpenFindings == 0
}

// OwnerFacts pairs an owner with the numbers of the day.
type OwnerFacts struct {
	OwnerSubject string
	Facts        Facts
}

// Source reads the Homelab numbers of every owner for the local day that
// starts at dayStart (inclusive) and ends 24 hours later.
type Source interface {
	Snapshot(ctx context.Context, dayStart time.Time) ([]OwnerFacts, error)
}

// Sender is the notifications engine's contribution route.
type Sender interface {
	UpsertDigestContribution(ctx context.Context, contribution notifications.DigestContribution) error
}

type ownerState struct {
	hash   string
	sentAt time.Time
}

// Job upserts the Homelab contribution of every owner when it changes.
type Job struct {
	Source   Source
	Sender   Sender
	Location *time.Location
	Interval time.Duration
	Now      func() time.Time
	Logger   *slog.Logger

	mu    sync.Mutex
	state map[string]ownerState
}

// Run polls until ctx ends.
func (j *Job) Run(ctx context.Context) {
	if j == nil || j.Source == nil || j.Sender == nil {
		return
	}
	interval := j.Interval
	if interval <= 0 {
		interval = DefaultPollInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if _, err := j.RunOnce(ctx); err != nil && ctx.Err() == nil {
			j.logger().Warn("homelab_digest_pass_failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// RunOnce sends the contributions whose numbers changed since the last send and
// whose last send is at least MinSendInterval old. It returns how many it sent.
func (j *Job) RunOnce(ctx context.Context) (int, error) {
	now := j.now()
	location := j.location()
	local := now.In(location)
	dayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	day := dayStart.Format("2006-01-02")
	snapshot, err := j.Source.Snapshot(ctx, dayStart)
	if err != nil {
		return 0, fmt.Errorf("homelab digest snapshot: %w", err)
	}
	sent := 0
	var firstErr error
	for _, owner := range snapshot {
		if owner.OwnerSubject == "" || owner.Facts.empty() {
			continue
		}
		contribution := Contribution(owner.OwnerSubject, day, owner.Facts)
		hash := contributionHash(contribution)
		if !j.due(owner.OwnerSubject, hash, now) {
			continue
		}
		if err := j.Sender.UpsertDigestContribution(ctx, contribution); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("homelab digest contribution: %w", err)
			}
			continue
		}
		j.record(owner.OwnerSubject, hash, now)
		sent++
	}
	return sent, firstErr
}

func (j *Job) due(owner, hash string, now time.Time) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	previous, seen := j.state[owner]
	if !seen {
		return true
	}
	if previous.hash == hash {
		return false
	}
	return now.Sub(previous.sentAt) >= MinSendInterval
}

func (j *Job) record(owner, hash string, now time.Time) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.state == nil {
		j.state = map[string]ownerState{}
	}
	j.state[owner] = ownerState{hash: hash, sentAt: now}
}

func (j *Job) now() time.Time {
	if j.Now != nil {
		return j.Now().UTC()
	}
	return time.Now().UTC()
}

func (j *Job) location() *time.Location {
	if j.Location != nil {
		return j.Location
	}
	if location, err := time.LoadLocation(FallbackTimezone); err == nil {
		return location
	}
	return time.UTC
}

func (j *Job) logger() *slog.Logger {
	if j.Logger != nil {
		return j.Logger
	}
	return slog.Default()
}

// Contribution renders the Homelab numbers as a digest contribution. Failed
// backups and open findings are warn or critical when above zero and neutral at
// zero; a healthy count is good.
func Contribution(ownerSubject, day string, facts Facts) notifications.DigestContribution {
	serverTone := "neutral"
	switch {
	case facts.ServersTotal > 0 && facts.ServersOK == facts.ServersTotal:
		serverTone = "good"
	case facts.ServersTotal > facts.ServersOK:
		serverTone = "warn"
	}
	backupTone := "neutral"
	if facts.BackupsOK > 0 {
		backupTone = "good"
	}
	failedTone := "neutral"
	if facts.BackupsFailed > 0 {
		failedTone = "critical"
	}
	findingsTone := "neutral"
	switch {
	case facts.CriticalFindings > 0:
		findingsTone = "critical"
	case facts.OpenFindings > 0:
		findingsTone = "warn"
	}
	return notifications.DigestContribution{
		Auth0UserID: ownerSubject,
		Section:     notifications.DigestSectionHomelab,
		Producer:    notifications.DigestProducerHomelab,
		Day:         day,
		Message: notifications.DigestMessage{ID: MessageID, Params: map[string]any{
			"servers_ok": facts.ServersOK, "backups_ok": facts.BackupsOK, "backups_failed": facts.BackupsFailed,
			"updates_ready": facts.UpdatesReady, "open_findings": facts.OpenFindings,
		}},
		Metrics: []notifications.DigestMetric{
			{ID: "servers_ok", Value: facts.ServersOK, Tone: serverTone},
			{ID: "backups_ok", Value: facts.BackupsOK, Tone: backupTone},
			{ID: "backups_failed", Value: facts.BackupsFailed, Tone: failedTone},
			{ID: "updates_ready", Value: facts.UpdatesReady, Tone: "neutral"},
			{ID: "open_findings", Value: facts.OpenFindings, Tone: findingsTone},
		},
		Pointers: []notifications.DigestPointer{},
		DeepLink: DeepLink,
	}
}

func contributionHash(c notifications.DigestContribution) string {
	var canonical strings.Builder
	canonical.WriteString(c.Day + "|" + c.Message.ID + "|")
	for _, metric := range c.Metrics {
		canonical.WriteString(metric.ID + "=" + strconv.Itoa(metric.Value) + ":" + metric.Tone + ";")
	}
	sum := sha256.Sum256([]byte(canonical.String()))
	return hex.EncodeToString(sum[:])
}
