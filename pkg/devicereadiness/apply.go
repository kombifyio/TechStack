package devicereadiness

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"
)

// Applying a resolution is the moment this capability stops observing and
// starts changing a machine that has agreed to nothing yet. Three things make
// that defensible and all three are enforced here rather than by the caller:
// only catalog entries can be applied, a replaced file is kept beside itself so
// the device can be put back, and everything that happened is recorded whether
// it worked or not.

// backupSuffix marks the copy of a file this package replaced. It is the only
// trace an undo needs: no executor state, no remembered history.
const backupSuffix = ".kombify-readiness-backup"

// Parameters are the values a catalog entry leaves open, because they can only
// be known from the report or from the executor. Nothing outside this closed
// set is substituted, so a catalog entry can never be turned into an arbitrary
// command by its inputs.
type Parameters struct {
	// Interface is the interface a fix acts on, chosen from the report.
	Interface string
	// OperatorTime is this machine's clock, used to correct a device that has
	// no time source of its own.
	OperatorTime time.Time
}

// Receipt is what happened when a resolution was applied. It is stored beside
// the report and is the answer to "what did we do to this machine".
type Receipt struct {
	SchemaVersion string    `json:"schemaVersion"`
	ResolutionID  string    `json:"resolutionId"`
	StartedAt     time.Time `json:"startedAt"`
	FinishedAt    time.Time `json:"finishedAt"`

	// Device identifies which machine was changed, by the key it presented
	// rather than by an address that may be reassigned.
	DeviceHost           string `json:"deviceHost"`
	DeviceKeyFingerprint string `json:"deviceKeyFingerprint"`

	// Files are the paths written, each saying whether a previous version was
	// kept and where.
	Files []FileOutcome `json:"files,omitempty"`
	// Commands are what ran, in order, with bounded and redacted output.
	Commands []commandResult `json:"commands,omitempty"`

	// EgressUsed records that a temporary way out was open while this ran,
	// because that is a fact about the change, not a detail of it.
	EgressUsed bool `json:"egressUsed"`

	Succeeded bool   `json:"succeeded"`
	Failure   string `json:"failure,omitempty"`
}

// FileOutcome is one file the executor wrote.
type FileOutcome struct {
	Path string `json:"path"`
	// BackupPath is empty when nothing was replaced: the file is new, and
	// undoing it means deleting it.
	BackupPath string `json:"backupPath,omitempty"`
	// Skipped is set when an idempotent append found its marker already
	// present, so a second apply changed nothing.
	Skipped bool `json:"skipped"`
}

// Apply carries out one catalog entry against the device.
//
// The resolution is looked up by id rather than taken from the caller: a
// caller that could pass a Resolution could pass any command at all, and the
// catalog would stop being the boundary it exists to be.
func (s *Session) Apply(ctx context.Context, resolutionID string, params Parameters) (Receipt, error) {
	receipt := Receipt{
		SchemaVersion:        SchemaVersion,
		ResolutionID:         resolutionID,
		StartedAt:            time.Now().UTC(),
		DeviceHost:           s.target.Host,
		DeviceKeyFingerprint: s.hostKeyFingerprint,
		EgressUsed:           s.egress != nil,
	}

	resolution, found := ResolutionByID(resolutionID)
	if !found {
		receipt.FinishedAt = time.Now().UTC()
		receipt.Failure = "no such resolution"
		return receipt, fmt.Errorf("device readiness: %q is not in the resolution catalog", resolutionID)
	}
	if resolution.Mode != ModeApply {
		receipt.FinishedAt = time.Now().UTC()
		receipt.Failure = "this resolution is advice, not a change"
		return receipt, fmt.Errorf("device readiness: %q is advice and cannot be carried out", resolutionID)
	}
	if resolution.RequiresEgress && s.egress == nil {
		receipt.FinishedAt = time.Now().UTC()
		receipt.Failure = "this resolution needs a way out and none is open"
		return receipt, fmt.Errorf("device readiness: %q needs egress, which is not open", resolutionID)
	}

	// Files first, so a service restart in Commands observes the configuration
	// it is being restarted for.
	for _, change := range resolution.Files {
		outcome, err := s.writeFile(ctx, change, resolution.RequiresRoot)
		receipt.Files = append(receipt.Files, outcome)
		if err != nil {
			receipt.FinishedAt = time.Now().UTC()
			receipt.Failure = err.Error()
			return receipt, err
		}
	}

	for _, command := range resolution.Commands {
		rendered, err := renderCommand(command, params)
		if err != nil {
			receipt.FinishedAt = time.Now().UTC()
			receipt.Failure = err.Error()
			return receipt, err
		}
		result, err := s.run(ctx, elevate(rendered, resolution.RequiresRoot), rendered, nil, DefaultCommandTimeout)
		receipt.Commands = append(receipt.Commands, result)
		if err != nil {
			receipt.FinishedAt = time.Now().UTC()
			receipt.Failure = err.Error()
			return receipt, err
		}
		if result.ExitCode != 0 {
			receipt.FinishedAt = time.Now().UTC()
			receipt.Failure = fmt.Sprintf("%q exited %d", result.Command, result.ExitCode)
			return receipt, fmt.Errorf("device readiness: %s", receipt.Failure)
		}
	}

	receipt.FinishedAt = time.Now().UTC()
	receipt.Succeeded = true
	return receipt, nil
}

