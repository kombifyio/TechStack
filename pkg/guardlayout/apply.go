package guardlayout

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/internal/privatefs"
	"github.com/kombifyio/techstack/pkg/guardbinding"
)

// Default locations of a Guard host.
const (
	DefaultBinaryPath     = "/usr/local/bin/techstack"
	DefaultOperationsPath = "/usr/local/libexec/techstack-stackkit-operations"
	DefaultSocketPath     = "/run/techstack-guard-executor.sock"
	DefaultUser           = "kombify-guard"
	systemdDir            = "/etc/systemd/system"
	stateRoot             = "/var/lib/techstack"
	executorStateDir      = "/var/lib/techstack-guard-executor"
	enrollmentFile        = EnrollmentFile
	layoutTrialFile       = "layout-trial.json"
)

// Runner runs a host command.
type Runner interface {
	Run(ctx context.Context, name string, args ...string) error
}

type execRunner struct{}

func (execRunner) Run(ctx context.Context, name string, args ...string) error {
	path, err := exec.LookPath(name)
	if err != nil {
		return err
	}
	output, err := exec.CommandContext(ctx, path, args...).CombinedOutput() // #nosec G204 -- fixed binaries and arguments built in this package.
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return nil
}

// Options configure Apply.
type Options struct {
	BinaryPath     string
	OperationsPath string
	GuardUser      string
	SocketPath     string
	// Root prefixes every absolute path; tests point it at a temporary tree.
	Root   string
	Runner Runner
	// Lookup resolves the Guard user; nil uses os/user.
	Lookup func(name string) (uid, gid int, err error)
	// Chown changes the ownership of a directory the caller names by its
	// root-owned path; nil uses os.Lchown.
	Chown func(path string, uid, gid int) error
	// ChownBeneath changes the ownership of one entry inside a daemon-writable
	// tree, confined to root; nil uses (*os.Root).Lchown.
	ChownBeneath func(root *os.Root, rel string, uid, gid int) error
	// VerifyExecutor proves the executor answers through its socket as root
	// before the daemon is switched to it.
	VerifyExecutor func(ctx context.Context, socket string) error
	// NotifyReady is whether this binary signals readiness to systemd.
	NotifyReady bool
	// Activate restarts the daemon onto the new layout. A migration of a
	// running Guard instead defers to a supervised trial.
	Activate bool
	// Trial makes the switch reversible: the supervisor restores the legacy unit
	// unless the new daemon reports an authenticated round trip.
	Trial func(ctx context.Context, id string) error
	Log   *slog.Logger
	Now   func() time.Time
}

func (o *Options) defaults() {
	if o.BinaryPath == "" {
		o.BinaryPath = DefaultBinaryPath
	}
	if o.OperationsPath == "" {
		o.OperationsPath = DefaultOperationsPath
	}
	if o.GuardUser == "" {
		o.GuardUser = DefaultUser
	}
	if o.SocketPath == "" {
		o.SocketPath = DefaultSocketPath
	}
	if o.Runner == nil {
		o.Runner = execRunner{}
	}
	if o.Lookup == nil {
		o.Lookup = lookupUser
	}
	if o.Chown == nil {
		o.Chown = os.Lchown
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = func() time.Time { return time.Now().UTC() }
	}
}

func (o *Options) path(p string) string { return filepath.Join(o.Root, p) }

func lookupUser(name string) (int, int, error) {
	account, err := user.Lookup(name)
	if err != nil {
		return 0, 0, err
	}
	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return 0, 0, err
	}
	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return 0, 0, err
	}
	return uid, gid, nil
}

// Result reports what Apply did.
type Result struct {
	// Migrated is true when an existing root Guard was switched to the layout.
	Migrated bool
	// Switched is true when the daemon unit now describes the split layout.
	Switched bool
	TrialID  string
}

// unitUser is the User= of a systemd unit, "" when it names none.
func unitUser(unit string) string {
	user := ""
	for _, line := range strings.Split(unit, "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "User="); ok {
			user = strings.TrimSpace(value)
		}
	}
	return user
}

// IsLegacyUnit reports whether a daemon unit still runs the Guard as root.
func IsLegacyUnit(unit string) bool {
	user := unitUser(unit)
	return user == "" || user == "root"
}

