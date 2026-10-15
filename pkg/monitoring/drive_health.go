package monitoring

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
	"github.com/kombifyio/techstack/pkg/guardtelemetry"
	"github.com/google/uuid"
)

// DriveAlert is one durable condition episode. Observation failures are separate
// from hardware evidence; recovery is recorded only when fresh evidence covers it.
type DriveAlert struct {
	ID              string            `json:"id"`
	TenantID        string            `json:"-"`
	WorkerID        string            `json:"worker_id"`
	ConditionKey    string            `json:"condition_key,omitempty"`
	ServerID        string            `json:"server_id"`
	DriveID         string            `json:"drive_id,omitempty"`
	Category        string            `json:"category"`
	Code            string            `json:"code"`
	Severity        string            `json:"severity"`
	Message         string            `json:"message"`
	Evidence        map[string]string `json:"evidence"`
	RecommendedStep string            `json:"recommended_step"`
	FirstSeenAt     time.Time         `json:"first_seen_at"`
	LastSeenAt      time.Time         `json:"last_seen_at"`
	ResolvedAt      *time.Time        `json:"resolved_at,omitempty"`
}
type DriveState struct {
	DriveID           string               `json:"drive_id"`
	DevicePath        string               `json:"device_path"`
	ObservationStatus string               `json:"observation_status"`
	Health            string               `json:"health"`
	LastAttemptAt     time.Time            `json:"last_attempt_at"`
	LastAttempt       *drivehealth.Reading `json:"last_attempt"`
	LastSuccess       *drivehealth.Reading `json:"last_success"`
	Alerts            []DriveAlert         `json:"alerts"`
}
type DriveServerState struct {
	TenantID          string       `json:"-"`
	ServerID          string       `json:"server_id"`
	WorkerID          string       `json:"worker_id"`
	SourceEpoch       string       `json:"-"`
	Sequence          int64        `json:"-"`
	LastAttemptAt     time.Time    `json:"last_attempt_at"`
	CollectionStatus  string       `json:"collection_status"`
	CollectionReason  string       `json:"collection_reason,omitempty"`
	InventoryComplete bool         `json:"inventory_complete"`
	Drives            []DriveState `json:"drives"`
	Alerts            []DriveAlert `json:"alerts"`
}
type DriveHealthResponse struct {
	GeneratedAt time.Time          `json:"generated_at"`
	Servers     []DriveServerState `json:"servers"`
}

// DriveHealthStore commits the state and its notification events atomically.
// Empty tenant is the self-hosted tenant, never a wildcard.
type DriveHealthStore interface {
	Update(context.Context, guardtelemetry.Identity, func(*DriveServerState) ([]DriveAlert, error)) error
	List(context.Context, string, string) ([]DriveServerState, error)
	Deliver(context.Context, string, Notifier) error
	ListTenants(context.Context, string, int) ([]string, error)
}
type DriveHealthService struct {
	Store        DriveHealthStore
	Notifier     Notifier
	Now          func() time.Time
	StaleAfter   time.Duration
	TenantScopes func(context.Context) ([]string, error)
	OnError      func(error)
}

func (s *DriveHealthService) now() time.Time {
	if s.Now != nil {
		return s.Now().UTC()
	}
	return time.Now().UTC()
}
func (s *DriveHealthService) staleAfter() time.Duration {
	if s.StaleAfter > 0 {
		return s.StaleAfter
	}
	return 30 * time.Minute
}