// writeFile puts one declared file on the device, keeping whatever was there.
func (s *Session) writeFile(ctx context.Context, change FileChange, asRoot bool) (FileOutcome, error) {
	outcome := FileOutcome{Path: change.Path}

	if change.AppendUnlessPresent != "" {
		found, err := s.fileContains(ctx, change.Path, change.AppendUnlessPresent, asRoot)
		if err != nil {
			return outcome, err
		}
		if found {
			outcome.Skipped = true
			return outcome, nil
		}
	}

	if change.Backup {
		existed, err := s.fileExists(ctx, change.Path, asRoot)
		if err != nil {
			return outcome, err
		}
		if existed {
			backup := change.Path + backupSuffix
			// Copy rather than move: a device that loses power between the two
			// steps must still have the file it started with.
			result, err := s.run(ctx, elevate(shellQuoteCommand([]string{"cp", "-a", change.Path, backup}), asRoot), "keep a copy of "+change.Path, nil, DefaultCommandTimeout)
			if err != nil {
				return outcome, err
			}
			if result.ExitCode != 0 {
				return outcome, fmt.Errorf("device readiness: could not keep a copy of %s before replacing it", change.Path)
			}
			outcome.BackupPath = backup
		}
	}

	mode := change.Mode
	if mode == 0 {
		mode = 0o644
	}
	// The content travels on standard input rather than inside the command
	// line, so nothing in it is ever interpreted by a shell.
	var write string
	if change.Append != "" {
		write = fmt.Sprintf("cat >> %s", shellQuote(change.Path))
	} else {
		write = fmt.Sprintf("cat > %s", shellQuote(change.Path))
	}
	payload := change.Content
	if change.Append != "" {
		payload = change.Append
	}

	script := fmt.Sprintf("set -e; mkdir -p %s; %s; chmod %o %s",
		shellQuote(parentDir(change.Path)), write, mode, shellQuote(change.Path))

	result, err := s.run(ctx, elevate(script, asRoot), "write "+change.Path, []byte(payload), DefaultCommandTimeout)
	if err != nil {
		return outcome, err
	}
	if result.ExitCode != 0 {
		return outcome, fmt.Errorf("device readiness: could not write %s: %s", change.Path, firstLine(result.Stderr))
	}
	return outcome, nil
}