// Apply creates the protected layout and, only after the privileged executor is
// proven reachable, switches the daemon unit to it. Enrollment, identity and the
// journal are kept and re-owned, never recreated. An existing root unit is kept
// as a .legacy copy so the old layout can be restored.
func Apply(ctx context.Context, opts Options) (Result, error) {
	opts.defaults()
	var result Result
	if _, err := os.Stat(opts.path("/run/systemd/system")); err != nil {
		return result, errors.New("the privilege-split layout needs systemd")
	}
	if existing, err := os.ReadFile(filepath.Join(opts.path(systemdDir), AgentUnit)); err == nil { // #nosec G304 -- a fixed systemd unit path.
		// Only the root unit this installer wrote, or an earlier split unit, is
		// replaced. Any other unit (for example the gRPC packaging unit) belongs
		// to someone else.
		if user := unitUser(string(existing)); !IsLegacyUnit(string(existing)) && user != opts.GuardUser {
			return result, fmt.Errorf("%s runs as %q and is not a kombify Guard HTTPS unit this command may replace", AgentUnit, user)
		}
	}
	params := UnitParams{
		BinaryPath: opts.BinaryPath, OperationsPath: opts.OperationsPath, GuardUser: opts.GuardUser,
		SocketPath: opts.SocketPath, NotifyReady: opts.NotifyReady,
	}
	if err := ensureUser(ctx, &opts); err != nil {
		return result, err
	}
	uid, gid, err := opts.Lookup(opts.GuardUser)
	if err != nil {
		return result, fmt.Errorf("resolve %s: %w", opts.GuardUser, err)
	}
	if err := prepareExecutorDirectories(&opts); err != nil {
		return result, err
	}
	if err := writeFile(opts.path(ExecutorEnvFile), ExecutorEnvText(params), 0o600, false); err != nil {
		return result, err
	}
	for name, text := range map[string]string{ExecutorSocketUnit: ExecutorSocketText(params), ExecutorUnit: ExecutorUnitText(params)} {
		if err := writeFile(filepath.Join(opts.path(systemdDir), name), text, 0o644, true); err != nil {
			return result, err
		}
	}
	if err := opts.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return result, err
	}
	if err := opts.Runner.Run(ctx, "systemctl", "enable", "--now", ExecutorSocketUnit); err != nil {
		return result, fmt.Errorf("start the executor socket: %w", err)
	}
	if opts.VerifyExecutor != nil {
		if err := opts.VerifyExecutor(ctx, opts.SocketPath); err != nil {
			// The old layout stays in charge: nothing the daemon runs under was touched.
			return result, fmt.Errorf("the privileged executor does not answer, keeping the current layout: %w", err)
		}
	}

	agentUnit := filepath.Join(opts.path(systemdDir), AgentUnit)
	existing, readErr := os.ReadFile(agentUnit) // #nosec G304 -- a fixed systemd unit path.
	legacy := readErr == nil && IsLegacyUnit(string(existing))
	unlock, err := lockLayoutState(&opts)
	if err != nil {
		return result, err
	}
	defer unlock()
	existing, readErr = os.ReadFile(agentUnit)
	legacy = readErr == nil && IsLegacyUnit(string(existing))
	if _, err := readLayoutTrial(&opts); err == nil {
		return result, errors.New("a prepared layout trial must be reconciled before another migration")
	} else if !errors.Is(err, os.ErrNotExist) {
		return result, err
	}
	if readErr == nil && !legacy {
		// A verified split installation already has custody under the daemon.
		// Reapplying executor prerequisites must not start another transfer.
		if err := adoptRootWrittenEnrollment(&opts, uid, gid); err != nil {
			return result, err
		}
		if err := writeFile(agentUnit, AgentUnitText(params), 0o644, true); err != nil {
			return result, err
		}
		if err := opts.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return result, errors.Join(err, restoreSplitUnit(ctx, &opts, string(existing)))
		}
		if err := opts.Runner.Run(ctx, "systemctl", "enable", AgentUnit); err != nil {
			return result, errors.Join(err, restoreSplitUnit(ctx, &opts, string(existing)))
		}
		if opts.Activate {
			if err := opts.Runner.Run(ctx, "systemctl", "restart", AgentUnit); err != nil {
				return result, errors.Join(err, restoreSplitUnit(ctx, &opts, string(existing)))
			}
		}
		result.Switched = true
		return result, nil
	}
	trialID := fmt.Sprintf("%d", opts.Now().UnixNano())
	trial, err := captureLayoutTrial(&opts, trialID, uid, gid, params, legacy)
	if err != nil {
		return result, err
	}
	if err := reserveLayoutTrial(&opts, trial); err != nil {
		return result, err
	}
	if legacy {
		result.Migrated = true
		if err := writeFile(agentUnit+LegacyUnitSuffix, string(existing), 0o644, true); err != nil {
			return result, errors.Join(err, discardPreparedTrial(&opts, trial.ID))
		}
	}
	// The root caller must publish the existing non-secret handoff marker
	// before starting its supervisor. Create a missing projection directory
	// only after verification; do not re-own an existing daemon directory.
	if err := os.MkdirAll(opts.path(stateRoot+"/guard-handoff"), 0o700); err != nil {
		rollbackErr := rollbackLayoutLocked(ctx, &opts)
		if rollbackErr == nil {
			rollbackErr = finishLayoutTrialLocked(&opts, trialID)
		}
		return result, errors.Join(err, rollbackErr)
	}
	unlock() // The independent supervisor must acquire this same lifecycle lock.
	switch {
	case legacy && opts.Trial != nil:
		result.TrialID = trialID
		if err := opts.Trial(ctx, result.TrialID); err != nil {
			rollbackErr := rollbackLayout(ctx, &opts)
			if rollbackErr == nil {
				rollbackErr = FinishLayoutTrial(opts, trialID)
			}
			return result, errors.Join(fmt.Errorf("start the layout trial: %w", err), rollbackErr)
		}
		if current, err := os.ReadFile(agentUnit); err == nil {
			result.Switched = !IsLegacyUnit(string(current))
		}
	default:
		// install.sh invokes Apply outside the daemon's service group. A live
		// root daemon always supplies Trial and transfers in its supervisor.
		if err := startLayoutTrial(ctx, &opts, trialID, opts.Activate); err != nil {
			if phase, readErr := LayoutTrialPhase(opts, trialID); readErr == nil && phase == "rolled_back" {
				return result, errors.Join(err, FinishLayoutTrial(opts, trialID))
			}
			return result, err
		}
		result.Switched = true
		if err := CommitLayoutTrial(opts, trialID); err != nil {
			return result, err
		}
		if err := FinishLayoutTrial(opts, trialID); err != nil {
			return result, err
		}
	}
	return result, nil
}