func (s *DriveHealthService) Ingest(ctx context.Context, id guardtelemetry.Identity, epoch string, sequence int64, c drivehealth.Collection) error {
	if s.Store == nil || id.ServerID == "" || id.WorkerID == "" || epoch == "" || sequence <= 0 {
		return errors.New("drive health needs authenticated server identity and storage")
	}
	if err := c.Validate(s.now()); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, r := range c.Drives {
		if r.ObservedAt.IsZero() || r.ObservedAt.After(c.ObservedAt.Add(time.Minute)) || r.ObservedAt.Before(c.ObservedAt.Add(-time.Minute)) {
			return errors.New("invalid drive observation time")
		}
		if r.DriveID != "" {
			if seen[r.DriveID] {
				return errors.New("duplicate drive identity")
			}
			seen[r.DriveID] = true
		}
	}
	return s.Store.Update(ctx, id, func(state *DriveServerState) ([]DriveAlert, error) {
		if (!state.LastAttemptAt.IsZero() && (c.ObservedAt.Before(state.LastAttemptAt) || (state.SourceEpoch != epoch && !c.ObservedAt.After(state.LastAttemptAt)))) || (state.SourceEpoch == epoch && sequence <= state.Sequence) {
			return nil, nil
		}
		state.WorkerID = id.WorkerID
		state.SourceEpoch = epoch
		state.Sequence = sequence
		state.LastAttemptAt = c.ObservedAt.UTC()
		state.CollectionStatus = c.Status
		state.CollectionReason = c.Reason
		state.InventoryComplete = c.Complete
		// Anonymous readings have no continuity across collection attempts.
		retained := state.Drives[:0]
		for _, d := range state.Drives {
			if !strings.HasPrefix(d.DriveID, "unidentified-") {
				retained = append(retained, d)
			}
		}
		state.Drives = retained
		var events []DriveAlert
		collection := map[string]DriveAlert{}
		covered := map[string]bool{"collection": true, "stale": true}
		if c.Status != "available" {
			collection["collection"] = condition("collection", "collection", "warning", "Drive health collection is unavailable", map[string]string{"status": c.Status, "reason": c.Reason}, "Check Guard permissions and the installed drive inspection tool.")
		}
		for _, r := range c.Drives {
			key := driveAvailabilityKey(r.DevicePath)
			if r.DriveID == "" {
				a := condition("collection", "observation", "warning", "Drive identity and health cannot be attributed", map[string]string{"device_path": r.DevicePath, "status": r.Status, "reason": r.Reason, "source": r.Source, "observed_at": r.ObservedAt.UTC().Format(time.RFC3339Nano)}, "Check drive access and read its hardware identity before assessing health.")
				a.ConditionKey = key
				collection[key] = a
				covered[key] = true
			} else if r.Status == drivehealth.Available {
				covered[key] = true
			}
		}
		events = append(events, reconcileDriveAlerts(state, "", collection, covered, c.ObservedAt)...)
		old := map[string]*DriveState{}
		for i := range state.Drives {
			old[state.Drives[i].DriveID] = &state.Drives[i]
		}
		observed := map[string]bool{}
		anonymousPaths := map[string]bool{}
		for _, r := range c.Drives {
			if r.DriveID == "" {
				anonymousPaths[r.DevicePath] = true
			}
		}
		for n, r := range c.Drives {
			driveID := r.DriveID
			if driveID == "" {
				hash := sha256.Sum256([]byte(fmt.Sprintf("%s/%d/%d", epoch, sequence, n)))
				driveID = fmt.Sprintf("unidentified-%x", hash[:10])
			}
			observed[driveID] = true
			d := old[driveID]
			if d == nil {
				state.Drives = append(state.Drives, DriveState{DriveID: driveID, Alerts: []DriveAlert{}})
				d = &state.Drives[len(state.Drives)-1]
			}
			previous := d.LastSuccess
			d.DevicePath = r.DevicePath
			d.ObservationStatus = r.Status
			if r.Status == drivehealth.Missing {
				d.ObservationStatus = "disconnected"
			}
			copyReading := r
			d.LastAttempt = &copyReading
			d.LastAttemptAt = r.ObservedAt
			d.Health = "unknown"
			current, coverage := driveConditions(r, previous)
			coverage["disconnected"] = true
			coverage["stale"] = true
			if r.Status == drivehealth.Missing {
				current["disconnected"] = condition("collection", "disconnected", "warning", "Previously observed drive is absent from the complete inventory", map[string]string{"device_path": r.DevicePath}, "Check the drive connection or confirm the hardware replacement.")
			} else if r.Status != "available" {
				current["observation"] = condition("collection", "observation", "warning", "Drive observation is incomplete or unavailable", map[string]string{"status": r.Status, "reason": r.Reason}, "Check drive access and missing SMART or NVMe fields.")
			}
			coverage["observation"] = true
			for code, alert := range current {
				if alert.Evidence == nil {
					alert.Evidence = map[string]string{}
				}
				alert.Evidence["device_path"] = r.DevicePath
				alert.Evidence["source"] = r.Source
				alert.Evidence["observed_at"] = r.ObservedAt.UTC().Format(time.RFC3339Nano)
				current[code] = alert
			}
			if r.DriveID != "" {
				events = append(events, reconcileDriveAlerts(state, driveID, current, coverage, r.ObservedAt)...)
			}
			// Re-find after append: slice growth can move addresses retained in old.
			d = findDrive(state, driveID)
			if r.DriveID != "" && r.Status == "available" {
				success := r
				d.LastSuccess = &success
			}
			d.Health = driveRisk(r, d.Alerts)
			if r.DriveID == "" {
				d.Health = "unknown"
			}
			old = map[string]*DriveState{}
			for i := range state.Drives {
				old[state.Drives[i].DriveID] = &state.Drives[i]
			}
		}
		for i := range state.Drives {
			d := &state.Drives[i]
			if !observed[d.DriveID] && anonymousPaths[d.DevicePath] {
				d.ObservationStatus = "incomplete"
				d.Health = "unknown"
				d.LastAttemptAt = c.ObservedAt
				// The server/path availability episode owns this ambiguous attempt.
				continue
			}
			if !observed[d.DriveID] && (c.Status != drivehealth.Available || !c.Complete) && d.ObservationStatus != "disconnected" {
				d.ObservationStatus = c.Status
				if d.ObservationStatus == drivehealth.Available {
					d.ObservationStatus = drivehealth.Incomplete
				}
				d.Health = "unknown"
				d.LastAttemptAt = c.ObservedAt
				continue
			}
			if !observed[d.DriveID] && c.Status == "available" && c.Complete {
				d.ObservationStatus = "disconnected"
				d.Health = "unknown"
				events = append(events, reconcileDriveAlerts(state, d.DriveID, map[string]DriveAlert{"disconnected": condition("collection", "disconnected", "warning", "Previously observed drive is absent from the complete inventory", map[string]string{"device_path": d.DevicePath}, "Check the drive connection or confirm the hardware replacement.")}, map[string]bool{"disconnected": true}, c.ObservedAt)...)
			}
		}
		pruneDriveHistory(state, c.ObservedAt)
		sort.Slice(state.Drives, func(i, j int) bool { return state.Drives[i].DriveID < state.Drives[j].DriveID })
		return events, nil
	})
}
func findDrive(s *DriveServerState, id string) *DriveState {
	for i := range s.Drives {
		if s.Drives[i].DriveID == id {
			return &s.Drives[i]
		}
	}
	return nil
}
func condition(category, code, severity, message string, evidence map[string]string, step string) DriveAlert {
	return DriveAlert{Category: category, Code: code, Severity: severity, Message: message, Evidence: evidence, RecommendedStep: step}
}
func reconcileDriveAlerts(s *DriveServerState, driveID string, current map[string]DriveAlert, covered map[string]bool, at time.Time) []DriveAlert {
	alerts := &s.Alerts
	if driveID != "" {
		alerts = &findDrive(s, driveID).Alerts
	}
	if *alerts == nil {
		*alerts = []DriveAlert{}
	}
	var events []DriveAlert
	for i := range *alerts {
		a := &(*alerts)[i]
		if a.ResolvedAt != nil {
			continue
		}
		key := a.Code
		if a.ConditionKey != "" {
			key = a.ConditionKey
		}
		if c, ok := current[key]; ok {
			if c.Severity != a.Severity && severityRank(c.Severity) > severityRank(a.Severity) {
				a.Severity = c.Severity
				a.Evidence = c.Evidence
				a.Message = c.Message
				a.LastSeenAt = at
				events = append(events, *a)
			} else {
				a.Severity = c.Severity
				a.Message = c.Message
				a.RecommendedStep = c.RecommendedStep
				a.LastSeenAt = at
				a.Evidence = c.Evidence
			}
			delete(current, key)
		} else if covered[key] {
			resolved := at
			a.ResolvedAt = &resolved
			a.LastSeenAt = at
			events = append(events, *a)
		}
	}
	keys := make([]string, 0, len(current))
	for code := range current {
		keys = append(keys, code)
	}
	sort.Strings(keys)
	for _, code := range keys {
		a := current[code]
		a.ID = uuid.NewString()
		a.TenantID = s.TenantID
		a.WorkerID = s.WorkerID
		a.ServerID = s.ServerID
		a.DriveID = driveID
		a.FirstSeenAt = at
		a.LastSeenAt = at
		*alerts = append(*alerts, a)
		events = append(events, a)
	}
	return events
}
func severityRank(s string) int {
	switch s {
	case "critical":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	}
	return 0
}
func driveRisk(r drivehealth.Reading, alerts []DriveAlert) string {
	status := r.Status
	if status != "available" && status != "incomplete" {
		return "unknown"
	}
	risk := "healthy"
	if status == "incomplete" || !driveEvidenceAvailable(r) {
		risk = "unknown"
	}
	for _, a := range alerts {
		if a.ResolvedAt == nil && a.Category == "hardware" {
			if a.Severity == "critical" {
				return "critical"
			}
			risk = "warning"
		}
	}
	return risk
}
func driveConditions(r drivehealth.Reading, previous *drivehealth.Reading) (map[string]DriveAlert, map[string]bool) {
	out := map[string]DriveAlert{}
	covered := map[string]bool{}
	if r.Status != drivehealth.Available && r.Status != drivehealth.Incomplete {
		return out, covered
	}
	add := func(code, severity string, evidence map[string]string, step string) {
		out[code] = condition("hardware", code, severity, "Drive reports a hardware health condition", evidence, step)
	}
	boolCheck := func(code string, value *bool, bad bool) {
		if value == nil {
			return
		}
		covered[code] = true
		if *value == bad {
			add(code, "critical", map[string]string{"reported": fmt.Sprint(*value)}, "Back up data immediately and inspect or replace the drive.")
		}
	}
	boolCheck("smart_failed", r.SmartPassed, false)
	boolCheck("self_test_failed", r.SelfTestFailed, true)
	boolCheck("attribute_failed", r.AttributeFailed, true)
	if r.CriticalWarning != nil {
		covered["nvme_warning"] = true
		if *r.CriticalWarning != 0 {
			add("nvme_warning", "critical", map[string]string{"critical_warning": fmt.Sprint(*r.CriticalWarning)}, "Back up data and inspect the NVMe critical warning before further writes.")
		}
	}
	if r.PercentageUsed != nil {
		covered["wear"] = true
		if *r.PercentageUsed >= 90 {
			add("wear", "warning", map[string]string{"percentage_used": fmt.Sprint(*r.PercentageUsed), "interpretation": "estimated_rated_endurance_consumed"}, "Plan replacement and verify backups; consumed rated endurance alone is not a hardware failure or lifetime prediction.")
		}
	}
	if r.TemperatureCelsius != nil {
		covered["temperature"] = true
		warning, critical := 65.0, 80.0
		warningSource, criticalSource := "guard_policy", "guard_policy"
		if r.TemperatureWarningCelsius != nil {
			warning = *r.TemperatureWarningCelsius
			warningSource = "device"
		}
		if r.TemperatureCriticalCelsius != nil {
			critical = *r.TemperatureCriticalCelsius
			criticalSource = "device"
		}
		if critical <= warning {
			if r.TemperatureCriticalCelsius == nil {
				critical = warning + 15
			} else if r.TemperatureWarningCelsius == nil {
				warning = critical - 10
			}
		}
		severity, threshold, source := "", warning, warningSource
		if *r.TemperatureCelsius >= critical {
			severity = "critical"
			threshold = critical
			source = criticalSource
		} else if *r.TemperatureCelsius >= warning {
			severity = "warning"
		}
		if severity != "" {
			add("temperature", severity, map[string]string{"temperature_celsius": fmt.Sprint(*r.TemperatureCelsius), "threshold_celsius": fmt.Sprint(threshold), "threshold_source": source}, "Check cooling, airflow, and device temperature limits; reduce load while investigating excessive heat.")
		}
	}
	if r.AvailableSpare != nil && r.SpareThreshold != nil {
		covered["spare"] = true
		if *r.AvailableSpare < *r.SpareThreshold {
			add("spare", "critical", map[string]string{"available_spare": fmt.Sprint(*r.AvailableSpare), "spare_threshold": fmt.Sprint(*r.SpareThreshold)}, "Back up data and replace the drive with depleted spare capacity.")
		}
	}
	counter := func(code string, value, old *string, severity string, historical bool) {
		if value == nil {
			return
		}
		v, ok := new(big.Int).SetString(*value, 10)
		if !ok || v.Sign() < 0 {
			return
		}
		covered[code] = true
		covered[code+"_increase"] = true
		if historical && v.Sign() > 0 {
			add(code, severity, map[string]string{"cumulative": *value, "interpretation": "historical_total"}, "Verify backups and monitor subsequent readings for increases.")
		}
		if old != nil {
			p, valid := new(big.Int).SetString(*old, 10)
			if valid && v.Cmp(p) > 0 {
				increaseSeverity, step := "critical", "Back up data immediately and investigate the increasing drive error counter."
				if code == "error_log_entries" {
					increaseSeverity = "warning"
					step = "Inspect the NVMe error log; command errors alone do not establish media failure."
				}
				add(code+"_increase", increaseSeverity, map[string]string{"previous": *old, "current": *value, "increase": new(big.Int).Sub(v, p).String()}, step)
			}
		}
	}
	var media, logs, reallocated, pending, uncorrectable *string
	if previous != nil {
		media = previous.MediaErrors
		logs = previous.ErrorLogEntries
		reallocated = previous.ReallocatedSectors
		pending = previous.PendingSectors
		uncorrectable = previous.UncorrectableSectors
	}
	counter("media_errors", r.MediaErrors, media, "warning", true)
	counter("error_log_entries", r.ErrorLogEntries, logs, "warning", false)
	counter("reallocated_sectors", r.ReallocatedSectors, reallocated, "warning", true)
	counter("pending_sectors", r.PendingSectors, pending, "warning", true)
	counter("uncorrectable_sectors", r.UncorrectableSectors, uncorrectable, "critical", true)
	return out, covered
}

