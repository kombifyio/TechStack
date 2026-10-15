package drivehealth

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	ProbeScan         = "scan"
	ProbeSMART        = "smartctl"
	ProbeNVMeIdentify = "nvme_identify"
	ProbeNVMeSMART    = "nvme_smart"
	ProbeNVMeSelfTest = "nvme_selftest"
	MaxOutputBytes    = 256 << 10
	ProbeTimeout      = 5 * time.Second
	CollectionTimeout = 30 * time.Second
)

// Probe executes a fixed read command on the host, locally or through the
// admitted Guard executor. Output may accompany a collection error.
type Probe func(ctx context.Context, kind, devicePath, deviceType string) ([]byte, error)

// ProbeError carries a bounded collection reason across the Guard transport.
type ProbeError struct{ Reason string }

func (e *ProbeError) Error() string { return "drivehealth: " + e.Reason }
func ErrorReason(err error) string {
	if err == nil {
		return ""
	}
	var probeErr *ProbeError
	if errors.As(err, &probeErr) && reasonValue.MatchString(probeErr.Reason) {
		return probeErr.Reason
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return "timeout"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	if errors.Is(err, exec.ErrNotFound) {
		return "tool_missing"
	}
	return "collection_failed"
}

// ValidateProbe forbids caller-selected executables, flags, paths and writes.
func ValidateProbe(kind, path, deviceType string) error {
	if kind == ProbeScan {
		if path != "" || deviceType != "" {
			return fmt.Errorf("drivehealth: scan has no device arguments")
		}
		return nil
	}
	if kind != ProbeSMART && kind != ProbeNVMeIdentify && kind != ProbeNVMeSMART && kind != ProbeNVMeSelfTest {
		return fmt.Errorf("drivehealth: unknown probe")
	}
	if err := ValidateDevice(path, deviceType); err != nil {
		return err
	}
	if kind != ProbeSMART && (!strings.HasPrefix(path, "/dev/nvme") || deviceType != "" && deviceType != "nvme") {
		return fmt.Errorf("drivehealth: invalid NVMe probe")
	}
	return nil
}

type boundedOutput struct {
	data     bytes.Buffer
	exceeded bool
}

func (w *boundedOutput) Write(p []byte) (int, error) {
	if len(p) > MaxOutputBytes-w.data.Len() {
		w.exceeded = true
		return 0, &ProbeError{Reason: "output_limit"}
	}
	return w.data.Write(p)
}

// ExecProbe executes only inventory, identify and existing health-log reads.
// It never enables SMART, starts a self-test, formats or changes hardware.
func ExecProbe(ctx context.Context, kind, path, deviceType string) ([]byte, error) {
	if err := ValidateProbe(kind, path, deviceType); err != nil {
		return nil, err
	}
	if kind != ProbeScan {
		info, err := os.Lstat(path)
		if err != nil {
			return nil, &ProbeError{Reason: deviceError(err)}
		}
		if info.Mode()&os.ModeDevice == 0 {
			return nil, &ProbeError{Reason: "invalid_device"}
		}
	}
	name, args := "smartctl", []string{"--scan", "-j"}
	switch kind {
	case ProbeSMART:
		args = []string{"-j", "-a", "-l", "selftest"}
		if deviceType != "" {
			args = append(args, "-d", deviceType)
		}
		args = append(args, path)
	case ProbeNVMeIdentify:
		name = "nvme"
		args = []string{"id-ctrl", path, "-o", "json"}
	case ProbeNVMeSMART:
		name = "nvme"
		args = []string{"smart-log", path, "-o", "json"}
	case ProbeNVMeSelfTest:
		name = "nvme"
		args = []string{"self-test-log", path, "-o", "json"}
	}
	executable, lookupErr := trustedExecutable(name)
	if lookupErr != nil {
		if kind == ProbeScan && ErrorReason(lookupErr) == "tool_missing" {
			return sysfsInventory()
		}
		return nil, lookupErr
	}
	child, cancel := context.WithTimeout(ctx, ProbeTimeout)
	defer cancel()
	command := exec.CommandContext(child, executable, args...) // #nosec G204 -- fixed trusted executable and validated read-only argv.
	command.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "HOME=/root"}
	command.WaitDelay = time.Second
	var output, stderr boundedOutput
	command.Stdout = &output
	command.Stderr = &stderr
	err := command.Run()
	if output.exceeded || stderr.exceeded {
		return nil, &ProbeError{Reason: "output_limit"}
	}
	if child.Err() != nil {
		return nil, &ProbeError{Reason: "timeout"}
	}
	if err != nil {
		if kind == ProbeScan && errors.Is(err, exec.ErrNotFound) {
			return sysfsInventory()
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) && name == "smartctl" && exit.ExitCode()&7 == 0 {
			return output.data.Bytes(), nil
		}
		reason := ErrorReason(err)
		if errors.As(err, &exit) && name == "smartctl" {
			if exit.ExitCode()&2 != 0 {
				reason = "device_open_failed"
			} else {
				reason = "command_failed"
			}
		}
		if strings.Contains(strings.ToLower(stderr.data.String()), "permission denied") || strings.Contains(strings.ToLower(output.data.String()), "permission denied") {
			reason = "permission_denied"
		}
		return output.data.Bytes(), &ProbeError{Reason: reason}
	}
	return output.data.Bytes(), nil
}
func deviceError(err error) string {
	if errors.Is(err, os.ErrNotExist) {
		return "device_missing"
	}
	if errors.Is(err, os.ErrPermission) {
		return "permission_denied"
	}
	return "device_open_failed"
}

