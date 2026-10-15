package routes

import (
	"net/http"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	ksapi "github.com/kombifyio/techstack/pkg/api"
	"github.com/kombifyio/techstack/pkg/appcatalog"
	"github.com/kombifyio/techstack/pkg/controlplane"
	"github.com/kombifyio/techstack/pkg/httpx"
	"github.com/kombifyio/techstack/pkg/jobs"
	"github.com/kombifyio/techstack/pkg/serviceregistry"
)

const (
	serviceActionUpdate = "update"
	serviceActionAdopt  = "adopt"
	// serviceActionBackup runs the stack's StackKits backup from a service
	// card. StackKits executes it on the node; the service is only the handle.
	serviceActionBackup = "backup"

	updateModeStackKitUpgrade = "stackkit_upgrade"
	updateModeAdoptRequired   = "adopt_required"
	updateModeUnavailable     = "unavailable"

	versionSourceImageTag        = "image_tag"
	versionSourceImageLabel      = "image_label"
	reasonCompilerReleaseUnknown = "compiler_release_unknown"
	reasonUpdateCheckDisabled    = "update_check_disabled"
)

// serviceVersion is the version/update fact of one service or application.
// Every field is measured or compared, never invented: `latest` is only set
// after a real registry or release-pin comparison.
type serviceVersion struct {
	Current         string     `json:"current,omitempty"`
	Source          string     `json:"source,omitempty"`
	Image           string     `json:"image,omitempty"`
	Latest          string     `json:"latest,omitempty"`
	UpdateAvailable bool       `json:"update_available"`
	CheckedAt       *time.Time `json:"checked_at,omitempty"`
	UpdateMode      string     `json:"update_mode"`
	ReasonCode      string     `json:"reason_code,omitempty"`
}

// serviceVersionFor derives the version fact of one service row.
//
// Managed components report their measured image version. The access manifest's
// StackKitVersion identifies the Kit Definition, not the CLI compiler release,
// so it cannot admit a comparison with the pinned compiler. Observed containers
// are compared with their registry and are only updatable once adopted.
func (h *serviceRuntimeHandlers) serviceVersionFor(service controlplane.ServiceRuntime) *serviceVersion {
	image := stringFromAnyMap(service.Metadata, "image")
	label := stringFromAnyMap(service.Metadata, "image_version")
	if serviceregistry.CanonicalManagementState(service.ManagementState) == serviceregistry.ManagementManaged {
		return h.managedVersion(service, image, label)
	}
	if appcatalog.ParseImage(image).Repo == "" && label == "" {
		return nil
	}
	current, source, reason := appcatalog.CurrentVersion(image, label)
	version := &serviceVersion{Image: image, UpdateMode: updateModeAdoptRequired, ReasonCode: reason}
	if current == "" {
		return version
	}
	version.Current, version.Source = current, source
	if h.updates == nil {
		version.ReasonCode = reasonUpdateCheckDisabled
		return version
	}
	result := h.updates.Check(image, current)
	if !result.CheckedAt.IsZero() {
		checkedAt := result.CheckedAt.UTC()
		version.CheckedAt = &checkedAt
	}
	version.ReasonCode = result.ReasonCode
	if result.Latest != "" {
		version.Latest, version.UpdateAvailable = result.Latest, result.Newer
	}
	return version
}

func (h *serviceRuntimeHandlers) managedVersion(service controlplane.ServiceRuntime, image, label string) *serviceVersion {
	current, source, _ := appcatalog.CurrentVersion(image, label)
	return &serviceVersion{Current: current, Source: source, Image: image,
		UpdateMode: updateModeUnavailable, ReasonCode: reasonCompilerReleaseUnknown}
}

// stackKitReleaseVersion reduces "kit@5.1.0", "v5.1.0" and "5.1.0" to "5.1.0".
// Anything that is not a plain version yields "": a lifecycle label is not a
// release identity.
func stackKitReleaseVersion(identity string) string {
	_, version, found := strings.Cut(strings.TrimSpace(identity), "@")
	if !found {
		version = identity
	}
	version = strings.TrimPrefix(strings.TrimSpace(version), "v")
	if _, ok := appcatalog.CompareVersions(version, version); !ok {
		return ""
	}
	return version
}

