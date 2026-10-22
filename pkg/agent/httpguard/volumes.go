package httpguard

import (
	"context"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"

	"github.com/shirou/gopsutil/v4/disk"
)

const (
	// hostVolumeMinBytes is the smallest mounted filesystem reported as package
	// storage. Smaller mounts are firmware, boot, or virtual media.
	hostVolumeMinBytes = 1 << 30
	hostVolumeCap      = 8
)

// HostVolume is one mounted filesystem other than the root disk. External is
// always true for a volume that survives sanitizing: the root disk stays on
// Host.DiskGB.
type HostVolume struct {
	MountPoint string `json:"mount_point,omitempty"`
	SizeGB     int    `json:"size_gb,omitempty"`
	FreeGB     int    `json:"free_gb,omitempty"`
	Filesystem string `json:"filesystem,omitempty"`
	Label      string `json:"label,omitempty"`
	External   bool   `json:"external,omitempty"`
}

// ObserveDataVolumes lists filesystems a customer can use for package data.
// A probe error returns no volumes and does not fail the host report. The
// root filesystem is not a volume.
func ObserveDataVolumes(ctx context.Context) []HostVolume {
	if runtime.GOOS != "linux" {
		return nil
	}
	partitions, err := disk.PartitionsWithContext(ctx, true)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var volumes []HostVolume
	for _, partition := range partitions {
		if partitionReadOnly(partition.Opts) {
			continue
		}
		mount := path.Clean(strings.TrimSpace(partition.Mountpoint))
		if seen[mount] || skippedDataMount(mount) {
			continue
		}
		usage, usageErr := disk.UsageWithContext(ctx, partition.Mountpoint)
		if usageErr != nil || usage == nil || usage.Total < hostVolumeMinBytes {
			continue
		}
		seen[mount] = true
		freeGB := int(usage.Free / hostVolumeMinBytes)
		if usage.Free < hostVolumeMinBytes {
			// Measured free space below 1 GiB is not an unknown zero.
			freeGB = 1
		}
		volumes = append(volumes, HostVolume{
			MountPoint: mount,
			SizeGB:     int(usage.Total / hostVolumeMinBytes),
			FreeGB:     freeGB,
			Filesystem: strings.ToLower(strings.TrimSpace(partition.Fstype)),
			Label:      volumeLabel(partition.Device),
			External:   true,
		})
	}
	return SanitizeHostVolumes(volumes)
}

// SanitizeHostVolumes keeps mounts a control plane may store. It drops the
// root disk, virtual filesystems, read-only media, and paths that are not a
// customer data volume. At most eight volumes remain, ordered by mount.
func SanitizeHostVolumes(volumes []HostVolume) []HostVolume {
	if len(volumes) == 0 {
		return nil
	}
	clean := make([]HostVolume, 0, len(volumes))
	seen := map[string]bool{}
	for _, volume := range volumes {
		next, ok := sanitizeHostVolume(volume)
		if !ok || seen[next.MountPoint] {
			continue
		}
		seen[next.MountPoint] = true
		clean = append(clean, next)
	}
	sort.Slice(clean, func(i, j int) bool { return clean[i].MountPoint < clean[j].MountPoint })
	if len(clean) > hostVolumeCap {
		clean = clean[:hostVolumeCap]
	}
	if len(clean) == 0 {
		return nil
	}
	return clean
}

func sanitizeHostVolume(volume HostVolume) (HostVolume, bool) {
	if !volume.External || volume.SizeGB < 1 || volume.FreeGB < 0 {
		return HostVolume{}, false
	}
	raw := strings.TrimSpace(volume.MountPoint)
	if strings.Contains(raw, "..") {
		return HostVolume{}, false
	}
	mount := path.Clean(raw)
	if skippedDataMount(mount) {
		return HostVolume{}, false
	}
	switch strings.ToLower(strings.TrimSpace(volume.Filesystem)) {
	case "ext4", "xfs", "btrfs":
	default:
		return HostVolume{}, false
	}
	label := sanitizeVolumeLabel(volume.Label)
	return HostVolume{
		MountPoint: mount,
		SizeGB:     volume.SizeGB,
		FreeGB:     volume.FreeGB,
		Filesystem: strings.ToLower(strings.TrimSpace(volume.Filesystem)),
		Label:      label,
		External:   true,
	}, true
}

func skippedDataMount(mount string) bool {
	if mount == "." || mount == "/" || !strings.HasPrefix(mount, "/") || strings.Contains(mount, "..") {
		return true
	}
	if mount == "/boot" || strings.HasPrefix(mount, "/boot/") || mount == "/sysroot" || strings.HasPrefix(mount, "/sysroot/") {
		return true
	}
	for _, prefix := range []string{
		"/snap", "/proc", "/sys", "/dev", "/run",
		"/var/lib/docker", "/var/lib/containerd", "/var/lib/containers",
	} {
		if mount == prefix || strings.HasPrefix(mount, prefix+"/") {
			return true
		}
	}
	return false
}

func partitionReadOnly(opts []string) bool {
	for _, opt := range opts {
		for _, token := range strings.Split(opt, ",") {
			if strings.TrimSpace(token) == "ro" {
				return true
			}
		}
	}
	return false
}

func volumeLabel(device string) string {
	device = strings.TrimSpace(device)
	if device == "" {
		return ""
	}
	target, err := filepath.EvalSymlinks(device)
	if err != nil {
		target = device
	}
	entries, err := os.ReadDir("/dev/disk/by-label")
	if err != nil {
		return ""
	}
	for _, entry := range entries {
		resolved, linkErr := filepath.EvalSymlinks("/dev/disk/by-label/" + entry.Name())
		if linkErr != nil || resolved != target {
			continue
		}
		return entry.Name()
	}
	return ""
}

func sanitizeVolumeLabel(label string) string {
	label = strings.TrimSpace(label)
	if label == "" || len(label) > 64 {
		return ""
	}
	for _, char := range label {
		switch {
		case char >= 'a' && char <= 'z', char >= 'A' && char <= 'Z', char >= '0' && char <= '9':
		case char == '.' || char == '_' || char == '-' || char == ':' || char == '+' || char == '/':
		default:
			return ""
		}
	}
	return label
}
