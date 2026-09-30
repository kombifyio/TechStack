package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Regression (kombify-Techstack-gjnv): two or three managed rollouts ran their
// StackKits CLI generations side by side in generate_iac and OOM-killed the
// 2Gi Techstack instance, orphaning every in-flight job.
func TestStackKitCLIGenerationsRunOneAtATimePerProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the fake CLI is a POSIX shell script")
	}
	stackKitsDir := t.TempDir()
	for _, name := range []string{DefaultBasementKitRef, "modules", "foundation"} {
		if err := os.MkdirAll(filepath.Join(stackKitsDir, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	scratch := t.TempDir()
	logPath := filepath.Join(scratch, "cli.log")
	releasePath := filepath.Join(scratch, "release")
	binary := filepath.Join(scratch, "stackkit")
	script := "#!/bin/sh\necho start >> " + logPath + "\nwhile [ ! -f " + releasePath + " ]; do sleep 0.02; done\necho end >> " + logPath + "\n"
	if err := os.WriteFile(binary, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	generator := NewStackKitCLIGenerator(stackKitsDir)
	generator.Binary = binary
	generate := func(ctx context.Context, progress func(string)) error {
		workDir := t.TempDir()
		spec := filepath.Join(workDir, "stack-spec.yaml")
		if err := os.WriteFile(spec, []byte("name: gate\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := generator.GenerateStackKitArtifacts(ctx, StackKitArtifactGenerateRequest{
			WorkDir: workDir, StackSpecPath: spec, OutputDir: filepath.Join(workDir, "out"), Progress: progress,
		})
		return err
	}
	cliLog := func() string {
		data, _ := os.ReadFile(logPath)
		return strings.Join(strings.Fields(string(data)), ",")
	}

	first := make(chan error, 1)
	go func() { first <- generate(context.Background(), nil) }()
	for deadline := time.Now().Add(10 * time.Second); cliLog() != "start"; time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("first generation never started the CLI: %q", cliLog())
		}
	}

	waiterCtx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := generate(waiterCtx, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a waiter past its deadline returned %v, want its context error", err)
	}

	secondWaiting := make(chan struct{}, 2)
	second := make(chan error, 1)
	go func() {
		second <- generate(context.Background(), func(string) { secondWaiting <- struct{}{} })
	}()
	select {
	case <-secondWaiting:
	case <-time.After(10 * time.Second):
		t.Fatalf("second generation did not wait for the first: %q", cliLog())
	}
	if err := os.WriteFile(releasePath, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, done := range []chan error{first, second} {
		if err := <-done; err != nil {
			t.Fatalf("generation failed: %v", err)
		}
	}
	if got := cliLog(); got != "start,end,start,end" {
		t.Fatalf("StackKits CLI generations overlapped: %s", got)
	}
}
