// Package jobs provides unit tests for drift handler helper functions.
package jobs

import (
	"testing"
)

// TestParsePlanOutput tests parsing of terraform plan output.
func TestParsePlanOutput(t *testing.T) {
	t.Run("empty output", func(t *testing.T) {
		output := map[string]interface{}{}
		changes := parsePlanOutput(output)
		if len(changes) != 0 {
			t.Errorf("expected 0 changes, got %d", len(changes))
		}
	})

	t.Run("no resource_changes key", func(t *testing.T) {
		output := map[string]interface{}{
			"other_key": "value",
		}
		changes := parsePlanOutput(output)
		if len(changes) != 0 {
			t.Errorf("expected 0 changes, got %d", len(changes))
		}
	})

	t.Run("invalid resource_changes type", func(t *testing.T) {
		output := map[string]interface{}{
			"resource_changes": "not an array",
		}
		changes := parsePlanOutput(output)
		if len(changes) != 0 {
			t.Errorf("expected 0 changes, got %d", len(changes))
		}
	})

	t.Run("valid resource changes", func(t *testing.T) {
		output := map[string]interface{}{
			"resource_changes": []interface{}{
				map[string]interface{}{
					"address": "aws_instance.web[0]",
					"name":    "web",
					"change": map[string]interface{}{
						"actions": []interface{}{"update"},
						"before": map[string]interface{}{
							"instance_type": "t2.micro",
						},
						"after": map[string]interface{}{
							"instance_type": "t2.small",
						},
					},
				},
			},
		}
		changes := parsePlanOutput(output)
		if len(changes) != 1 {
			t.Errorf("expected 1 change, got %d", len(changes))
		}
		if changes[0].Action != "update" {
			t.Errorf("expected action 'update', got %q", changes[0].Action)
		}
		if changes[0].Address != "aws_instance.web[0]" {
			t.Errorf("expected address 'aws_instance.web[0]', got %q", changes[0].Address)
		}
	})

	t.Run("filters out no-op changes", func(t *testing.T) {
		output := map[string]interface{}{
			"resource_changes": []interface{}{
				map[string]interface{}{
					"address": "aws_instance.static",
					"change": map[string]interface{}{
						"actions": []interface{}{"no-op"},
					},
				},
			},
		}
		changes := parsePlanOutput(output)
		if len(changes) != 0 {
			t.Errorf("expected no-op to be filtered out, got %d changes", len(changes))
		}
	})

	t.Run("handles missing actions", func(t *testing.T) {
		output := map[string]interface{}{
			"resource_changes": []interface{}{
				map[string]interface{}{
					"address": "aws_instance.test",
					"change":  map[string]interface{}{},
				},
			},
		}
		changes := parsePlanOutput(output)
		// Should be filtered out because no action
		if len(changes) != 0 {
			t.Errorf("expected 0 changes (empty action filtered), got %d", len(changes))
		}
	})
}

// TestCalculatePlanSummary tests plan summary calculation.
func TestCalculatePlanSummary(t *testing.T) {
	changes := []ResourceChange{
		{Action: "create"},
		{Action: "create"},
		{Action: "update"},
		{Action: "update"},
		{Action: "update"},
		{Action: "delete"},
		{Action: "no-op"},
		{Action: "no-op"},
	}

	summary := calculatePlanSummary(changes)

	if summary.ToCreate != 2 {
		t.Errorf("expected ToCreate 2, got %d", summary.ToCreate)
	}
	if summary.ToUpdate != 3 {
		t.Errorf("expected ToUpdate 3, got %d", summary.ToUpdate)
	}
	if summary.ToDelete != 1 {
		t.Errorf("expected ToDelete 1, got %d", summary.ToDelete)
	}
	if summary.Unchanged != 2 {
		t.Errorf("expected Unchanged 2, got %d", summary.Unchanged)
	}
}

// TestCalculatePlanSummary_Empty tests empty changes slice.
func TestCalculatePlanSummary_Empty(t *testing.T) {
	summary := calculatePlanSummary([]ResourceChange{})

	if summary.ToCreate != 0 || summary.ToUpdate != 0 || summary.ToDelete != 0 || summary.Unchanged != 0 {
		t.Error("expected all counts to be 0 for empty changes")
	}
}

// TestCalculateFieldChanges tests field-level change detection.
func TestCalculateFieldChanges(t *testing.T) {
	t.Run("nil inputs", func(t *testing.T) {
		changes := calculateFieldChanges(nil, nil)
		if changes != nil {
			t.Error("expected nil for nil inputs")
		}
	})

	t.Run("nil before", func(t *testing.T) {
		changes := calculateFieldChanges(nil, map[string]interface{}{"key": "value"})
		if changes != nil {
			t.Error("expected nil when before is nil")
		}
	})

	t.Run("nil after", func(t *testing.T) {
		changes := calculateFieldChanges(map[string]interface{}{"key": "value"}, nil)
		if changes != nil {
			t.Error("expected nil when after is nil")
		}
	})

	t.Run("field added", func(t *testing.T) {
		before := map[string]interface{}{"existing": "value"}
		after := map[string]interface{}{"existing": "value", "new": "added"}
		changes := calculateFieldChanges(before, after)

		if changes == nil {
			t.Fatal("expected changes map")
		}
		if _, ok := changes["new"]; !ok {
			t.Error("expected 'new' field to be in changes")
		}
		if changes["new"].From != nil {
			t.Errorf("expected From to be nil, got %v", changes["new"].From)
		}
		if changes["new"].To != "added" {
			t.Errorf("expected To 'added', got %v", changes["new"].To)
		}
	})

	t.Run("field removed", func(t *testing.T) {
		before := map[string]interface{}{"existing": "value", "removed": "old"}
		after := map[string]interface{}{"existing": "value"}
		changes := calculateFieldChanges(before, after)

		if changes == nil {
			t.Fatal("expected changes map")
		}
		if _, ok := changes["removed"]; !ok {
			t.Error("expected 'removed' field to be in changes")
		}
		if changes["removed"].From != "old" {
			t.Errorf("expected From 'old', got %v", changes["removed"].From)
		}
		if changes["removed"].To != nil {
			t.Errorf("expected To to be nil, got %v", changes["removed"].To)
		}
	})

	t.Run("field modified", func(t *testing.T) {
		before := map[string]interface{}{"size": 10}
		after := map[string]interface{}{"size": 20}
		changes := calculateFieldChanges(before, after)

		if changes == nil {
			t.Fatal("expected changes map")
		}
		if _, ok := changes["size"]; !ok {
			t.Error("expected 'size' field to be in changes")
		}
	})

	t.Run("no changes", func(t *testing.T) {
		before := map[string]interface{}{"key": "value"}
		after := map[string]interface{}{"key": "value"}
		changes := calculateFieldChanges(before, after)

		if changes != nil {
			t.Errorf("expected nil for no changes, got %v", changes)
		}
	})
}