func restoreSplitUnit(ctx context.Context, opts *Options, text string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	if err := writeFile(filepath.Join(opts.path(systemdDir), AgentUnit), text, 0o644, true); err != nil {
		return err
	}
	if err := opts.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if opts.Activate {
		return opts.Runner.Run(ctx, "systemctl", "restart", AgentUnit)
	}
	return nil
}

func discardPreparedTrial(opts *Options, id string) error {
	trial, err := readLayoutTrial(opts)
	if err != nil {
		return err
	}
	if trial.ID != id || trial.Phase != "prepared" {
		return errors.New("layout preparation authority changed")
	}
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(layoutTrialFile)
}

// RestoreLegacy puts the pre-split daemon unit back and restarts it. The
// supervisor calls it when the new daemon never reported ready.
func RestoreLegacy(ctx context.Context, opts Options) error {
	opts.defaults()
	return restoreLegacy(ctx, &opts)
}

func restoreLegacy(ctx context.Context, opts *Options) error {
	return rollbackLayout(ctx, opts)
}

func ensureUser(ctx context.Context, opts *Options) error {
	if _, _, err := opts.Lookup(opts.GuardUser); err == nil {
		return nil
	}
	home := stateRoot + "/guard"
	shell := "/usr/sbin/nologin"
	if _, err := os.Stat(opts.path(shell)); err != nil {
		shell = "/sbin/nologin"
	}
	if err := opts.Runner.Run(ctx, "useradd", "--system", "--user-group", "--home-dir", home, "--no-create-home", "--shell", shell, opts.GuardUser); err != nil {
		return fmt.Errorf("create the %s system user: %w", opts.GuardUser, err)
	}
	return nil
}

