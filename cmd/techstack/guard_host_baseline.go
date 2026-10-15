package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"github.com/kombifyio/techstack/internal/guardbootstrap"
)

func isGuardHostBaselineMode(args []string) bool {
	return len(args) > 2 && args[1] == "guard" && args[2] == "host-baseline"
}

// runGuardHostBaseline prepares or verifies the neutral host contract. It never
// invokes the provider execution-channel or tenant enrollment paths.
func runGuardHostBaseline(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: techstack guard host-baseline prepare|verify|check-os [--ubuntu 26.04|24.04]")
	}
	flags := flag.NewFlagSet("techstack guard host-baseline", flag.ContinueOnError)
	version := flags.String("ubuntu", "26.04", "declared Ubuntu image version")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("unexpected host-baseline arguments")
	}
	script, err := guardbootstrap.RenderNeutralUbuntuHostBaseline(*version, args[0])
	if err != nil {
		return err
	}
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return errors.New("host-baseline requires root on Linux")
	}
	command := exec.CommandContext(ctx, "/bin/bash", "-s")
	command.Stdin = strings.NewReader(script)
	command.Stdout, command.Stderr = os.Stderr, os.Stderr
	if err := command.Run(); err != nil {
		return errors.New("host-baseline did not satisfy the declared Ubuntu profile")
	}
	if args[0] == "check-os" {
		return json.NewEncoder(os.Stdout).Encode(map[string]string{
			"status": "os-compatible", "profile": string(guardbootstrap.UbuntuHostPrepProfile(*version)),
		})
	}
	query := append([]string{"--show", "--showformat=${binary:Package}=${Version}\n"}, guardbootstrap.UbuntuHostBaselinePackages()...)
	packages, err := exec.CommandContext(ctx, "dpkg-query", query...).Output()
	if err != nil {
		return errors.New("host-baseline package version evidence unavailable")
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "ready", "profile": string(guardbootstrap.UbuntuHostPrepProfile(*version)),
		"executionChannel": "not-provisioned", "enrollment": "not-performed",
		"packages": strings.Split(strings.TrimSpace(string(packages)), "\n"),
	})
}
