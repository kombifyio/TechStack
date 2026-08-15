package unifier

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/core"
	"github.com/kombifyio/techstack/pkg/tofu"
)

// MockRunner implements tofu.ExtendedRunner for testing
type MockRunner struct {
	InitCalled     bool
	ApplyCalled    bool
	DestroyCalled  bool
	StateCalled    bool
	OutputCalled   bool
	ValidateCalled bool

	InitError     error
	ApplyError    error
	DestroyError  error
	StateError    error
	OutputError   error
	ValidateError error

	ApplyResult    *tofu.ApplyResult
	StateSummary   *tofu.StateSummary
	OutputValues   map[string]tofu.OutputValue
	ValidateResult *tofu.ValidateResult

	LastDir string
}

func (m *MockRunner) Init(dir string) error {
	m.InitCalled = true
	m.LastDir = dir
	return m.InitError
}

func (m *MockRunner) Plan(dir string) error {
	m.LastDir = dir
	return nil
}

func (m *MockRunner) Apply(dir string) error {
	m.ApplyCalled = true
	m.LastDir = dir
	return m.ApplyError
}

func (m *MockRunner) Destroy(dir string) error {
	m.DestroyCalled = true
	m.LastDir = dir
	return m.DestroyError
}

func (m *MockRunner) InitWithContext(ctx context.Context, dir string) error {
	return m.Init(dir)
}

func (m *MockRunner) ApplyWithContext(ctx context.Context, dir string) (*tofu.ApplyResult, error) {
	m.ApplyCalled = true
	m.LastDir = dir
	if m.ApplyError != nil {
		return nil, m.ApplyError
	}
	if m.ApplyResult != nil {
		return m.ApplyResult, nil
	}
	return &tofu.ApplyResult{
		Success:  true,
		Duration: 100 * time.Millisecond,
		Resources: tofu.ResourceChanges{
			Added: 1,
		},
	}, nil
}

func (m *MockRunner) DestroyWithContext(ctx context.Context, dir string) error {
	return m.Destroy(dir)
}

func (m *MockRunner) Output(dir string) (map[string]tofu.OutputValue, error) {
	m.OutputCalled = true
	m.LastDir = dir
	if m.OutputError != nil {
		return nil, m.OutputError
	}
	if m.OutputValues != nil {
		return m.OutputValues, nil
	}
	return map[string]tofu.OutputValue{}, nil
}

func (m *MockRunner) State(dir string) (*tofu.StateSummary, error) {
	m.StateCalled = true
	m.LastDir = dir
	if m.StateError != nil {
		return nil, m.StateError
	}
	if m.StateSummary != nil {
		return m.StateSummary, nil
	}
	return &tofu.StateSummary{
		Resources:    1,
		LastModified: time.Now(),
	}, nil
}

func (m *MockRunner) Validate(dir string) (*tofu.ValidateResult, error) {
	m.ValidateCalled = true
	m.LastDir = dir
	if m.ValidateError != nil {
		return nil, m.ValidateError
	}
	if m.ValidateResult != nil {
		return m.ValidateResult, nil
	}
	return &tofu.ValidateResult{
		Valid:        true,
		ErrorCount:   0,
		WarningCount: 0,
		Diagnostics:  []string{},
	}, nil
}

func TestEngine_Provision_NilSpec(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	err = engine.Provision(nil)
	if err == nil {
		t.Error("Expected error for nil spec")
	}
}

func TestEngine_Provision_EmptyName(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	spec := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "",
		},
	}

	err = engine.Provision(spec)
	if err == nil {
		t.Error("Expected error for empty name")
	}
}

func TestEngine_Provision_WithMockRunner(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	// Create temp directory for stack data
	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	// Set up mock runner
	mockRunner := &MockRunner{}
	engine.SetRunner(mockRunner)

	spec := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "test-stack",
		},
	}

	err = engine.Provision(spec)
	if err != nil {
		t.Errorf("Provision failed: %v", err)
	}

	if !mockRunner.InitCalled {
		t.Error("Expected Init to be called")
	}
	if !mockRunner.ApplyCalled {
		t.Error("Expected Apply to be called")
	}

	// Verify stack directory was created
	stackDir := filepath.Join(tmpDir, "test-stack")
	if _, err := os.Stat(stackDir); os.IsNotExist(err) {
		t.Error("Expected stack directory to be created")
	}
}

func TestEngine_Provision_InitError(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	mockRunner := &MockRunner{
		InitError: errors.New("init failed"),
	}
	engine.SetRunner(mockRunner)

	spec := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "test-stack",
		},
	}

	err = engine.Provision(spec)
	if err == nil {
		t.Error("Expected error when Init fails")
	}
	if !mockRunner.InitCalled {
		t.Error("Expected Init to be called")
	}
	if mockRunner.ApplyCalled {
		t.Error("Apply should not be called when Init fails")
	}
}

func TestEngine_Provision_ApplyError(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	mockRunner := &MockRunner{
		ApplyError: errors.New("apply failed"),
	}
	engine.SetRunner(mockRunner)

	spec := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "test-stack",
		},
	}

	err = engine.Provision(spec)
	if err == nil {
		t.Error("Expected error when Apply fails")
	}
	if !mockRunner.ApplyCalled {
		t.Error("Expected Apply to be called")
	}
}