// prepareExecutorDirectories leaves every existing daemon inode untouched.
// Custody is transferred only after the executor answers and the supervisor
// has stopped the daemon that could still write it.
func prepareExecutorDirectories(opts *Options) error {
	for _, path := range []string{"/etc/techstack", stateRoot, "/usr/local/libexec"} {
		if err := os.MkdirAll(opts.path(path), 0o755); err != nil {
			return err
		}
	}
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return err
	}
	return root.Close()
}

func transferDaemonCustody(opts *Options, uid, gid int) error {
	type entry struct {
		path      string
		mode      os.FileMode
		uid, gid  int
		recursive bool
	}
	entries := []entry{
		{"/etc/techstack", 0o750, 0, gid, false},
		{stateRoot, 0o755, 0, 0, false},
		{stateRoot + "/guard", 0o700, uid, gid, true},
		{stateRoot + "/guard-handoff", 0o700, uid, gid, true},
		{"/usr/local/libexec", 0o755, 0, 0, false},
	}
	for _, e := range entries {
		full := opts.path(e.path)
		if err := os.MkdirAll(full, e.mode); err != nil {
			return err
		}
		if err := os.Chmod(full, e.mode); err != nil {
			return err
		}
		if err := opts.Chown(full, e.uid, e.gid); err != nil {
			return err
		}
		if e.recursive {
			if err := chownTree(opts, full, e.uid, e.gid); err != nil {
				return err
			}
		}
	}
	// The enrollment is the Guard's credential; only the daemon user reads it.
	enrollment := opts.path(enrollmentFile)
	if info, err := os.Lstat(enrollment); err == nil {
		if !info.Mode().IsRegular() || linkCount(info) > 1 {
			return errors.New("the enrollment must be a single-link regular file")
		}
		if err := captureBindingPins(opts, enrollment, info); err != nil {
			return err
		}
		if err := os.Chmod(enrollment, 0o600); err != nil {
			return err
		}
		if err := opts.Chown(enrollment, uid, gid); err != nil {
			return err
		}
	}
	return nil
}

// adoptRootWrittenEnrollment hands a split installation an enrollment root
// wrote since the last layout run, such as after a re-pairing with sudo: it
// re-pins its binding keys and gives the file to the daemon user. An enrollment
// the daemon user already owns is left alone and never re-pinned.
func adoptRootWrittenEnrollment(opts *Options, uid, gid int) error {
	enrollment := opts.path(enrollmentFile)
	info, err := os.Lstat(enrollment)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || linkCount(info) > 1 {
		return errors.New("the enrollment must be a single-link regular file")
	}
	if !ownedByCaller(info) {
		return nil
	}
	if err := captureBindingPins(opts, enrollment, info); err != nil {
		return err
	}
	if err := os.Chmod(enrollment, 0o600); err != nil {
		return err
	}
	return opts.Chown(enrollment, uid, gid)
}

