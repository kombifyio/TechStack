package jobs

import (
	"fmt"
	"reflect"
	"strings"
	"time"
)

// JobSnapshot is an immutable, point-in-time view of the persistable job
// state. Payload and Result are recursively copied so persistence and
// projection code never observes maps that a running handler is mutating.
type JobSnapshot struct {
	ID                    string
	Type                  JobType
	TargetType            string
	TargetID              string
	TargetName            string
	State                 JobState
	Priority              int
	Payload               map[string]interface{}
	Result                map[string]interface{}
	Error                 string
	ErrorDetails          string
	Step                  string
	Message               string
	Progress              int
	Logs                  []LogEntry
	Attempts              int
	MaxAttempts           int
	CreatedAt             time.Time
	StartedAt             *time.Time
	CompletedAt           *time.Time
	WaitReason            string
	NextResumeAt          *time.Time
	PersistenceSuppressed bool
}

// Snapshot returns a read-only point-in-time copy of all job fields used by
// persistence and API projections.
func (j *Job) Snapshot() JobSnapshot {
	if j == nil {
		return JobSnapshot{}
	}

	j.mu.RLock()
	defer j.mu.RUnlock()

	return JobSnapshot{
		ID:                    j.ID,
		Type:                  j.Type,
		TargetType:            j.TargetType,
		TargetID:              j.TargetID,
		TargetName:            j.TargetName,
		State:                 j.State,
		Priority:              j.Priority,
		Payload:               cloneJobMap(j.Payload),
		Result:                cloneJobMap(j.Result),
		Error:                 j.Error,
		ErrorDetails:          j.ErrorDetails,
		Step:                  j.Step,
		Message:               j.Message,
		Progress:              j.Progress,
		Logs:                  append([]LogEntry(nil), j.Logs...),
		Attempts:              j.Attempts,
		MaxAttempts:           j.MaxAttempts,
		CreatedAt:             j.CreatedAt,
		StartedAt:             cloneTimePointer(j.StartedAt),
		CompletedAt:           cloneTimePointer(j.CompletedAt),
		WaitReason:            j.WaitReason,
		NextResumeAt:          cloneTimePointer(j.NextResumeAt),
		PersistenceSuppressed: j.suppressPersistence,
	}
}

// DetachedCopy preserves the existing pointer-shaped read API while ensuring
// callers cannot race with, or mutate, the queue's live Job instance.
func (j *Job) DetachedCopy() *Job {
	if j == nil {
		return nil
	}
	snapshot := j.Snapshot()
	return &Job{
		ID:                  snapshot.ID,
		Type:                snapshot.Type,
		TargetType:          snapshot.TargetType,
		TargetID:            snapshot.TargetID,
		TargetName:          snapshot.TargetName,
		State:               snapshot.State,
		Priority:            snapshot.Priority,
		Payload:             snapshot.Payload,
		Result:              snapshot.Result,
		Error:               snapshot.Error,
		ErrorDetails:        snapshot.ErrorDetails,
		Step:                snapshot.Step,
		Message:             snapshot.Message,
		Progress:            snapshot.Progress,
		Logs:                snapshot.Logs,
		Attempts:            snapshot.Attempts,
		MaxAttempts:         snapshot.MaxAttempts,
		CreatedAt:           snapshot.CreatedAt,
		StartedAt:           snapshot.StartedAt,
		CompletedAt:         snapshot.CompletedAt,
		WaitReason:          snapshot.WaitReason,
		NextResumeAt:        snapshot.NextResumeAt,
		suppressPersistence: snapshot.PersistenceSuppressed,
	}
}

func (j *Job) setStep(step string) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.Step = step
	j.mu.Unlock()
}

func (j *Job) replaceResult(result map[string]interface{}) {
	if j == nil {
		return
	}
	j.mu.Lock()
	j.Result = cloneJobMap(result)
	j.mu.Unlock()
}

func (j *Job) mutateResult(fn func(map[string]interface{})) {
	if j == nil || fn == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.Result == nil {
		j.Result = map[string]interface{}{}
	}
	fn(j.Result)
	// Detach any maps or slices supplied by the caller. Handlers commonly keep
	// building runtime proof structures after publishing an intermediate result.
	j.Result = cloneJobMap(j.Result)
}

func (j *Job) prepareDeployPayload() {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Type = JobTypeDeploy
	if j.Payload == nil {
		j.Payload = map[string]interface{}{}
	}
	if _, ok := j.Payload["apply"]; !ok {
		j.Payload["apply"] = true
	}
	j.Payload = cloneJobMap(j.Payload)
}

// BindTenantID makes the durable execution identity explicit before a job is
// admitted to a queue. Existing payload/result bindings are treated as
// immutable so an enqueue path cannot silently move work across tenants.
func (j *Job) BindTenantID(tenantID string) error {
	if j == nil {
		return fmt.Errorf("job is required")
	}
	tenantID = strings.TrimSpace(tenantID)
	if tenantID == "" {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	existing := firstJobString(j.Payload, j.Result, "tenant_id")
	if existing != "" && existing != tenantID {
		return fmt.Errorf("job tenant %q does not match enqueue tenant %q", existing, tenantID)
	}
	if j.Payload == nil {
		j.Payload = map[string]interface{}{}
	}
	j.Payload["tenant_id"] = tenantID
	j.Payload = cloneJobMap(j.Payload)
	return nil
}

func cloneTimePointer(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	cloned := *value
	return &cloned
}

func cloneJobMap(source map[string]interface{}) map[string]interface{} {
	if source == nil {
		return nil
	}
	cloned := make(map[string]interface{}, len(source))
	for key, value := range source {
		cloned[key] = cloneJobValue(value)
	}
	return cloned
}

// cloneJobValue recursively copies JSON-like maps and collections while
// retaining their concrete Go types (for example json.RawMessage and
// []map[string]interface{}). Struct values such as time.Time are values and
// can be copied directly.
func cloneJobValue(value interface{}) interface{} {
	if value == nil {
		return nil
	}
	return cloneJobReflectValue(reflect.ValueOf(value)).Interface()
}

func cloneJobReflectValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}

	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := cloneJobReflectValue(value.Elem())
		out := reflect.New(value.Type()).Elem()
		out.Set(cloned)
		return out
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeMapWithSize(value.Type(), value.Len())
		iterator := value.MapRange()
		for iterator.Next() {
			out.SetMapIndex(iterator.Key(), cloneJobReflectValue(iterator.Value()))
		}
		return out
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			out.Index(index).Set(cloneJobReflectValue(value.Index(index)))
		}
		return out
	case reflect.Array:
		out := reflect.New(value.Type()).Elem()
		for index := 0; index < value.Len(); index++ {
			out.Index(index).Set(cloneJobReflectValue(value.Index(index)))
		}
		return out
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		out := reflect.New(value.Type().Elem())
		out.Elem().Set(cloneJobReflectValue(value.Elem()))
		return out
	default:
		return value
	}
}
