package unifier

import (
	"path"
	"strings"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
)

// smallInternalDiskGB is the largest internal disk that also moves container
// images onto the external volume. An 8 GB eMMC cannot keep images beside the
// operating system once package data has moved.
const smallInternalDiskGB = 16

// PlaceExternalStorage chooses one external volume for every package when the
// internal disk is smaller than needGB. A nil result means the internal disk
// holds the data, or no reported volume is large enough. The operating system
// stays on the internal disk. needGB is the selected use cases' storage floor.
func PlaceExternalStorage(node core.EnvironmentComputeNode, needGB float64) *core.ExternalStoragePlacement {
	if needGB <= 0 || float64(node.DiskGB) >= needGB {
		return nil
	}
	volume, ok := largestFittingVolume(node.Volumes, needGB)
	if !ok {
		return nil
	}
	mount := path.Clean(volume.MountPoint)
	placement := &core.ExternalStoragePlacement{
		NodeID:       strings.TrimSpace(node.ID),
		MountPoint:   mount,
		DataRoot:     mount + "/data",
		BackupRoot:   mount + "/backups",
		StacksRoot:   mount + "/stacks",
		MediaRoot:    mount + "/media",
		VolumeSizeGB: volume.SizeGB,
	}
	if node.DiskGB > 0 && node.DiskGB <= smallInternalDiskGB {
		placement.ContainerDataRoot = mount + "/docker"
	}
	return placement
}

func largestFittingVolume(volumes []core.StorageVolume, needGB float64) (core.StorageVolume, bool) {
	var best core.StorageVolume
	found := false
	for _, volume := range volumes {
		if !usableExternalVolume(volume, needGB) {
			continue
		}
		if !found || volume.SizeGB > best.SizeGB || (volume.SizeGB == best.SizeGB && volume.MountPoint < best.MountPoint) {
			best = volume
			found = true
		}
	}
	return best, found
}

func usableExternalVolume(volume core.StorageVolume, needGB float64) bool {
	if !volume.External || volume.SizeGB <= 0 || float64(volume.SizeGB) < needGB {
		return false
	}
	// Free space below zero, or more free than the volume holds, is not an
	// unknown reading. Zero stays unknown.
	if volume.FreeGB < 0 || volume.FreeGB > volume.SizeGB || (volume.FreeGB > 0 && float64(volume.FreeGB) < needGB) {
		return false
	}
	if _, ok := safeDataMount(volume.MountPoint); !ok {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(volume.Filesystem)) {
	case "ext4", "xfs", "btrfs":
	default:
		return false
	}
	return true
}

// selectionStorageNeed is the storage floor the whole selection shares.
// One external volume has to hold every selected package, so the floor is the
// largest one.
func selectionStorageNeed(footprints []stackkitrelease.WorkloadFootprint) float64 {
	need := 0.0
	for _, footprint := range footprints {
		if footprint.HostFloor.MinStorageGB > need {
			need = footprint.HostFloor.MinStorageGB
		}
	}
	return need
}

func diskHolds(node core.EnvironmentComputeNode, goalNeed, selectionNeed float64) bool {
	if goalNeed <= 0 {
		return true
	}
	// One placement is shared, so a small goal does not fit on its own floor
	// when a selected sibling needs more than this disk or this volume holds.
	need := selectionNeed
	if goalNeed > need {
		need = goalNeed
	}
	if float64(node.DiskGB) >= need {
		return true
	}
	return PlaceExternalStorage(node, need) != nil
}

