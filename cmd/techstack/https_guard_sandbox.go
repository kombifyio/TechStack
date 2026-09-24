package main

import (
	"errors"
	"log/slog"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

const (
	httpsGuardUnitDir  = "/etc/systemd/system"
	httpsGuardUnitName = "techstack-agent.service"
)

var errHTTPSGuardSandboxRestarting = errors.New("https guard sandbox repair restarted the unit")

const httpsGuardExecutionChannelWritePath = "-/home/kombify/.ssh"

// rewriteHTTPSGuardSandboxUnit grants only the host paths owned by the pinned
// StackKits executors. NoNewPrivileges=yes blocks apt's _apt user, while a
// read-only execution-channel home prevents key-preservation during hardening.
func rewriteHTTPSGuardSandboxUnit(existing string) (string, bool) {
	updated := existing
	changed := false
	if strings.Contains(updated, "NoNewPrivileges=yes") {
		updated = strings.ReplaceAll(updated, "NoNewPrivileges=yes", "NoNewPrivileges=no")
		changed = true
	}
	lines := strings.Split(updated, "\n")
	// StackKits executors install host packages under /usr. For an identical
	// path systemd lets ProtectSystem's read-only /usr win over
	// ReadWritePaths=/usr, so any ProtectSystem mode makes those installs
	// fail with EROFS.
	for index, line := range lines {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "ProtectSystem="); ok && value != "no" && value != "false" {
			lines[index] = "ProtectSystem=no"
			changed = true
		}
	}
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "ReadWritePaths=") {
			continue
		}
		for _, path := range strings.Fields(strings.TrimPrefix(trimmed, "ReadWritePaths=")) {
			if path == httpsGuardExecutionChannelWritePath {
				return strings.Join(lines, "\n"), changed
			}
		}
		lines[index] = line + " " + httpsGuardExecutionChannelWritePath
		return strings.Join(lines, "\n"), true
	}
	return updated, changed
}

func repairHTTPSGuardSandbox(log *slog.Logger) error {
	if runtime.GOOS != "linux" {
		return nil
	}
	unitRoot, err := os.OpenRoot(httpsGuardUnitDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	defer unitRoot.Close()

	data, err := unitRoot.ReadFile(httpsGuardUnitName)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	updated, changed := rewriteHTTPSGuardSandboxUnit(string(data))
	if !changed {
		return nil
	}
	if log != nil {
		log.Info("https_guard_sandbox_repair", "reason", "stackkits_host_authority")
	}
	if err := unitRoot.WriteFile(httpsGuardUnitName, []byte(updated), 0o644); err != nil {
		return err
	}
	_ = exec.Command("systemctl", "daemon-reload").Run()
	cmd := exec.Command("systemctl", "restart", "techstack-agent.service")
	if err := cmd.Start(); err != nil {
		return err
	}
	return errHTTPSGuardSandboxRestarting
}