func TestEngine_Destroy_EmptyStackID(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	err = engine.Destroy("")
	if err == nil {
		t.Error("Expected error for empty stack ID")
	}
}

func TestEngine_Destroy_NonexistentStack(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	err = engine.Destroy("nonexistent-stack")
	if err == nil {
		t.Error("Expected error for nonexistent stack")
	}
}

func TestEngine_Destroy_WithMockRunner(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	// Create stack directory
	stackDir := filepath.Join(tmpDir, "test-stack")
	if err := os.MkdirAll(stackDir, 0755); err != nil {
		t.Fatalf("Failed to create stack directory: %v", err)
	}

	mockRunner := &MockRunner{}
	engine.SetRunner(mockRunner)

	err = engine.Destroy("test-stack")
	if err != nil {
		t.Errorf("Destroy failed: %v", err)
	}

	if !mockRunner.DestroyCalled {
		t.Error("Expected Destroy to be called")
	}
}

func TestEngine_Destroy_RunnerError(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	// Create stack directory
	stackDir := filepath.Join(tmpDir, "test-stack")
	if err := os.MkdirAll(stackDir, 0755); err != nil {
		t.Fatalf("Failed to create stack directory: %v", err)
	}

	mockRunner := &MockRunner{
		DestroyError: errors.New("destroy failed"),
	}
	engine.SetRunner(mockRunner)

	err = engine.Destroy("test-stack")
	if err == nil {
		t.Error("Expected error when Destroy fails")
	}
}

func TestEngine_Status_EmptyStackID(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	_, err = engine.Status("")
	if err == nil {
		t.Error("Expected error for empty stack ID")
	}
}

func TestEngine_Status_NonexistentStack(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	status, err := engine.Status("nonexistent-stack")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if status == nil {
		t.Fatal("Expected status result")
	}
	if status.State != "not_found" {
		t.Errorf("Expected state 'not_found', got %q", status.State)
	}
}

func TestEngine_Status_ExistingStack(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	// Create stack directory
	stackDir := filepath.Join(tmpDir, "test-stack")
	if err := os.MkdirAll(stackDir, 0755); err != nil {
		t.Fatalf("Failed to create stack directory: %v", err)
	}

	mockRunner := &MockRunner{
		StateSummary: &tofu.StateSummary{
			Resources:    5,
			LastModified: time.Now(),
		},
	}
	engine.SetRunner(mockRunner)

	status, err := engine.Status("test-stack")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if status == nil {
		t.Fatal("Expected status result")
	}
	if status.State != "running" {
		t.Errorf("Expected state 'running', got %q", status.State)
	}
	if !mockRunner.StateCalled {
		t.Error("Expected State to be called")
	}
}

func TestEngine_Status_EmptyState(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	// Create stack directory
	stackDir := filepath.Join(tmpDir, "test-stack")
	if err := os.MkdirAll(stackDir, 0755); err != nil {
		t.Fatalf("Failed to create stack directory: %v", err)
	}

	mockRunner := &MockRunner{
		StateSummary: &tofu.StateSummary{
			Resources:    0, // No resources
			LastModified: time.Now(),
		},
	}
	engine.SetRunner(mockRunner)

	status, err := engine.Status("test-stack")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if status == nil {
		t.Fatal("Expected status result")
	}
	if status.State != "empty" {
		t.Errorf("Expected state 'empty', got %q", status.State)
	}
}

func TestEngine_Status_StateError(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	// Create stack directory
	stackDir := filepath.Join(tmpDir, "test-stack")
	if err := os.MkdirAll(stackDir, 0755); err != nil {
		t.Fatalf("Failed to create stack directory: %v", err)
	}

	mockRunner := &MockRunner{
		StateError: errors.New("state read failed"),
	}
	engine.SetRunner(mockRunner)

	status, err := engine.Status("test-stack")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}

	if status == nil {
		t.Fatal("Expected status result")
	}
	if status.State != "error" {
		t.Errorf("Expected state 'error', got %q", status.State)
	}
	if len(status.Errors) == 0 {
		t.Error("Expected errors in status")
	}
}

func TestEngine_SetRunner(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	mockRunner := &MockRunner{}
	engine.SetRunner(mockRunner)

	// Verify the runner is set by using it
	tmpDir := t.TempDir()
	engine.SetDataDir(tmpDir)

	spec := &core.UnifiedSpec{
		KombinationSpec: core.KombinationSpec{
			Name: "test",
		},
	}

	_ = engine.Provision(spec)

	if !mockRunner.InitCalled {
		t.Error("SetRunner did not set the runner correctly")
	}
}

func TestEngine_SetDataDir(t *testing.T) {
	engine, err := New()
	if err != nil {
		t.Fatalf("Failed to create engine: %v", err)
	}

	customDir := "/custom/data/dir"
	engine.SetDataDir(customDir)

	// The data dir is used in getStackDir, which is private
	// We verify by checking that Provision creates directories in the right place
	// (tested implicitly in other tests)
}