// Read ages evidence under the same serialized transaction as ingestion, so an
// API read cannot overwrite a concurrently arriving recovery.
func (s *DriveHealthService) Read(ctx context.Context, tenantID, serverID string) (DriveHealthResponse, error) {
	now := s.now()
	states, err := s.Store.List(ctx, tenantID, serverID)
	if err != nil {
		return DriveHealthResponse{}, err
	}
	for _, state := range states {
		id := guardtelemetry.Identity{TenantID: tenantID, ServerID: state.ServerID, WorkerID: state.WorkerID}
		err = s.Store.Update(ctx, id, func(latest *DriveServerState) ([]DriveAlert, error) {
			var events []DriveAlert
			for i := range latest.Drives {
				d := &latest.Drives[i]
				if d.LastAttemptAt.IsZero() || now.Sub(d.LastAttemptAt) <= s.staleAfter() || d.ObservationStatus == "disconnected" {
					continue
				}
				d.ObservationStatus = "stale"
				d.Health = "unknown"
				events = append(events, reconcileDriveAlerts(latest, d.DriveID, map[string]DriveAlert{"stale": condition("collection", "stale", "warning", "Drive health observation is stale", map[string]string{"last_attempt_at": d.LastAttemptAt.Format(time.RFC3339)}, "Restore Guard connectivity and collect fresh drive health readings.")}, map[string]bool{"stale": true}, now)...)
			}
			if now.Sub(latest.LastAttemptAt) > s.staleAfter() {
				latest.CollectionStatus = "stale"
				latest.CollectionReason = "collection_not_recent"
				events = append(events, reconcileDriveAlerts(latest, "", map[string]DriveAlert{"stale": condition("collection", "stale", "warning", "Drive health observations are stale", map[string]string{"last_attempt_at": latest.LastAttemptAt.Format(time.RFC3339)}, "Restore Guard connectivity and collect fresh drive health readings.")}, map[string]bool{"stale": true}, now)...)
			}
			return events, nil
		})
		if err != nil {
			return DriveHealthResponse{}, err
		}
	}
	states, err = s.Store.List(ctx, tenantID, serverID)
	if states == nil {
		states = []DriveServerState{}
	}
	if serverID != "" && len(states) == 0 {
		states = append(states, DriveServerState{ServerID: serverID, CollectionStatus: "missing", CollectionReason: "no_observation", Drives: []DriveState{}, Alerts: []DriveAlert{}})
	}
	return DriveHealthResponse{GeneratedAt: now, Servers: states}, err
}