// sysfsInventory preserves visibility of devices when smartctl is uninstalled.
// Partitions and pseudo devices are omitted; unsupported physical/virtual disk
// health is represented by the per-device reader instead of a healthy zero.
func sysfsInventory() ([]byte, error) {
	entries, err := os.ReadDir("/sys/class/block")
	if err != nil {
		return nil, &ProbeError{Reason: "inventory_unsupported"}
	}
	type device struct {
		Name string `json:"name"`
		Type string `json:"type"`
	}
	devices := []device{}
	for _, entry := range entries {
		path := "/dev/" + entry.Name()
		if ValidateDevice(path, "") != nil {
			continue
		}
		if _, err := os.Stat(filepath.Join("/sys/class/block", entry.Name(), "partition")); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, &ProbeError{Reason: "inventory_failed"}
		}
		d := device{Name: path}
		if strings.HasPrefix(path, "/dev/nvme") {
			d.Type = "nvme"
		}
		devices = append(devices, d)
	}
	if len(devices) > MaxDrives {
		return nil, &ProbeError{Reason: "inventory_limit"}
	}
	return json.Marshal(struct {
		Devices []device `json:"devices"`
	}{devices})
}

// Collector keeps no historical measurement cache. Its only memory is identity
// metadata for explicit disappearance notices, never copied to a new device.
type Collector struct {
	Probe    Probe
	Now      func() time.Time
	mu       sync.Mutex
	previous map[string]Reading
}

func (c *Collector) Collect(ctx context.Context) (Collection, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	result := Collection{ObservedAt: now, Status: Inaccessible, Drives: []Reading{}}
	probe := c.Probe
	if probe == nil {
		probe = ExecProbe
	}
	bounded, cancel := context.WithTimeout(ctx, CollectionTimeout)
	defer cancel()
	raw, err := probe(bounded, ProbeScan, "", "")
	if err != nil {
		result.Reason = ErrorReason(err)
		if result.Reason == "tool_missing" || result.Reason == "inventory_unsupported" || result.Reason == "unsupported_platform" {
			result.Status = Unsupported
		}
		return result, err
	}
	o, err := decode(raw)
	if err != nil {
		result.Reason = "invalid_inventory"
		return result, err
	}
	if exit := uintValue(at(o, "smartctl", "exit_status")); exit != nil && *exit != 0 {
		result.Reason = "inventory_failed"
		return result, &ProbeError{Reason: result.Reason}
	}
	rows, ok := at(o, "devices").([]any)
	if !ok {
		result.Reason = "invalid_inventory"
		return result, &ProbeError{Reason: result.Reason}
	}
	if len(rows) > MaxDrives {
		result.Reason = "inventory_limit"
		return result, &ProbeError{Reason: result.Reason}
	}
	result.Status = Available
	result.Complete = true
	current := map[string]Reading{}
	paths := map[string]bool{}
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			result.Complete = false
			result.Reason = "invalid_inventory"
			continue
		}
		path, kind := textValue(m["name"]), textValue(m["type"])
		if ValidateDevice(path, kind) != nil {
			result.Complete = false
			result.Reason = "unsupported_inventory_device"
			continue
		}
		if paths[path] {
			continue
		}
		paths[path] = true
		r := c.read(bounded, probe, path, kind, now)
		if r.DriveID != "" {
			if _, duplicate := current[r.DriveID]; duplicate {
				continue
			}
			current[r.DriveID] = r
		}
		result.Drives = append(result.Drives, r)
	}
	// A complete discovery can report absent identities without reusing old
	// readings. An inaccessible path never inherits its previous drive identity.
	if result.Complete {
		for id, old := range c.previous {
			if _, found := current[id]; found {
				continue
			}
			// A path still present but unreadable cannot prove replacement/removal.
			if paths[old.DevicePath] {
				continue
			}
			if len(result.Drives) >= MaxDrives {
				result.Complete = false
				result.Reason = "inventory_limit"
				break
			}
			r := baseReading(old.DevicePath, SourceInventory, now)
			r.DriveID, r.Model, r.Serial, r.WWN, r.Protocol = id, old.Model, old.Serial, old.WWN, old.Protocol
			finish(&r)
			r.Status = Missing
			r.Reason = "device_missing"
			result.Drives = append(result.Drives, r)
		}
		if result.Complete {
			identities := map[string]Reading{}
			for id, r := range current {
				identities[id] = identityMetadata(r)
			}
			for id, old := range c.previous {
				if _, found := current[id]; found {
					continue
				}
				if paths[old.DevicePath] {
					identifiedReplacement := false
					for _, r := range current {
						if r.DevicePath == old.DevicePath {
							identifiedReplacement = true
							break
						}
					}
					if !identifiedReplacement {
						identities[id] = old
					}
				}
			}
			c.previous = identities
		}
	}
	if !result.Complete {
		result.Status = Incomplete
	}
	sort.Slice(result.Drives, func(i, j int) bool {
		if result.Drives[i].DevicePath == result.Drives[j].DevicePath {
			return result.Drives[i].DriveID < result.Drives[j].DriveID
		}
		return result.Drives[i].DevicePath < result.Drives[j].DevicePath
	})
	return result, result.Validate(now)
}