// captureBindingPins copies the enrollment's binding keys into the executor's
// root-only pin file. Only an enrollment this (root) process owns is trusted:
// once the daemon user owns it, the daemon could have rewritten it, so a later
// run keeps the pins it already has.
func captureBindingPins(opts *Options, enrollment string, info fs.FileInfo) error {
	if !ownedByCaller(info) {
		return nil
	}
	data, err := os.ReadFile(enrollment) // #nosec G304 -- the fixed enrollment path.
	if err != nil {
		return err
	}
	// The enrollment is the redemption response, usually in its {"data": ...}
	// envelope; the daemon reads it the same way (parseAgentEnrollment).
	type enrollmentKeys struct {
		RuntimeAgentID string                   `json:"runtime_agent_id"`
		AgentToken     string                   `json:"agent_token"`
		Keys           []guardbinding.PinnedKey `json:"binding_signing_keys"`
	}
	var doc struct {
		enrollmentKeys
		Data enrollmentKeys `json:"data"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("read binding keys from the enrollment: %w", err)
	}
	keys := doc.Keys
	if doc.Data.RuntimeAgentID != "" || doc.Data.AgentToken != "" {
		keys = doc.Data.Keys
	}
	encoded, err := guardbinding.EncodePins(keys)
	if err != nil {
		return err
	}
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return err
	}
	defer root.Close()
	return privatefs.Replace(root, guardbinding.PinFile, encoded, 0o600)
}

// layoutTrial is protected lifecycle metadata, never a copy of key/journal
// bytes. The shared handoff marker cannot authorize an ownership change.
type layoutTrial struct {
	ID             string `json:"id"`
	Root           string `json:"root"`
	UID, GID       int
	Legacy         bool              `json:"legacy"`
	Phase          string            `json:"phase"`
	CustodyStarted bool              `json:"custody_started"`
	StartAttempted bool              `json:"start_attempted"`
	Params         UnitParams        `json:"params"`
	Ownership      []layoutOwnership `json:"ownership"`
}

type layoutOwnership struct {
	Path     string `json:"path"`
	UID, GID int
	Mode     os.FileMode `json:"mode"`
	Device   uint64      `json:"device"`
	Inode    uint64      `json:"inode"`
}

func layoutRoot(opts *Options) (*os.Root, error) {
	path := opts.Root
	if path == "" {
		path = "/"
	}
	return os.OpenRoot(path)
}

func validCustodyPath(path string) bool {
	return path == "etc/techstack" || path == "etc/techstack/agent-enrollment.json" || path == "var/lib/techstack" ||
		path == "usr/local/libexec" || path == "var/lib/techstack/guard" || path == "var/lib/techstack/guard-handoff" ||
		strings.HasPrefix(path, "var/lib/techstack/guard/") || strings.HasPrefix(path, "var/lib/techstack/guard-handoff/")
}

func captureLayoutTrial(opts *Options, id string, uid, gid int, params UnitParams, legacy bool) (layoutTrial, error) {
	root, err := layoutRoot(opts)
	if err != nil {
		return layoutTrial{}, err
	}
	defer root.Close()
	trial := layoutTrial{ID: id, Root: root.Name(), UID: uid, GID: gid, Legacy: legacy, Phase: "prepared", Params: params}
	appendEntry := func(path string, info fs.FileInfo) error {
		if info.Mode().IsRegular() && info.Mode()&(os.ModeSetuid|os.ModeSetgid) != 0 {
			return fmt.Errorf("custody data %s must not carry executable privilege bits", path)
		}
		owner, group, err := ownershipIDs(info)
		if err != nil {
			return err
		}
		device, inode, err := ownershipIdentity(info)
		if err != nil {
			return err
		}
		trial.Ownership = append(trial.Ownership, layoutOwnership{Path: path, UID: owner, GID: group, Mode: info.Mode(), Device: device, Inode: inode})
		return nil
	}
	for _, path := range []string{"etc/techstack", "var/lib/techstack", "usr/local/libexec", "etc/techstack/agent-enrollment.json", "var/lib/techstack/guard", "var/lib/techstack/guard-handoff"} {
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return layoutTrial{}, err
		}
		if info.Mode()&os.ModeSymlink != 0 || (path == "etc/techstack/agent-enrollment.json" && (!info.Mode().IsRegular() || linkCount(info) > 1)) {
			return layoutTrial{}, fmt.Errorf("unsafe custody root %s", path)
		}
		if err := appendEntry(path, info); err != nil {
			return layoutTrial{}, err
		}
		if path != "var/lib/techstack/guard" && path != "var/lib/techstack/guard-handoff" {
			continue
		}
		if err := fs.WalkDir(root.FS(), path, func(entryPath string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entryPath == path {
				return nil
			}
			info, err := root.Lstat(entryPath)
			if err != nil {
				return err
			}
			return appendEntry(entryPath, info)
		}); err != nil {
			return layoutTrial{}, err
		}
	}
	return trial, nil
}

func writeLayoutTrial(opts *Options, trial layoutTrial) error {
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return err
	}
	defer root.Close()
	previous, err := readLayoutTrial(opts)
	if err != nil {
		return err
	}
	if previous.ID != trial.ID {
		return errors.New("layout trial authority changed")
	}
	data, err := json.Marshal(trial)
	if err != nil {
		return err
	}
	return privatefs.Replace(root, layoutTrialFile, data, 0o600)
}

func reserveLayoutTrial(opts *Options, trial layoutTrial) error {
	data, err := json.Marshal(trial)
	if err != nil {
		return err
	}
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return err
	}
	defer root.Close()
	file, err := privatefs.OpenFile(root, layoutTrialFile, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cleanup := func(cause error) error { return errors.Join(cause, file.Close(), root.Remove(layoutTrialFile)) }
	if _, err := file.Write(data); err != nil {
		return cleanup(err)
	}
	if err := file.Sync(); err != nil {
		return cleanup(err)
	}
	if err := file.Close(); err != nil {
		return errors.Join(err, root.Remove(layoutTrialFile))
	}
	return nil
}

func readLayoutTrial(opts *Options) (layoutTrial, error) {
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return layoutTrial{}, err
	}
	defer root.Close()
	data, err := privatefs.ReadFile(root, layoutTrialFile)
	if err != nil {
		return layoutTrial{}, err
	}
	var trial layoutTrial
	if err := json.Unmarshal(data, &trial); err != nil {
		return trial, err
	}
	anchor, err := layoutRoot(opts)
	if err != nil {
		return trial, err
	}
	defer anchor.Close()
	if trial.Root != anchor.Name() || trial.ID == "" || (trial.Phase != "prepared" && trial.Phase != "transferring" && trial.Phase != "trial" && trial.Phase != "committed" && trial.Phase != "rolled_back") {
		return trial, errors.New("layout trial does not bind this installation")
	}
	for _, entry := range trial.Ownership {
		if !validCustodyPath(entry.Path) || filepath.ToSlash(filepath.Clean(entry.Path)) != entry.Path || entry.UID < 0 || entry.GID < 0 {
			return trial, errors.New("layout ownership metadata escapes its installation custody")
		}
	}
	return trial, nil
}

// StartLayoutTrial is called by the existing independent trial supervisor.
// It quiesces the old daemon before any custody inode changes owner.
func StartLayoutTrial(ctx context.Context, opts Options, id string) error {
	opts.defaults()
	return startLayoutTrial(ctx, &opts, id, true)
}

func startLayoutTrial(ctx context.Context, opts *Options, id string, activate bool) error {
	unlock, err := lockLayoutState(opts)
	if err != nil {
		return err
	}
	defer unlock()
	trial, err := readLayoutTrial(opts)
	if err != nil {
		return err
	}
	if trial.ID != id || trial.Phase != "prepared" {
		return errors.New("layout trial is not the prepared transition")
	}
	transition := func() error {
		// Reserve the transfer before stopping: a failed metadata write leaves
		// the current daemon running and original custody untouched.
		trial.Phase = "transferring"
		if err := writeLayoutTrial(opts, trial); err != nil {
			return err
		}
		if activate && trial.Legacy {
			if err := opts.Runner.Run(ctx, "systemctl", "stop", AgentUnit); err != nil {
				return err
			}
		}
		trial.CustodyStarted = true
		if err := writeLayoutTrial(opts, trial); err != nil {
			return err
		}
		if err := transferDaemonCustody(opts, trial.UID, trial.GID); err != nil {
			return err
		}
		if err := writeFile(filepath.Join(opts.path(systemdDir), AgentUnit), AgentUnitText(trial.Params), 0o644, true); err != nil {
			return err
		}
		if err := opts.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
			return err
		}
		if err := opts.Runner.Run(ctx, "systemctl", "enable", AgentUnit); err != nil {
			return err
		}
		if activate {
			trial.StartAttempted = true
			if err := writeLayoutTrial(opts, trial); err != nil {
				return err
			}
			if err := opts.Runner.Run(ctx, "systemctl", "start", AgentUnit); err != nil {
				return err
			}
		}
		trial.Phase = "trial"
		return writeLayoutTrial(opts, trial)
	}
	if err := transition(); err != nil {
		return errors.Join(err, rollbackLayoutLocked(ctx, opts))
	}
	return nil
}

// CommitLayoutTrial durably marks the acknowledged transition; the supervisor
// retains its metadata until terminal bookkeeping succeeds.
func CommitLayoutTrial(opts Options, id string) error {
	opts.defaults()
	unlock, err := lockLayoutState(&opts)
	if err != nil {
		return err
	}
	defer unlock()
	trial, err := readLayoutTrial(&opts)
	if err != nil {
		return err
	}
	if trial.ID != id || (trial.Phase != "trial" && trial.Phase != "committed") {
		return errors.New("layout trial is not ready to commit")
	}
	trial.Phase = "committed"
	return writeLayoutTrial(&opts, trial)
}

// LayoutTrialPhase reads only the protected lifecycle record, never handoff data.
func LayoutTrialPhase(opts Options, id string) (string, error) {
	opts.defaults()
	trial, err := readLayoutTrial(&opts)
	if err != nil {
		return "", err
	}
	if trial.ID != id {
		return "", errors.New("layout trial identity changed")
	}
	return trial.Phase, nil
}

// FinishLayoutTrial removes metadata only after terminal outcome bookkeeping.
func FinishLayoutTrial(opts Options, id string) error {
	opts.defaults()
	unlock, err := lockLayoutState(&opts)
	if err != nil {
		return err
	}
	defer unlock()
	return finishLayoutTrialLocked(&opts, id)
}

func finishLayoutTrialLocked(opts *Options, id string) error {
	trial, err := readLayoutTrial(opts)
	if err != nil {
		return err
	}
	if trial.ID != id || (trial.Phase != "committed" && trial.Phase != "rolled_back") {
		return errors.New("layout trial has no durable terminal disposition")
	}
	root, err := privatefs.Open(opts.path(executorStateDir))
	if err != nil {
		return err
	}
	defer root.Close()
	return root.Remove(layoutTrialFile)
}

func rollbackLayout(ctx context.Context, opts *Options) error {
	unlock, err := lockLayoutState(opts)
	if err != nil {
		return err
	}
	defer unlock()
	return rollbackLayoutLocked(ctx, opts)
}

func rollbackLayoutLocked(ctx context.Context, opts *Options) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	trial, err := readLayoutTrial(opts)
	if err != nil {
		return fmt.Errorf("load original layout custody: %w", err)
	}
	if trial.Phase == "committed" {
		return errors.New("an acknowledged layout must be finalized, not rolled back")
	}
	if trial.Phase == "rolled_back" {
		return nil
	}
	if trial.StartAttempted {
		if err := opts.Runner.Run(ctx, "systemctl", "stop", AgentUnit); err != nil {
			return fmt.Errorf("stop split custody writers: %w", err)
		}
	}
	if trial.CustodyStarted {
		if err := restoreLayoutOwnership(opts, trial); err != nil {
			return fmt.Errorf("restore original layout custody: %w", err)
		}
	}
	agentUnit := filepath.Join(opts.path(systemdDir), AgentUnit)
	if trial.Legacy {
		legacy, err := os.ReadFile(agentUnit + LegacyUnitSuffix)
		if err != nil {
			return err
		}
		if err := writeFile(agentUnit, string(legacy), 0o644, true); err != nil {
			return err
		}
	} else if err := os.Remove(agentUnit); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := opts.Runner.Run(ctx, "systemctl", "daemon-reload"); err != nil {
		return err
	}
	if trial.Legacy {
		// Start is harmless when the legacy daemon never stopped; restart would
		// terminate a failed migration's own root caller before compensation.
		if err := opts.Runner.Run(ctx, "systemctl", "start", AgentUnit); err != nil {
			return err
		}
	}
	trial.Phase = "rolled_back"
	return writeLayoutTrial(opts, trial)
}

func restoreLayoutOwnership(opts *Options, trial layoutTrial) error {
	anchor, err := layoutRoot(opts)
	if err != nil {
		return err
	}
	defer anchor.Close()
	original := map[string]layoutOwnership{}
	for _, entry := range trial.Ownership {
		original[entry.Path] = entry
	}
	restore := func(root *os.Root, rel, path string, info fs.FileInfo) error {
		entry, captured := original[path]
		found := captured
		if !found {
			for parent := filepath.ToSlash(filepath.Dir(path)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
				if inherited, ok := original[parent]; ok {
					entry = layoutOwnership{UID: inherited.UID, GID: inherited.GID, Mode: info.Mode() &^ (os.ModeSetuid | os.ModeSetgid)}
					found = true
					break
				}
			}
		}
		if !found {
			return fmt.Errorf("no original custody authority for %s", path)
		}
		if captured && entry.Mode.Type() != info.Mode().Type() {
			return fmt.Errorf("custody inode type changed for %s", path)
		}
		if info.Mode().IsRegular() && linkCount(info) > 1 {
			uid, gid, err := ownershipIDs(info)
			if err != nil {
				return err
			}
			if uid != entry.UID || gid != entry.GID {
				return fmt.Errorf("cannot restore shared-link custody %s without changing an external inode", path)
			}
			return nil // Neither transfer nor rollback may mutate a shared inode.
		}
		mode := entry.Mode & (os.ModePerm | os.ModeSticky | os.ModeSetuid | os.ModeSetgid)
		device, inode, err := ownershipIdentity(info)
		if err != nil {
			return err
		}
		if !captured || device != entry.Device || inode != entry.Inode {
			mode &^= os.ModeSetuid | os.ModeSetgid
		}
		return restoreOwnershipAt(root, rel, info, entry.UID, entry.GID, mode, opts.ChownBeneath)
	}
	for _, path := range []string{"etc/techstack", "var/lib/techstack", "usr/local/libexec", "etc/techstack/agent-enrollment.json", "var/lib/techstack/guard", "var/lib/techstack/guard-handoff"} {
		info, err := anchor.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if path != "var/lib/techstack/guard" && path != "var/lib/techstack/guard-handoff" {
			parent, err := anchor.OpenRoot(filepath.Dir(path))
			if err != nil {
				return err
			}
			err = restore(parent, filepath.Base(path), path, info)
			_ = parent.Close()
			if err != nil {
				return err
			}
			continue
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("custody root %s was replaced", path)
		}
		tree, err := anchor.OpenRoot(path)
		if err != nil {
			return err
		}
		// Keep every directory private to the restoring process until its
		// children are restored. A second daemon-UID process cannot redirect
		// traversal; each chmod/chown is also bound to a no-follow inode handle.
		var directories []string
		err = fs.WalkDir(tree.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			info, err := tree.Lstat(rel)
			if err != nil {
				return err
			}
			full := path
			if rel != "." {
				full += "/" + filepath.ToSlash(rel)
			}
			if entry.IsDir() {
				if err := restoreOwnershipAt(tree, rel, info, os.Geteuid(), os.Getegid(), 0o700, opts.ChownBeneath); err != nil {
					return err
				}
				directories = append(directories, rel)
				return nil
			}
			return restore(tree, rel, full, info)
		})
		if err == nil {
			for index := len(directories) - 1; index >= 0; index-- {
				rel := directories[index]
				full := path
				if rel != "." {
					full += "/" + filepath.ToSlash(rel)
				}
				info, statErr := tree.Lstat(rel)
				if statErr != nil {
					err = statErr
					break
				}
				if restoreErr := restore(tree, rel, full, info); restoreErr != nil {
					err = restoreErr
					break
				}
			}
		}
		_ = tree.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// chownTree re-owns everything below dir for the Guard user. The tree is
// writable by the daemon, so it is never walked and re-owned by path: a daemon
// that swaps a subdirectory for a symlink between the walk and the chown would
// move the ownership of a root-owned file. The walk and every chown go through
// an os.Root opened on dir, which resolves each component beneath the open
// directory and refuses to leave it, and Lchown never follows the last
// component. A regular file with more than one link is skipped: re-owning it
// would re-own the other path of the same inode (a hard link planted to a
// root-owned file).
func chownTree(opts *Options, dir string, uid, gid int) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer func() { _ = root.Close() }()
	return fs.WalkDir(root.FS(), ".", func(rel string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if rel == "." {
			return nil // the directory itself is re-owned by the caller, by its root-owned path
		}
		if !entry.IsDir() {
			info, statErr := root.Lstat(rel)
			if statErr != nil {
				return statErr
			}
			if info.Mode().IsRegular() && linkCount(info) > 1 {
				opts.Log.Warn("guard_layout_hardlink_not_reowned", "path", rel)
				return nil
			}
		}
		return opts.chownBeneath(root, rel, uid, gid)
	})
}

func (o *Options) chownBeneath(root *os.Root, rel string, uid, gid int) error {
	if o.ChownBeneath != nil {
		return o.ChownBeneath(root, rel, uid, gid)
	}
	return root.Lchown(rel, uid, gid)
}

// writeFile writes text atomically. Without overwrite an existing file is kept,
// so an operator's edits survive a repeated apply.
func writeFile(path, text string, mode os.FileMode, overwrite bool) error {
	if !overwrite {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-"+filepath.Base(path)+"-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	fail := func(cause error) error {
		_ = tmp.Close()
		_ = os.Remove(name)
		return cause
	}
	if _, err := tmp.WriteString(text); err != nil {
		return fail(err)
	}
	if err := tmp.Chmod(mode); err != nil {
		return fail(err)
	}
	if err := tmp.Sync(); err != nil {
		return fail(err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Rename(name, path)
}