// Run ages evidence and retries durable notification events in tenant scopes.
func (s *DriveHealthService) Run(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		if s.TenantScopes != nil {
			tenants, err := s.TenantScopes(ctx)
			s.reportError(err)
			if err == nil {
				for _, tenant := range tenants {
					s.reconcileTenant(ctx, tenant)
				}
			}
		} else {
			after := ""
			for {
				tenants, err := s.Store.ListTenants(ctx, after, 100)
				if err != nil {
					s.reportError(err)
					break
				}
				for _, tenant := range tenants {
					s.reconcileTenant(ctx, tenant)
				}
				if len(tenants) < 100 {
					break
				}
				after = tenants[len(tenants)-1]
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *DriveHealthService) reconcileTenant(ctx context.Context, tenant string) {
	_, err := s.Read(ctx, tenant, "")
	s.reportError(err)
	if s.Notifier != nil {
		s.reportError(s.Store.Deliver(ctx, tenant, s.Notifier))
	}
}
func (s *DriveHealthService) reportError(err error) {
	if err != nil && s.OnError != nil {
		s.OnError(err)
	}
}
func driveNotification(a DriveAlert) Alert {
	evidence, _ := json.Marshal(a.Evidence)
	return Alert{RuleName: "DriveHealth:" + a.Code, Severity: a.Severity, Message: strings.Join([]string{a.Message, fmt.Sprintf("Drive %s at %s", a.DriveID, a.Evidence["device_path"]), string(evidence), a.RecommendedStep}, " "), Labels: map[string]string{"tenant_id": a.TenantID, "server_id": a.ServerID, "drive_id": a.DriveID, "event_id": a.ID, "condition_id": a.ID, "agent_id": a.WorkerID, "worker_id": a.WorkerID, "device_path": a.Evidence["device_path"], "source": a.Evidence["source"], "observed_at": a.Evidence["observed_at"], "recommended_step": a.RecommendedStep, "category": a.Category, "message_id": "techstack.monitoring.drive_health." + a.Code, "evidence": string(evidence)}, FiredAt: a.FirstSeenAt, ResolvedAt: a.ResolvedAt}
}

func driveEvidenceAvailable(r drivehealth.Reading) bool {
	if len(r.Missing) > 0 {
		return false
	}
	switch r.Protocol {
	case "NVMe":
		return r.CriticalWarning != nil && r.PercentageUsed != nil && r.AvailableSpare != nil && r.SpareThreshold != nil && r.MediaErrors != nil && r.ErrorLogEntries != nil && r.TemperatureCelsius != nil
	case "ATA":
		return r.SmartPassed != nil && r.SelfTestFailed != nil && r.TemperatureCelsius != nil
	case "SCSI":
		return r.SmartPassed != nil && r.SelfTestFailed != nil && r.TemperatureCelsius != nil
	}
	return false
}
func pruneDriveHistory(s *DriveServerState, now time.Time) {
	prune := func(alerts []DriveAlert) []DriveAlert {
		out := []DriveAlert{}
		resolved := []DriveAlert{}
		for _, a := range alerts {
			if a.ResolvedAt == nil {
				out = append(out, a)
			} else if now.Sub(*a.ResolvedAt) <= 90*24*time.Hour {
				resolved = append(resolved, a)
			}
		}
		if len(resolved) > 64 {
			resolved = resolved[len(resolved)-64:]
		}
		return append(resolved, out...)
	}
	s.Alerts = prune(s.Alerts)
	for i := range s.Drives {
		s.Drives[i].Alerts = prune(s.Drives[i].Alerts)
	}
	sort.SliceStable(s.Drives, func(i, j int) bool {
		a, b := s.Drives[i], s.Drives[j]
		if (a.ObservationStatus == "disconnected") != (b.ObservationStatus == "disconnected") {
			return a.ObservationStatus != "disconnected"
		}
		if a.LastAttempt == nil {
			return false
		}
		if b.LastAttempt == nil {
			return true
		}
		return a.LastAttempt.ObservedAt.After(b.LastAttempt.ObservedAt)
	})
	retained := s.Drives[:0]
	for _, d := range s.Drives {
		if d.ObservationStatus == "disconnected" && (len(retained) >= 512 || (d.LastAttempt != nil && now.Sub(d.LastAttempt.ObservedAt) > 90*24*time.Hour)) {
			continue
		}
		retained = append(retained, d)
	}
	s.Drives = retained
}

func driveAvailabilityKey(path string) string {
	digest := sha256.Sum256([]byte(path))
	return fmt.Sprintf("availability-%x", digest[:12])
}