func (s *Session) fileExists(ctx context.Context, path string, asRoot bool) (bool, error) {
	result, err := s.run(ctx, elevate("test -e "+shellQuote(path), asRoot), "look for "+path, nil, DefaultCommandTimeout)
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

func (s *Session) fileContains(ctx context.Context, path, needle string, asRoot bool) (bool, error) {
	command := fmt.Sprintf("grep -qF -- %s %s", shellQuote(needle), shellQuote(path))
	result, err := s.run(ctx, elevate(command, asRoot), "look inside "+path, nil, DefaultCommandTimeout)
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

// Undo restores what a receipt changed: files that were replaced come back from
// their copy, and files that were created are removed. Commands are not undone,
// because a catalog entry that cannot be reversed by its files alone is marked
// as not reversible and never offered as one that can.
func (s *Session) Undo(ctx context.Context, receipt Receipt) (Receipt, error) {
	undo := Receipt{
		SchemaVersion:        SchemaVersion,
		ResolutionID:         receipt.ResolutionID,
		StartedAt:            time.Now().UTC(),
		DeviceHost:           s.target.Host,
		DeviceKeyFingerprint: s.hostKeyFingerprint,
	}

	resolution, found := ResolutionByID(receipt.ResolutionID)
	if !found || !resolution.Reversible {
		undo.FinishedAt = time.Now().UTC()
		undo.Failure = "this change cannot be reversed from its receipt"
		return undo, fmt.Errorf("device readiness: %q is not reversible", receipt.ResolutionID)
	}

	// Latest file first, so a fix that wrote several files unwinds in the
	// order it was applied.
	files := append([]FileOutcome(nil), receipt.Files...)
	sort.SliceStable(files, func(i, j int) bool { return i > j })

	for _, file := range files {
		if file.Skipped {
			continue
		}
		var command string
		if file.BackupPath != "" {
			command = fmt.Sprintf("mv -f %s %s", shellQuote(file.BackupPath), shellQuote(file.Path))
		} else {
			command = "rm -f " + shellQuote(file.Path)
		}
		result, err := s.run(ctx, elevate(command, resolution.RequiresRoot), command, nil, DefaultCommandTimeout)
		undo.Commands = append(undo.Commands, result)
		if err != nil {
			undo.FinishedAt = time.Now().UTC()
			undo.Failure = err.Error()
			return undo, err
		}
		undo.Files = append(undo.Files, FileOutcome{Path: file.Path})
	}

	// A fix whose effect needed a command to take hold needs one to let go.
	// The catalog's own commands are re-run only where they are a reload, which
	// is why this is the last command and never the whole list.
	if reload := reloadCommandFor(resolution); reload != "" {
		result, err := s.run(ctx, elevate(reload, resolution.RequiresRoot), reload, nil, DefaultCommandTimeout)
		undo.Commands = append(undo.Commands, result)
		if err != nil {
			undo.FinishedAt = time.Now().UTC()
			undo.Failure = err.Error()
			return undo, err
		}
	}

	undo.FinishedAt = time.Now().UTC()
	undo.Succeeded = true
	return undo, nil
}

// reloadCommandFor returns the one command that makes a removed file take
// effect. It is deliberately a small allowlist rather than "re-run everything":
// re-running an install or a clock correction while undoing would be absurd.
func reloadCommandFor(resolution Resolution) string {
	for _, command := range resolution.Commands {
		if len(command) == 0 {
			continue
		}
		switch {
		case command[0] == "netplan":
			return "netplan apply"
		case command[0] == "systemctl" && len(command) >= 3 && command[1] == "restart":
			return "systemctl restart " + command[2]
		}
	}
	return ""
}

// renderCommand fills the placeholders a catalog entry left open. An unknown
// placeholder is refused rather than passed through: a command reaching the
// device with an unsubstituted variable would be interpreted by its shell.
func renderCommand(command []string, params Parameters) (string, error) {
	rendered := make([]string, 0, len(command))
	for _, argument := range command {
		switch {
		case argument == "$IFACE":
			if params.Interface == "" {
				return "", fmt.Errorf("device readiness: this fix needs an interface and none was chosen")
			}
			// The interface comes from the request. Only a kernel interface
			// name may reach the device: anything else could carry an option
			// or the one expansion shellQuoteCommand deliberately passes through.
			if !interfaceNamePattern.MatchString(params.Interface) {
				return "", fmt.Errorf("device readiness: %q is not an interface name", params.Interface)
			}
			rendered = append(rendered, params.Interface)
		case argument == "$OPERATOR_TIME":
			if params.OperatorTime.IsZero() {
				return "", fmt.Errorf("device readiness: this fix needs the operator clock and none was given")
			}
			rendered = append(rendered, params.OperatorTime.UTC().Format("2006-01-02 15:04:05"))
		case strings.Contains(argument, "$(") || strings.HasPrefix(argument, "$"):
			// The kernel-release substitution is the one expansion a command
			// legitimately needs the device to perform, because only the
			// device knows which kernel it is running.
			if strings.Contains(argument, "$(uname -r)") {
				rendered = append(rendered, argument)
				continue
			}
			return "", fmt.Errorf("device readiness: refusing to send an unresolved placeholder %q", argument)
		default:
			rendered = append(rendered, argument)
		}
	}
	return shellQuoteCommand(rendered), nil
}

// interfaceNamePattern admits Linux interface names (IFNAMSIZ allows 15
// characters) such as eth0, enp3s0, br-lan or eth0.100.
var interfaceNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,14}$`)

func elevate(command string, asRoot bool) string {
	if !asRoot {
		return command
	}
	// A device whose only account is root has no sudo, and a device with sudo
	// may need it; asking for one and falling back to the other is the only
	// form that works on both without knowing in advance.
	return "if [ \"$(id -u)\" = 0 ]; then sh -c " + shellQuote(command) +
		"; else sudo -n sh -c " + shellQuote(command) + "; fi"
}

// shellQuote wraps a value so a shell reads it as one literal argument.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

func shellQuoteCommand(command []string) string {
	quoted := make([]string, 0, len(command))
	for _, argument := range command {
		// An argument the catalog deliberately left for the device's own shell
		// is passed through; everything else is quoted.
		if strings.Contains(argument, "$(uname -r)") {
			quoted = append(quoted, argument)
			continue
		}
		quoted = append(quoted, shellQuote(argument))
	}
	return strings.Join(quoted, " ")
}

func parentDir(path string) string {
	if idx := strings.LastIndexByte(path, '/'); idx > 0 {
		return path[:idx]
	}
	return "/"
}
