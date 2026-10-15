package agent

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

const (
	// stackKitApplySilenceLimit is how long a runtime owner in flight may stay
	// silent before the node ends the apply as stuck. The pinned CLI reports a
	// running owner every 15 seconds between its started and its terminal event,
	// so this is twelve missed reports. Outside an owner in flight (before the
	// first owner, between owners, after the last one) the apply may be silent
	// legitimately, and only the command's wall-clock deadline governs it.
	stackKitApplySilenceLimit = 3 * time.Minute

	stackKitProgressPollInterval = 250 * time.Millisecond
	stackKitOwnerPhase           = "apply.owner"
	// maxStackKitEventTailChunk bounds one poll of the event spool.
	maxStackKitEventTailChunk = 4 << 20
	// maxStackKitOwnerCount bounds an owner's reported total and the number of
	// owners the watch tracks; maxStackKitOwnerIdentity bounds one identity.
	maxStackKitOwnerCount    = 10_000
	maxStackKitOwnerIdentity = 512
)

var (
	stackKitEventNumberPattern = regexp.MustCompile(`^[0-9]{1,9}$`)
	stackKitEventCodePattern   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
)

// stackKitEventFieldRules lists the rollout-event attributes the node forwards
// into a live log record, and how each value is reduced. Anything not listed is
// dropped: an event's attributes can in principle carry free-form text, and
// only closed identities, positions and timings may leave the node.
var stackKitEventFieldRules = map[string]func(string) (string, bool){
	"scope":        func(v string) (string, bool) { return v, v == "channel" || v == "owner" },
	"owner":        safeStackKitEventLabel,
	"module":       safeStackKitEventLabel,
	"workload":     safeStackKitEventLabel,
	"position":     matchesStackKitEventValue(stackKitEventNumberPattern),
	"total":        matchesStackKitEventValue(stackKitEventNumberPattern),
	"targets":      matchesStackKitEventValue(stackKitEventNumberPattern),
	"elapsed_ms":   matchesStackKitEventValue(stackKitEventNumberPattern),
	"replayed":     func(v string) (string, bool) { return v, v == "true" },
	"degraded":     func(v string) (string, bool) { return v, v == "true" },
	"failure_code": matchesStackKitEventValue(stackKitEventCodePattern),
}

func safeStackKitEventLabel(value string) (string, bool) {
	if strings.TrimSpace(value) == "" {
		return "", false
	}
	return stackkitcommand.SafeEventLabel(value), true
}

func matchesStackKitEventValue(pattern *regexp.Regexp) func(string) (string, bool) {
	return func(value string) (string, bool) { return value, pattern.MatchString(value) }
}

func (executor *StackKitExecutor) applySilenceLimit() time.Duration {
	if executor.silenceLimit > 0 {
		return executor.silenceLimit
	}
	return stackKitApplySilenceLimit
}

// forwardedStackKitEventFields returns the bounded facts of one rollout event
// for its live log record: the allowed attributes and the closed failure class.
func forwardedStackKitEventFields(event map[string]any) map[string]string {
	fields := map[string]string{}
	if attributes, ok := event["attributes"].(map[string]any); ok {
		for key, rule := range stackKitEventFieldRules {
			raw, _ := attributes[key].(string)
			if value, keep := rule(raw); keep {
				fields[key] = value
			}
		}
	}
	if class, _ := event["failureClass"].(string); stackKitEventCodePattern.MatchString(class) {
		fields["failure_class"] = class
	}
	return fields
}

// stackKitSilenceWatch decides when a runtime owner of a StackKits apply has
// gone quiet. It tracks the owners in flight by identity and holds no clock:
// every call passes the time it concerns, so the decision can run on explicit
// times.
type stackKitSilenceWatch struct {
	limit time.Duration
	// lastEvent is the arrival of the latest well-formed rollout event.
	lastEvent time.Time
	owners    map[string]*stackKitOwner
	fired     bool
}

// stackKitOwner is what the watch knows of one runtime owner. StackKits
// guarantees a heartbeat only while the owner is in flight: from its started
// event until its terminal one.
type stackKitOwner struct {
	label     string
	position  int
	total     int
	lastEvent time.Time
	inFlight  bool
	finished  bool
}

func newStackKitSilenceWatch(limit time.Duration) *stackKitSilenceWatch {
	return &stackKitSilenceWatch{limit: limit, owners: map[string]*stackKitOwner{}}
}

// observe records one line of the event spool seen at now. Only a well-formed
// rollout event is progress, by the same checks the spool validation applies:
// an object with a time, a phase and a known status.
func (w *stackKitSilenceWatch) observe(now time.Time, raw []byte) {
	var event struct {
		Time       time.Time       `json:"time"`
		Phase      string          `json:"phase"`
		Status     string          `json:"status"`
		Attributes json.RawMessage `json:"attributes"`
	}
	if json.Unmarshal(bytes.TrimSpace(raw), &event) != nil || event.Time.IsZero() ||
		strings.TrimSpace(event.Phase) == "" || !stackkitcommand.ValidRolloutStatus(event.Status) {
		return
	}
	w.lastEvent = now
	if event.Phase != stackKitOwnerPhase {
		return
	}
	var attributes map[string]any
	_ = json.Unmarshal(event.Attributes, &attributes)
	attribute := func(key string) string {
		value, _ := attributes[key].(string)
		return value
	}
	identity := attribute("owner")
	if len(identity) > maxStackKitOwnerIdentity {
		identity = identity[:maxStackKitOwnerIdentity]
	}
	owner := w.owners[identity]
	switch event.Status {
	case "started", "running":
		if owner != nil && owner.finished {
			// A late heartbeat of an owner that already ended does not re-arm.
			return
		}
		if owner == nil {
			if owner = w.track(identity); owner == nil {
				return
			}
		}
		owner.inFlight, owner.lastEvent = true, now
		owner.setPlace(attribute("position"), attribute("total"))
	case "succeeded", "failed":
		// A replayed owner reports succeeded without ever starting.
		if owner == nil {
			if owner = w.track(identity); owner == nil {
				return
			}
		}
		owner.inFlight, owner.finished = false, true
	}
}

