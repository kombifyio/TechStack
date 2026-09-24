package jobs

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

const (
	stackKitCommandResultVersion = "stackkit.command-result/v1"
	stackKitDriftReportVersion   = "stackkit.drift-report/v1"
)

type stackKitDriftReport struct {
	SchemaVersion string                 `json:"schemaVersion"`
	HasDrift      bool                   `json:"hasDrift"`
	Subjects      []stackKitDriftSubject `json:"subjects"`
}

type stackKitDriftSubject struct {
	Subject string `json:"subject"`
	Status  string `json:"status"`
	Code    string `json:"code,omitempty"`
}

type stackKitCommandEnvelope struct {
	SchemaVersion string          `json:"schemaVersion"`
	Command       string          `json:"command"`
	Status        string          `json:"status"`
	Data          json.RawMessage `json:"data"`
}

func detectStackKitDrift(ctx context.Context, workDir string, timeout time.Duration) (stackKitDriftReport, error) {
	stdout, err := runStackKitDrift(ctx, workDir, timeout, "detect")
	if err != nil {
		return stackKitDriftReport{}, err
	}
	report, err := parseStackKitDriftReport(stdout)
	if err != nil {
		return stackKitDriftReport{}, err
	}
	return report, nil
}

func reconcileStackKitDrift(ctx context.Context, workDir string, timeout time.Duration) error {
	_, err := runStackKitDrift(ctx, workDir, timeout, "reconcile")
	return err
}

func runStackKitDrift(ctx context.Context, workDir string, timeout time.Duration, operation string) ([]byte, error) {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return nil, fmt.Errorf("StackKits drift requires a workspace directory")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if timeout <= 0 {
		if operation == "reconcile" {
			timeout = stackKitWriteCommandTimeout
		} else {
			timeout = stackKitReadCommandTimeout
		}
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	binary := firstNonEmpty(strings.TrimSpace(os.Getenv(stackKitCLIEnv)), defaultStackKitCLIBinary)
	args := []string{"--no-log", "--chdir", workDir, "drift", operation, "--json"}
	if operation == "reconcile" {
		args = []string{"--no-log", "--chdir", workDir, "drift", "reconcile", "--mode", "standard", "--owner-approve", "--json"}
	}
	cmd := exec.CommandContext(runCtx, binary, args...) // #nosec G702 -- the operator-owned process environment selects the executable; arguments are passed without a shell.
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if runCtx.Err() != nil {
			return nil, fmt.Errorf("StackKits drift %s timed out after %s: %w", operation, timeout, runCtx.Err())
		}
		detail := firstNonEmpty(strings.TrimSpace(stdout.String()), strings.TrimSpace(stderr.String()))
		return nil, fmt.Errorf("StackKits drift %s failed: %w: %s", operation, err, tailText(detail, 4000))
	}
	return stdout.Bytes(), nil
}

func parseStackKitDriftReport(raw []byte) (stackKitDriftReport, error) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return stackKitDriftReport{}, fmt.Errorf("StackKits drift detect returned empty output")
	}
	var envelope stackKitCommandEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return stackKitDriftReport{}, fmt.Errorf("decode StackKits drift detect result: %w", err)
	}
	if envelope.SchemaVersion != "" && envelope.SchemaVersion != stackKitCommandResultVersion {
		return stackKitDriftReport{}, fmt.Errorf("StackKits drift detect returned an unsupported command result")
	}
	payload := envelope.Data
	if len(bytes.TrimSpace(payload)) == 0 {
		payload = raw
	}
	var report stackKitDriftReport
	if err := json.Unmarshal(payload, &report); err != nil {
		return stackKitDriftReport{}, fmt.Errorf("decode StackKits drift report: %w", err)
	}
	if report.SchemaVersion != "" && report.SchemaVersion != stackKitDriftReportVersion {
		return stackKitDriftReport{}, fmt.Errorf("StackKits drift detect returned an unsupported drift report")
	}
	return report, nil
}
