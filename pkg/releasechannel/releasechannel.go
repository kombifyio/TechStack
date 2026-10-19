// Package releasechannel decides which deployment serves a person and runs a
// homelab's work (CI-CD-PLATFORM-STANDARD §4.6.2). People choose Stable or
// Preview in kombify Cloud; Cloud stays the one store of that choice and this
// package mirrors it in memory only.
//
// A homelab follows its owner's channel. Stable serves and runs everyone who
// did not choose Preview; Preview runs only those who did. Shared
// (organization) tenants stay on Stable.
//
// Stable is the single authority for the hand-over. It reads Cloud's list,
// stops working a person the moment they appear in it and serves the list it
// honors to Preview. Preview works only the people on a list it read from
// stable within FreshFor. Stable resumes a person only HandBack after they
// left its list, and after its own boot, so every list Preview may still act
// on is one stable honored: no tenant is ever worked by both deployments.
package releasechannel

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/kombifyio/techstack/pkg/config"
)

// PersonalTenantPrefix is the canonical personal tenant namespace
// (`usr:<subject>`); the bare subject is its pre-standardization form.
const PersonalTenantPrefix = "usr:"

const (
	// FreshFor bounds how old a hand-over list Preview may act on.
	FreshFor = 60 * time.Second
	// HandBack is how long stable waits before working a person again; it
	// exceeds FreshFor with margin for a restart that overlaps the old process.
	HandBack = 5 * time.Minute
	// Settle is how long a person stays on stable's list before stable hands
	// them over, so a second stable process (a deploy overlap) has read the
	// same list and stopped working them too.
	Settle = 30 * time.Second
)

// Source reads the people who chose Preview. complete is false when the list
// is partial.
type Source func(ctx context.Context) (subjects []string, complete bool, err error)

// Directory holds the last list read.
type Directory struct {
	source Source
	now    func() time.Time

	mu       sync.RWMutex
	subjects map[string]struct{}
	since    map[string]time.Time
	loadedAt time.Time
	bootedAt time.Time
	left     map[string]time.Time
}

// NewDirectory returns a directory that has not read yet.
func NewDirectory(source Source) *Directory {
	return &Directory{source: source, now: time.Now, bootedAt: time.Now(), left: map[string]time.Time{}, since: map[string]time.Time{}}
}

// Refresh replaces the list. A failed or partial read keeps the previous one.
func (d *Directory) Refresh(ctx context.Context) error {
	subjects, complete, err := d.source(ctx)
	if err != nil {
		return err
	}
	if !complete {
		return errPartialList
	}
	next := make(map[string]struct{}, len(subjects))
	for _, subject := range subjects {
		if subject = strings.TrimSpace(subject); subject != "" {
			next[subject] = struct{}{}
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	for subject := range d.subjects {
		if _, still := next[subject]; !still {
			d.left[subject] = now
		}
	}
	for subject, at := range d.left {
		if _, back := next[subject]; back || now.Sub(at) >= HandBack {
			delete(d.left, subject)
		}
	}
	for subject := range d.since {
		if _, still := next[subject]; !still {
			delete(d.since, subject)
		}
	}
	for subject := range next {
		if _, known := d.since[subject]; !known {
			d.since[subject] = now
		}
	}
	d.subjects, d.loadedAt = next, now
	return nil
}

// Run refreshes until ctx ends. report sees each read's outcome (nil on success).
func (d *Directory) Run(ctx context.Context, interval time.Duration, report func(error)) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := d.Refresh(ctx); report != nil && ctx.Err() == nil {
			report(err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// Honored is the list stable hands to Preview: the people stable has not
// worked for at least Settle. ok is false before the first read.
func (d *Directory) Honored() (subjects []string, ok bool) {
	if d == nil {
		return nil, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.subjects == nil {
		return nil, false
	}
	now := d.now()
	subjects = make([]string, 0, len(d.subjects))
	for subject := range d.subjects {
		if now.Sub(d.since[subject]) >= Settle {
			subjects = append(subjects, subject)
		}
	}
	return subjects, true
}

// owns applies the channel's rule to one subject.
func (d *Directory) owns(subject string, preview bool) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	now := d.now()
	_, listed := d.subjects[subject]
	if preview {
		return subject != "" && d.subjects != nil && now.Sub(d.loadedAt) < FreshFor && listed
	}
	if now.Sub(d.bootedAt) < HandBack || listed {
		return false
	}
	at, left := d.left[subject]
	return !left || now.Sub(at) >= HandBack
}

// settled reports whether this channel's ownership is final enough for a
// one-time pass such as the boot requeue.
func (d *Directory) settled(preview bool) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	if preview {
		return d.subjects != nil
	}
	return d.now().Sub(d.bootedAt) >= HandBack
}

type partialListError struct{}

func (partialListError) Error() string { return "release channel list is partial" }

var errPartialList error = partialListError{}

var installed struct {
	sync.RWMutex
	directory *Directory
}

// Install makes directory the process-wide source of channel decisions.
func Install(directory *Directory) {
	installed.Lock()
	installed.directory = directory
	installed.Unlock()
}

func current() *Directory {
	installed.RLock()
	defer installed.RUnlock()
	return installed.directory
}

// Running is the channel this deployment serves.
func Running() string { return config.ReleaseChannel() }

// ChoosesPreview reports whether subject chose Preview; known is false when
// no list has been read.
func ChoosesPreview(subject string) (preview, known bool) {
	d := current()
	if d == nil {
		return false, false
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	if d.subjects == nil {
		return false, false
	}
	_, preview = d.subjects[strings.TrimSpace(subject)]
	return preview, true
}

// OwnsSubject reports whether this deployment runs subject's homelab work.
// Without a directory stable owns everyone and Preview no one.
func OwnsSubject(subject string) bool {
	d := current()
	preview := Running() == "preview"
	if d == nil {
		return !preview
	}
	return d.owns(strings.TrimSpace(subject), preview)
}

// TenantSubject is the owner of a personal tenant, else "".
func TenantSubject(tenantID string) string {
	tenantID = strings.TrimSpace(tenantID)
	if subject, ok := strings.CutPrefix(tenantID, PersonalTenantPrefix); ok {
		return strings.TrimSpace(subject)
	}
	if strings.Contains(tenantID, "|") {
		return tenantID
	}
	return ""
}

// OwnsTenant reports whether this deployment runs tenant's work. Shared and
// system tenants belong to Stable.
func OwnsTenant(tenantID string) bool {
	if subject := TenantSubject(tenantID); subject != "" {
		return OwnsSubject(subject)
	}
	return Running() != "preview"
}

// Settled reports whether ownership is final for a one-time pass: stable after
// HandBack from boot, Preview after its first read. Without a directory it is.
func Settled() bool {
	d := current()
	if d == nil {
		return true
	}
	return d.settled(Running() == "preview")
}

// State names the directory's condition for health reporting.
func State() string {
	d := current()
	if d == nil {
		return "unconfigured"
	}
	d.mu.RLock()
	defer d.mu.RUnlock()
	switch {
	case d.subjects == nil:
		return "loading"
	case d.now().Sub(d.loadedAt) < FreshFor:
		return "fresh"
	default:
		return "stale"
	}
}