// updateAllowed is the single rule for the `update` action, used both to
// advertise it and to enforce it. Only a StackKit-managed service whose stack
// is behind the pinned release can be updated, by upgrading its stack, and
// only while its server admits the stack upgrade.
func updateAllowed(service controlplane.ServiceRuntime, version *serviceVersion, server *controlplane.ServerRuntime) bool {
	if version == nil || !version.UpdateAvailable || version.UpdateMode != updateModeStackKitUpgrade ||
		service.MutationLock.Locked() || len(serviceGroupLockSources(service)) != 0 || server == nil || adoptedApplication(&service) {
		return false
	}
	for _, action := range serverStackActions(*server) {
		if action == serverStackActionUpgrade {
			return true
		}
	}
	return false
}

// backupAllowed advertises and enforces Back up now for a stacked service whose
// node accepts direct StackKits backup operations (Basement kits; managed kits
// keep their backup schedule). Reading a locked service is not a mutation, and
// neither is backing it up, so the owner guardrail does not suppress it.
func backupAllowed(service controlplane.ServiceRuntime, server *controlplane.ServerRuntime) bool {
	if server == nil || adoptedApplication(&service) ||
		serviceregistry.CanonicalManagementState(service.ManagementState) != serviceregistry.ManagementManaged {
		return false
	}
	for _, action := range serverStackActions(*server) {
		if action == serverStackActionBackupStatus {
			return true
		}
	}
	return false
}

// pinnedStackKitRelease returns the StackKits release served to managed agents.
// Resolving the bundle verifies its files, so the answer is kept for a short
// time instead of being recomputed per service row.
func pinnedStackKitRelease() func() string {
	var (
		mu      sync.Mutex
		value   string
		expires time.Time
	)
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		if time.Now().Before(expires) {
			return value
		}
		value = ""
		if release, err := jobs.CachedTargetStackKitRelease(); err == nil && release != nil {
			value = release.Receipt().Version
		}
		expires = time.Now().Add(5 * time.Minute)
		return value
	}
}

// applicationActionUnion folds per-component allowed actions into the
// application-wide set the card offers.
func applicationActionUnion(components []serviceRuntimeResponse) []string {
	offered := map[string]bool{}
	for _, component := range components {
		for _, action := range component.AllowedActions {
			switch action {
			case serviceActionStart, serviceActionStop, serviceActionRestart, serviceActionLogs, serviceActionUpdate, serviceActionAdopt:
				offered[action] = true
			}
		}
	}
	union := make([]string, 0, len(offered))
	for action := range offered {
		union = append(union, action)
	}
	sort.Strings(union)
	return union
}

var upgradeReleasePattern = regexp.MustCompile(`^v[0-9]+\.[0-9]+\.[0-9]+(?:-(?:beta|edge)\.[0-9]+)?$`)

// upgradeTargetRelease names the pinned release for the stack upgrade; a pin
// the upgrade command would not accept falls back to its own `latest`.
func upgradeTargetRelease(pinned string) string {
	target := "v" + stackKitReleaseVersion(pinned)
	if !upgradeReleasePattern.MatchString(target) {
		return "latest"
	}
	return target
}

// refuseLockedStackServices fails closed: a stack upgrade writes to every
// service of the stack, so one owner-locked service refuses it, and an
// unreadable lock state refuses it too.
func (h *serviceRuntimeHandlers) refuseLockedStackServices(e *httpx.Event, tenantID, stackID string) error {
	rows, err := h.store.ListServiceRuntimes(e.Request.Context(), tenantID, stackID, "")
	if err != nil {
		return httpx.Error(e, http.StatusServiceUnavailable, ksapi.ErrCodeUnavailable, "Failed to read service lock state", nil)
	}
	locked := make([]string, 0, len(rows))
	for i := range rows {
		if rows[i].MutationLock.Locked() {
			locked = append(locked, rows[i].ID)
		}
	}
	if len(locked) == 0 {
		return nil
	}
	return httpx.Error(e, http.StatusConflict, ksapi.ErrCodeConflict, "Stack contains locked services", map[string]any{
		"reason": "service_locked", "locked_service_ids": locked,
	})
}
