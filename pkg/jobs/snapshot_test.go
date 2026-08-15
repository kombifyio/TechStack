package jobs

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestJobSnapshotDeepClonesMutableState(t *testing.T) {
	now := time.Now().UTC()
	original := now
	job := &Job{
		ID:           "job-1",
		Type:         JobTypeDeploy,
		TargetID:     "stack-1",
		State:        JobStateRunning,
		Payload:      map[string]interface{}{"nested": map[string]interface{}{"workers": []interface{}{map[string]interface{}{"host": "old"}}}},
		Result:       map[string]interface{}{"runtime": map[string]interface{}{"lifecycle": []map[string]interface{}{{"state": "ready"}}}, "raw": json.RawMessage(`{"ok":true}`)},
		Logs:         []LogEntry{{Timestamp: now, Level: "info", Message: "started"}},
		StartedAt:    &now,
		NextResumeAt: &now,
	}

	snapshot := job.Snapshot()
	job.Payload["nested"].(map[string]interface{})["workers"].([]interface{})[0].(map[string]interface{})["host"] = "new"
	job.Result["runtime"].(map[string]interface{})["lifecycle"].([]map[string]interface{})[0]["state"] = "failed"
	job.Result["raw"].(json.RawMessage)[2] = 'x'
	job.Logs[0].Message = "changed"
	changed := now.Add(time.Hour)
	*job.StartedAt = changed
	*job.NextResumeAt = changed

	workers := snapshot.Payload["nested"].(map[string]interface{})["workers"].([]interface{})
	if got := workers[0].(map[string]interface{})["host"]; got != "old" {
		t.Fatalf("snapshot payload alias: got %v", got)
	}
	lifecycle := snapshot.Result["runtime"].(map[string]interface{})["lifecycle"].([]map[string]interface{})
	if got := lifecycle[0]["state"]; got != "ready" {
		t.Fatalf("snapshot result alias: got %v", got)
	}
	if got := string(snapshot.Result["raw"].(json.RawMessage)); got != `{"ok":true}` {
		t.Fatalf("snapshot raw message alias: got %s", got)
	}
	if snapshot.Logs[0].Message != "started" {
		t.Fatalf("snapshot logs alias: got %q", snapshot.Logs[0].Message)
	}
	if !snapshot.StartedAt.Equal(original) || !snapshot.NextResumeAt.Equal(original) {
		t.Fatal("snapshot time pointer alias")
	}
}

func TestReplaceResultDoesNotRetainCallerAliases(t *testing.T) {
	job := &Job{}
	nested := map[string]interface{}{"state": "ready"}
	result := map[string]interface{}{"runtime": nested}

	job.replaceResult(result)
	nested["state"] = "failed"

	if got := job.Snapshot().Result["runtime"].(map[string]interface{})["state"]; got != "ready" {
		t.Fatalf("replaceResult retained caller alias: got %v", got)
	}
}

func TestJobSnapshotConcurrentNestedResultMutation(t *testing.T) {
	job := &Job{Result: map[string]interface{}{"runtime": map[string]interface{}{"sequence": 0}}}
	var workers sync.WaitGroup
	workers.Add(2)

	go func() {
		defer workers.Done()
		for sequence := 1; sequence <= 500; sequence++ {
			job.mutateResult(func(result map[string]interface{}) {
				result["runtime"] = map[string]interface{}{
					"sequence": sequence,
					"events":   []interface{}{map[string]interface{}{"message": fmt.Sprintf("event-%d", sequence)}},
				}
			})
		}
	}()

	go func() {
		defer workers.Done()
		for iteration := 0; iteration < 500; iteration++ {
			snapshot := job.Snapshot()
			runtime, ok := snapshot.Result["runtime"].(map[string]interface{})
			if !ok {
				t.Errorf("runtime snapshot has type %T", snapshot.Result["runtime"])
				return
			}
			if events, exists := runtime["events"]; exists {
				if _, ok := events.([]interface{}); !ok {
					t.Errorf("events snapshot has type %T", events)
					return
				}
			}
		}
	}()

	workers.Wait()
}

func TestJobDetachedCopyDoesNotExposeLiveState(t *testing.T) {
	job := &Job{Payload: map[string]interface{}{"nested": map[string]interface{}{"value": "live"}}}
	detached := job.DetachedCopy()
	detached.Payload["nested"].(map[string]interface{})["value"] = "caller"

	if got := job.Snapshot().Payload["nested"].(map[string]interface{})["value"]; got != "live" {
		t.Fatalf("detached copy mutated live job: got %v", got)
	}
}