func (w *stackKitSilenceWatch) track(identity string) *stackKitOwner {
	if len(w.owners) >= maxStackKitOwnerCount {
		return nil
	}
	owner := &stackKitOwner{label: stackkitcommand.SafeEventLabel(identity)}
	w.owners[identity] = owner
	return owner
}

// setPlace adopts the owner's position only when it is well formed: positive
// integers within the owner bound, position not above total. An event that
// fails keeps what the owner reported before.
func (o *stackKitOwner) setPlace(position, total string) {
	if !stackKitEventNumberPattern.MatchString(position) || !stackKitEventNumberPattern.MatchString(total) {
		return
	}
	p, _ := strconv.Atoi(position)
	t, _ := strconv.Atoi(total)
	if p < 1 || t < 1 || t > maxStackKitOwnerCount || p > t {
		return
	}
	o.position, o.total = p, t
}

// stalled reports, once, that the apply has been silent for the limit while at
// least one owner is in flight, and names the owner that has been silent the
// longest. With no owner in flight (before the first owner, between owners,
// after the last) only the command's wall clock applies.
func (w *stackKitSilenceWatch) stalled(now time.Time) (stackkitcommand.ApplyStall, bool) {
	if w.fired || now.Sub(w.lastEvent) < w.limit {
		return stackkitcommand.ApplyStall{}, false
	}
	var longest *stackKitOwner
	for _, owner := range w.owners {
		if owner.inFlight && owner.silentLongerThan(longest) {
			longest = owner
		}
	}
	if longest == nil {
		return stackkitcommand.ApplyStall{}, false
	}
	w.fired = true
	return stackkitcommand.ApplyStall{
		Owner: longest.label, Position: longest.position, Total: longest.total, Idle: now.Sub(longest.lastEvent),
	}, true
}

// silentLongerThan orders in-flight owners by their last event, then by place
// and label, so the owner a stall names does not depend on map order.
func (o *stackKitOwner) silentLongerThan(other *stackKitOwner) bool {
	switch {
	case other == nil:
		return true
	case !o.lastEvent.Equal(other.lastEvent):
		return o.lastEvent.Before(other.lastEvent)
	case o.position != other.position:
		return o.position < other.position
	default:
		return o.label < other.label
	}
}

// stackKitApplyStalledError is the failure of a command the node ended because
// its apply stopped reporting progress.
type stackKitApplyStalledError struct{ stall stackkitcommand.ApplyStall }

func (e *stackKitApplyStalledError) Error() string { return e.stall.Message() }

// stackKitEventTail reads the lines appended to the event spool since its last
// poll. It is independent of the live-log sink on purpose: a slow or blocked
// log delivery must never be mistaken for a silent apply.
type stackKitEventTail struct {
	path    string
	offset  int64
	partial []byte
	// spool is the file as the previous poll saw it.
	spool os.FileInfo
}

// newLines returns the complete lines appended since the previous poll. A spool
// that shrank, or that is no longer the same file, is read again from its start:
// the offset and partial line belong to the file that was there before.
func (t *stackKitEventTail) newLines() [][]byte {
	file, err := os.Open(filepath.Clean(t.path))
	if err != nil {
		return nil
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil
	}
	if info.Size() < t.offset || (t.spool != nil && !os.SameFile(t.spool, info)) {
		t.offset, t.partial = 0, nil
	}
	t.spool = info
	if _, err := file.Seek(t.offset, io.SeekStart); err != nil {
		return nil
	}
	chunk, err := io.ReadAll(io.LimitReader(file, maxStackKitEventTailChunk))
	if err != nil || len(chunk) == 0 {
		return nil
	}
	t.offset += int64(len(chunk))
	t.partial = append(t.partial, chunk...)
	var lines [][]byte
	for {
		end := bytes.IndexByte(t.partial, '\n')
		if end < 0 {
			break
		}
		lines = append(lines, t.partial[:end])
		t.partial = t.partial[end+1:]
	}
	if len(t.partial) > maxStackKitCommandEventLine {
		t.partial = nil
	}
	return lines
}

// watchStackKitSilence feeds the event spool to the watch until stop closes and
// calls onStall once if the apply goes quiet.
func watchStackKitSilence(path string, watch *stackKitSilenceWatch, ticks <-chan time.Time, stop <-chan struct{}, onStall func(stackkitcommand.ApplyStall)) {
	tail := stackKitEventTail{path: path}
	for {
		select {
		case <-stop:
			return
		case now := <-ticks:
			for _, line := range tail.newLines() {
				watch.observe(now, line)
			}
			if stall, silent := watch.stalled(now); silent {
				onStall(stall)
			}
		}
	}
}