// ExternalStorageForSelection returns the shared placement for the selected
// use cases. It stays nil when any connected server's internal disk already
// holds the whole selection, or when no external volume does.
func ExternalStorageForSelection(spec *core.KombinationSpec, inventory *core.EnvironmentInventory) *core.ExternalStoragePlacement {
	if spec == nil || inventory == nil {
		return nil
	}
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		return nil
	}
	profile := metadataValue(spec, "compute_tier", "standard")
	footprints, err := selectedFootprints(catalog, selectedUseCaseRefs(spec.Metadata, nil), profile, nil, nil)
	if err != nil || len(footprints) == 0 {
		return nil
	}
	need := selectionStorageNeed(footprints)
	if need <= 0 {
		return nil
	}
	var chosen *core.ExternalStoragePlacement
	for _, node := range reportedComputeNodes(inventory) {
		if !nodeHasCPUAndRAM(node, footprints) {
			continue
		}
		if float64(node.DiskGB) >= need {
			return nil
		}
		placement := PlaceExternalStorage(node, need)
		if placement == nil {
			continue
		}
		if chosen == nil || placement.VolumeSizeGB > chosen.VolumeSizeGB {
			chosen = placement
		}
	}
	return chosen
}

func reportedComputeNodes(inventory *core.EnvironmentInventory) []core.EnvironmentComputeNode {
	if inventory == nil {
		return nil
	}
	var reported []core.EnvironmentComputeNode
	for _, node := range inventory.ComputeNodes {
		switch node.Status {
		case "pending-registration", "missing", "offline", "planned", failureStatus:
			continue
		}
		if node.CPU <= 0 || node.RAMMB <= 0 || node.DiskGB <= 0 {
			continue
		}
		reported = append(reported, node)
	}
	return reported
}

func nodeHasCPUAndRAM(node core.EnvironmentComputeNode, footprints []stackkitrelease.WorkloadFootprint) bool {
	for _, footprint := range footprints {
		if float64(node.CPU) < footprint.HostFloor.MinCPUCores || float64(node.RAMMB)/1024 < footprint.HostFloor.MinRAMGB {
			return false
		}
	}
	return true
}

func recordStoragePlacement(spec *core.KombinationSpec, placement *core.ExternalStoragePlacement) {
	if spec == nil || placement == nil {
		return
	}
	if spec.Metadata == nil {
		spec.Metadata = map[string]string{}
	}
	spec.Metadata["storage_external_mount"] = placement.MountPoint
	spec.Metadata["storage_data_root"] = placement.DataRoot
	spec.Metadata["storage_backup_root"] = placement.BackupRoot
	spec.Metadata["storage_stacks_root"] = placement.StacksRoot
	spec.Metadata["storage_media_root"] = placement.MediaRoot
	if placement.ContainerDataRoot != "" {
		spec.Metadata["storage_container_data_root"] = placement.ContainerDataRoot
	}
}

// safeDataMount accepts one absolute customer data mount. Boot, virtual and
// container-engine paths are refused before cleaning, so ".." cannot retarget
// them. The returned mount is the cleaned form.
func safeDataMount(raw string) (string, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "..") {
		return "", false
	}
	mount := path.Clean(raw)
	if mount == "." || mount == "/" || !strings.HasPrefix(mount, "/") || strings.Contains(mount, "..") {
		return "", false
	}
	if mount == "/boot" || strings.HasPrefix(mount, "/boot/") || mount == "/sysroot" || strings.HasPrefix(mount, "/sysroot/") {
		return "", false
	}
	for _, prefix := range []string{
		"/snap", "/proc", "/sys", "/dev", "/run",
		"/var/lib/docker", "/var/lib/containerd", "/var/lib/containers",
	} {
		if mount == prefix || strings.HasPrefix(mount, prefix+"/") {
			return "", false
		}
	}
	return mount, true
}

// childOfMount reports whether raw is a cleaned path strictly inside mount.
func childOfMount(mount, raw string) bool {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.Contains(raw, "..") {
		return false
	}
	root := path.Clean(raw)
	return root != mount && strings.HasPrefix(root, mount+"/")
}

