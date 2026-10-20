package devicereadiness

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/substrate"
	"github.com/kombifyio/techstack/pkg/agent/httpguard"
)

// ObserveLocalAccelerator runs the accelerator part of the probe on this
// machine. The node-local Guard calls it, so a hypervisor reports the GPU a
// guest can receive without anyone holding a way into the node.
func ObserveLocalAccelerator(ctx context.Context) (substrate.AcceleratorObservation, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-s")
	command.Env = append(os.Environ(), "KOMBIFY_PROBE_SCOPE=accelerators")
	command.Stdin = strings.NewReader(ProbeScript)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil && stdout.Len() == 0 {
		return substrate.AcceleratorObservation{}, fmt.Errorf("device readiness: the accelerator probe did not run: %w", err)
	}
	facts, err := DecodeFacts(stdout.Bytes(), time.Now())
	if err != nil {
		return substrate.AcceleratorObservation{}, err
	}
	return AcceleratorObservation(facts), nil
}

// AcceleratorObservation condenses the passthrough checks into what a guest
// placement needs: the proposed GPU, and the checks that did not pass, which
// explain a missing proposal or what the guest start still depends on.
func AcceleratorObservation(facts Facts) substrate.AcceleratorObservation {
	observation := substrate.AcceleratorObservation{
		Observed: facts.Hypervisor.Observed && facts.Hypervisor.Kind != "" && facts.Accelerators.Observed,
		Proposed: facts.Accelerators.ProposedAccelerator(),
	}
	said := map[string]bool{}
	for _, check := range acceleratorChecks(facts) {
		// A skipped check matters only when it is why there is no proposal;
		// a node without a Thunderbolt device is not missing anything.
		skippedNoise := check.Status == StatusSkipped && (observation.Proposed != nil || check.ID == CheckThunderbolt)
		if check.Status == StatusPass || skippedNoise || said[check.Summary] {
			continue
		}
		said[check.Summary] = true
		observation.Findings = append(observation.Findings, substrate.AcceleratorFinding{
			ID: check.ID, Status: string(check.Status), Summary: check.Summary,
		})
	}
	return observation
}

// ObserveLocalHardware runs the hardware part of the probe on this machine and
// condenses it into what the inventory carries. It reads procfs and sysfs
// through POSIX sh and needs no privilege. It returns nil with the error when
// the probe did not run or read nothing: the caller sends no hardware facts
// then, never an empty or guessed set.
func ObserveLocalHardware(ctx context.Context) (*httpguard.HostHardware, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "sh", "-s")
	command.Env = append(os.Environ(), "KOMBIFY_PROBE_SCOPE=hardware")
	command.Stdin = strings.NewReader(ProbeScript)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	if err := command.Run(); err != nil && stdout.Len() == 0 {
		return nil, fmt.Errorf("device readiness: the hardware probe did not run: %w", err)
	}
	facts, err := DecodeFacts(stdout.Bytes(), time.Now())
	if err != nil {
		return nil, err
	}
	observation := facts.Hardware.Observation(facts.ObservedAt)
	if observation == nil {
		return nil, fmt.Errorf("device readiness: the hardware probe observed nothing")
	}
	return observation, nil
}