func identityMetadata(r Reading) Reading {
	return Reading{DriveID: r.DriveID, DevicePath: r.DevicePath, Model: r.Model, Serial: r.Serial, WWN: r.WWN, Protocol: r.Protocol}
}

func (c *Collector) read(ctx context.Context, probe Probe, path, kind string, now time.Time) Reading {
	raw, probeErr := probe(ctx, ProbeSMART, path, kind)
	r, parseErr := ParseSMART(raw, path, now)
	if parseErr == nil && probeErr == nil && r.Status != Unsupported {
		return r
	}
	if strings.HasPrefix(path, "/dev/nvme") && (parseErr != nil || r.Status == Unsupported || r.Status == Inaccessible) {
		identity, e1 := probe(ctx, ProbeNVMeIdentify, path, "nvme")
		health, e2 := probe(ctx, ProbeNVMeSMART, path, "nvme")
		if e1 == nil && e2 == nil {
			if nvme, err := ParseNVMe(identity, health, path, now); err == nil {
				selfTest, selfTestErr := probe(ctx, ProbeNVMeSelfTest, path, "nvme")
				if selfTestErr == nil {
					_ = ApplyNVMeSelfTest(&nvme, selfTest)
				}
				// Identity must still match after the log read: a swapped device
				// between separate commands must never own its predecessor's log.
				again, e3 := probe(ctx, ProbeNVMeIdentify, path, "nvme")
				if e3 == nil {
					if check, err := ParseNVMe(again, health, path, now); err == nil && check.DriveID != "" && check.DriveID == nvme.DriveID {
						return nvme
					}
				}
				return failedReading(path, SourceNVMe, now, "identity_changed")
			}
		}
		if probeErr == nil {
			if e1 != nil {
				probeErr = e1
			} else {
				probeErr = e2
			}
		}
	}
	if parseErr == nil {
		if probeErr != nil {
			if r.Status == Available {
				r.Status = Incomplete
			}
			r.Reason = ErrorReason(probeErr)
			if r.Reason == "permission_denied" || r.Reason == "device_open_failed" {
				r.Status = Inaccessible
			}
		}
		return r
	}
	reason := "invalid_telemetry"
	if probeErr != nil {
		reason = ErrorReason(probeErr)
	}
	return failedReading(path, SourceSMART, now, reason)
}
func failedReading(path, source string, now time.Time, reason string) Reading {
	r := baseReading(path, source, now)
	finish(&r)
	r.Status = Inaccessible
	r.Reason = reason
	if reason == "tool_missing" {
		r.Status = Unsupported
	}
	if reason == "device_missing" {
		r.Status = Missing
	}
	return r
}