func canonicalPlacement(placement core.ExternalStoragePlacement) *core.ExternalStoragePlacement {
	mount, ok := safeDataMount(placement.MountPoint)
	if !ok || !childOfMount(mount, placement.DataRoot) || !childOfMount(mount, placement.BackupRoot) || !childOfMount(mount, placement.StacksRoot) || !childOfMount(mount, placement.MediaRoot) {
		return nil
	}
	if strings.TrimSpace(placement.ContainerDataRoot) != "" && !childOfMount(mount, placement.ContainerDataRoot) {
		return nil
	}
	placement.MountPoint = mount
	placement.DataRoot = path.Clean(strings.TrimSpace(placement.DataRoot))
	placement.BackupRoot = path.Clean(strings.TrimSpace(placement.BackupRoot))
	placement.StacksRoot = path.Clean(strings.TrimSpace(placement.StacksRoot))
	placement.MediaRoot = path.Clean(strings.TrimSpace(placement.MediaRoot))
	if strings.TrimSpace(placement.ContainerDataRoot) != "" {
		placement.ContainerDataRoot = path.Clean(strings.TrimSpace(placement.ContainerDataRoot))
	}
	return &placement
}

// StoragePlacementFromMetadata reads the roots recorded on a handoff. A missing,
// partial, or unsafe set returns nil so a template keeps its own defaults.
func StoragePlacementFromMetadata(metadata map[string]string) *core.ExternalStoragePlacement {
	if len(metadata) == 0 {
		return nil
	}
	return canonicalPlacement(core.ExternalStoragePlacement{
		MountPoint:        metadata["storage_external_mount"],
		DataRoot:          metadata["storage_data_root"],
		BackupRoot:        metadata["storage_backup_root"],
		StacksRoot:        metadata["storage_stacks_root"],
		MediaRoot:         metadata["storage_media_root"],
		ContainerDataRoot: metadata["storage_container_data_root"],
	})
}

// ApplyStackSpecStorage writes the shared roots onto a StackSpec document.
// It sets only the storage intent StackKits already accepts. Container images
// move only when the document already has a top-level container intent and
// the placement names a container root, which is the small-disk case.
// #SystemIntentV2 has no container field; inventing one would make the spec
// fail closed.
func ApplyStackSpecStorage(document map[string]interface{}, placement *core.ExternalStoragePlacement) {
	if document == nil || placement == nil {
		return
	}
	placement = canonicalPlacement(*placement)
	if placement == nil {
		return
	}
	storage, _ := document["storage"].(map[string]interface{})
	if storage == nil {
		storage = map[string]interface{}{}
	}
	storage["dataRoot"] = placement.DataRoot
	storage["backupRoot"] = placement.BackupRoot
	storage["stacksRoot"] = placement.StacksRoot
	storage["mediaRoot"] = placement.MediaRoot
	external, _ := storage["external"].(map[string]interface{})
	if external == nil {
		external = map[string]interface{}{}
	}
	external["mountPoint"] = placement.MountPoint
	storage["external"] = external
	if _, ok := storage["volumeDriver"]; !ok {
		storage["volumeDriver"] = "local"
	}
	document["storage"] = storage
	if placement.ContainerDataRoot == "" {
		return
	}
	container, _ := document["container"].(map[string]interface{})
	if container == nil {
		return
	}
	container["dataRoot"] = placement.ContainerDataRoot
	document["container"] = container
}

// externalVolumeUsedGB is space already taken on a volume whose free space was
// measured. Unknown free space (zero) contributes no usage, so a missing
// reading is not treated as a full disk and root usage is not borrowed.
func externalVolumeUsedGB(volume core.StorageVolume) float64 {
	if volume.FreeGB <= 0 || volume.FreeGB > volume.SizeGB {
		return 0
	}
	return float64(volume.SizeGB - volume.FreeGB)
}

// placedDataVolume is the external volume layout capacity uses when it is
// larger than the internal disk. The operating system stays internal.
func placedDataVolume(capabilities core.WorkerCapabilities) (core.StorageVolume, bool) {
	volume, ok := largestFittingVolume(capabilities.Volumes, 0)
	if !ok || float64(volume.SizeGB) <= float64(capabilities.Disk) {
		return core.StorageVolume{}, false
	}
	return volume, true
}

// dataStorageGB is the space the layout planner can give package data:
// the internal disk, or a larger external volume when one is mounted.
func dataStorageGB(capabilities core.WorkerCapabilities) float64 {
	if volume, ok := placedDataVolume(capabilities); ok {
		return float64(volume.SizeGB)
	}
	return float64(capabilities.Disk)
}
