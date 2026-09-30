package agent

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	agentpb "github.com/kombifyio/techstack/pkg/api/agentpb"
	"github.com/kombifyio/techstack/pkg/stackkitcommand"
)

// minHostMaintenanceStackKitsVersion is the first StackKits release that
// ships `stackkit host updates plan|apply` and `stackkit host reboot`
// (stackkit.host-maintenance/v1).
const minHostMaintenanceStackKitsVersion = "v0.46.6"

// hostRebootDelay is the fixed delay passed to `stackkit host reboot`. It
// gives the agent time to return the acknowledgement before the node goes
// down; the caller cannot choose it.
const hostRebootDelay = "30s"

// stackKitHostArgs renders the fixed argv of a host maintenance operation.
// The only caller input is the plan digest of an apply, and it must be a
// sha256 digest; everything else is constant.
func stackKitHostArgs(command *agentpb.StackKitCommand) ([]string, error) {
	switch command.Operation {
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_PLAN:
		return []string{"host", "updates", "plan", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_UPDATE_APPLY:
		digest := strings.TrimSpace(command.HostPlanDigest)
		if !stackkitcommand.ValidHostPlanDigest(digest) {
			return nil, fmt.Errorf("StackKit host update apply requires a sha256 plan digest")
		}
		if !command.OwnerApproved {
			return nil, fmt.Errorf("StackKit host update apply requires Owner approval")
		}
		return []string{"host", "updates", "apply", "--plan-digest", digest, "--yes", "--json"}, nil
	case agentpb.StackKitOperation_STACKKIT_OPERATION_HOST_REBOOT:
		if !command.OwnerApproved {
			return nil, fmt.Errorf("StackKit host reboot requires Owner approval")
		}
		return []string{"host", "reboot", "--yes", "--delay", hostRebootDelay, "--json"}, nil
	default:
		return nil, fmt.Errorf("unsupported StackKit host operation %s", command.Operation.String())
	}
}

// HostMaintenanceCapabilities returns the host maintenance capability when
// this agent can run it: the process is root, NoNewPrivileges is off (apt and
// the reboot guard need setuid helpers and systemd-run), and the pinned
// StackKits release ships the host commands. The installer's
// techstack-agent.service runs the HTTPS agent as root with
// NoNewPrivileges=no; the hardened packaging/techstack-agent.service runs as
// User=techstack with NoNewPrivileges=yes and therefore never advertises it.
func (executor *StackKitExecutor) HostMaintenanceCapabilities() []string {
	if !executor.Available() || !hostMaintenancePrivileged() {
		return nil
	}
	pin, err := stackkitrelease.LoadPin(executor.pinPath)
	if err != nil || !stackkitcommand.ReleaseAtLeast(pin.Version, minHostMaintenanceStackKitsVersion) {
		return nil
	}
	return []string{stackkitcommand.HostMaintenanceCapability}
}

func hostMaintenancePrivileged() bool {
	if runtime.GOOS != "linux" || os.Geteuid() != 0 {
		return false
	}
	noNewPrivs, ok := processNoNewPrivs("/proc/self/status")
	return ok && !noNewPrivs
}

// processNoNewPrivs reads the NoNewPrivs flag from a /proc status file.
func processNoNewPrivs(path string) (bool, bool) {
	file, err := os.Open(path) // #nosec G304 -- fixed procfs path.
	if err != nil {
		return false, false
	}
	defer func() { _ = file.Close() }()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		value, found := strings.CutPrefix(scanner.Text(), "NoNewPrivs:")
		if found {
			return strings.TrimSpace(value) != "0", true
		}
	}
	return false, false
}