// TestSplitResourceAddress tests resource address parsing.
func TestSplitResourceAddress(t *testing.T) {
	tests := []struct {
		name    string
		address string
		want    []string
	}{
		{
			name:    "simple address",
			address: "aws_instance.web",
			want:    []string{"aws_instance", "web"},
		},
		{
			name:    "address with module",
			address: "module.vpc.aws_subnet.private",
			want:    []string{"module", "vpc", "aws_subnet", "private"},
		},
		{
			name:    "address with index",
			address: "aws_instance.web[0]",
			want:    []string{"aws_instance", "web[0]"},
		},
		{
			name:    "address with count index",
			address: "module.web[0].aws_instance.server",
			want:    []string{"module", "web[0]", "aws_instance", "server"},
		},
		{
			name:    "empty address",
			address: "",
			want:    []string{},
		},
		{
			name:    "single component",
			address: "aws_instance",
			want:    []string{"aws_instance"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := splitResourceAddress(tt.address)
			if len(got) != len(tt.want) {
				t.Errorf("splitResourceAddress(%q) got %d parts, want %d", tt.address, len(got), len(tt.want))
				return
			}
			for i, part := range got {
				if part != tt.want[i] {
					t.Errorf("splitResourceAddress(%q)[%d] = %q, want %q", tt.address, i, part, tt.want[i])
				}
			}
		})
	}
}

// TestGetStringValue tests string value extraction from maps.
func TestGetStringValue(t *testing.T) {
	m := map[string]interface{}{
		"string_key": "string_value",
		"int_key":    123,
		"bool_key":   true,
	}

	if got := getStringValue(m, "string_key"); got != "string_value" {
		t.Errorf("expected 'string_value', got %q", got)
	}

	if got := getStringValue(m, "int_key"); got != "" {
		t.Errorf("expected empty string for non-string, got %q", got)
	}

	if got := getStringValue(m, "missing_key"); got != "" {
		t.Errorf("expected empty string for missing key, got %q", got)
	}
}

// TestGetStringFromInterface tests interface to string conversion.
func TestGetStringFromInterface(t *testing.T) {
	if got := getStringFromInterface("hello"); got != "hello" {
		t.Errorf("expected 'hello', got %q", got)
	}

	if got := getStringFromInterface(123); got != "" {
		t.Errorf("expected empty string for int, got %q", got)
	}

	if got := getStringFromInterface(nil); got != "" {
		t.Errorf("expected empty string for nil, got %q", got)
	}
}

// TestJsonEqual tests JSON equality comparison.
func TestJsonEqual(t *testing.T) {
	tests := []struct {
		name string
		a    interface{}
		b    interface{}
		want bool
	}{
		{"equal strings", "hello", "hello", true},
		{"different strings", "hello", "world", false},
		{"equal numbers", 123, 123, true},
		{"equal maps", map[string]int{"a": 1}, map[string]int{"a": 1}, true},
		{"different maps", map[string]int{"a": 1}, map[string]int{"a": 2}, false},
		{"equal arrays", []int{1, 2, 3}, []int{1, 2, 3}, true},
		{"different arrays", []int{1, 2, 3}, []int{3, 2, 1}, false},
		{"nil values", nil, nil, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := jsonEqual(tt.a, tt.b); got != tt.want {
				t.Errorf("jsonEqual(%v, %v) = %v, want %v", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

// TestWrapDriftError tests drift error wrapping.
func TestWrapDriftError(t *testing.T) {
	err := wrapDriftError("test_step", "test message", "test details")

	jobErr, ok := err.(*JobError)
	if !ok {
		t.Fatal("expected *JobError")
	}

	if jobErr.Category != ErrorCategoryTransient {
		t.Errorf("expected transient category, got %v", jobErr.Category)
	}

	if !jobErr.Retryable {
		t.Error("expected drift error to be retryable")
	}

	if jobErr.Context["step"] != "test_step" {
		t.Errorf("expected step 'test_step', got %v", jobErr.Context["step"])
	}

	if jobErr.Context["message"] != "test message" {
		t.Errorf("expected message 'test message', got %v", jobErr.Context["message"])
	}

	if jobErr.Context["details"] != "test details" {
		t.Errorf("expected details 'test details', got %v", jobErr.Context["details"])
	}
}
