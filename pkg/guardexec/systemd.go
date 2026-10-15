package guardexec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var trialUnitPattern = regexp.MustCompile(`^techstack-guard-trial-[0-9]{1,20}$`)

// ExecSystemd drives the host's systemd.
type ExecSystemd struct{}

func (ExecSystemd) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	path, err := exec.LookPath(name)
	if err != nil {
		return nil, err
	}
	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput() // #nosec G204 -- fixed binaries and arguments built in this package.
	if err != nil {
		return output, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return output, nil
}

// Restart restarts a unit.
func (s ExecSystemd) Restart(ctx context.Context, unit string) error {
	if unit != AgentUnit {
		return fmt.Errorf("the executor restarts only %s", AgentUnit)
	}
	_, err := s.run(ctx, "systemctl", "restart", unit)
	return err
}

// Show returns unit properties.
func (s ExecSystemd) Show(ctx context.Context, unit string, properties ...string) (map[string]string, error) {
	args := []string{"show", unit}
	for _, property := range properties {
		args = append(args, "-p", property)
	}
	output, err := s.run(ctx, "systemctl", args...)
	if err != nil {
		return nil, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(output), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = value
		}
	}
	return values, nil
}

// RunTransient starts argv in its own transient unit so it survives the restart
// it supervises. Only the update trial supervisor may be started this way.
func (s ExecSystemd) RunTransient(ctx context.Context, unit string, argv []string) error {
	if !trialUnitPattern.MatchString(unit) {
		return errors.New("only the update trial supervisor may run as a transient unit")
	}
	if len(argv) < 4 || argv[1] != "guard-executor" || (argv[2] != "trial" && argv[2] != "layout-trial") {
		return errors.New("the transient unit must run an update trial supervisor")
	}
	args := append([]string{"--unit=" + unit, "--collect", "--quiet", "--no-block", "--description=kombify Guard update trial", "--property=EnvironmentFile=-/etc/techstack/executor.env", "--"}, argv...)
	_, err := s.run(ctx, "systemd-run", args...)
	return err
}
