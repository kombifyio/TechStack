package unifier

import (
	"math"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/internal/stackkitrelease"
	"github.com/kombifyio/techstack/pkg/core"
)

func TestExternalStoragePlacesASmallDiskOntoTheConnectedVolume(t *testing.T) {
	need, footprints := photosStorageFloor(t)
	if need <= 8 {
		t.Fatalf("photos storage floor = %v, want a floor above an 8 GB internal disk", need)
	}
	spec := photosSpec()
	node := smallNode(footprints)
	node.DiskGB = 8
	node.Volumes = []core.StorageVolume{fleetVolume(int(math.Ceil(need)))}
	inventory := &core.EnvironmentInventory{ComputeNodes: []core.EnvironmentComputeNode{node}}

	for _, gap := range capacityGaps(spec, inventory) {
		if gap.Code == "use_case_host_floor_unmet" || gap.Code == "external_storage_required" {
			t.Fatalf("connected volume should cover photos: %+v", gap)
		}
	}
	trace := BuildDecisionTrace(spec, &core.DecisionContext{Environment: inventory}, nil, nil)
	placement := trace.StoragePlacement
	if placement == nil || placement.MountPoint != "/mnt/fleet-usb" || placement.DataRoot != "/mnt/fleet-usb/data" || placement.BackupRoot != "/mnt/fleet-usb/backups" || placement.StacksRoot != "/mnt/fleet-usb/stacks" || placement.MediaRoot != "/mnt/fleet-usb/media" {
		t.Fatalf("placement = %+v", placement)
	}
	if placement.ContainerDataRoot != "/mnt/fleet-usb/docker" {
		t.Fatalf("container root = %q, want images on the volume for an 8 GB disk", placement.ContainerDataRoot)
	}
	if spec.Metadata["storage_data_root"] != placement.DataRoot || spec.Metadata["storage_container_data_root"] != placement.ContainerDataRoot {
		t.Fatalf("metadata = %#v", spec.Metadata)
	}
	if !hasDecisionCode(trace.Reasons, "external_data_placement") || !hasGuidanceCode(trace.Guidance, "external_data_placement") {
		t.Fatalf("decision did not explain the placement: reasons=%+v guidance=%+v", trace.Reasons, trace.Guidance)
	}
}

func TestExternalStorageGapCitesTheCapableDisk(t *testing.T) {
	need, footprints := photosStorageFloor(t)
	spec := photosSpec()
	capable := smallNode(footprints)
	capable.DiskGB = 8
	incapable := core.EnvironmentComputeNode{ID: "big", Status: "online", CPU: capable.CPU, RAMMB: 512, DiskGB: 64}
	inventory := &core.EnvironmentInventory{ComputeNodes: []core.EnvironmentComputeNode{capable, incapable}}
	var message string
	for _, gap := range capacityGaps(spec, inventory) {
		if gap.Code == "external_storage_required" {
			message = gap.Message
		}
		if gap.Code == "use_case_host_floor_unmet" {
			t.Fatalf("disk shortfall was reported as a host floor: %+v", gap)
		}
	}
	if message == "" || !strings.Contains(message, "has 8 GB") || strings.Contains(message, "has 64 GB") {
		t.Fatalf("gap = %q, want the 8 GB disk that can run photos (floor %v)", message, need)
	}
}

func TestExternalStorageKeepsDataInternalOnALargeDisk(t *testing.T) {
	need, footprints := photosStorageFloor(t)
	spec := photosSpec()
	node := smallNode(footprints)
	node.DiskGB = int(math.Ceil(need))
	node.Volumes = []core.StorageVolume{fleetVolume(node.DiskGB + 10)}
	inventory := &core.EnvironmentInventory{ComputeNodes: []core.EnvironmentComputeNode{node}}
	trace := BuildDecisionTrace(spec, &core.DecisionContext{Environment: inventory}, nil, nil)
	if trace.StoragePlacement != nil || spec.Metadata["storage_data_root"] != "" {
		t.Fatalf("internal disk holds photos, placement = %+v metadata = %#v", trace.StoragePlacement, spec.Metadata)
	}
}

func TestExternalStorageAsksForADriveWithoutFormatting(t *testing.T) {
	need, footprints := photosStorageFloor(t)
	spec := photosSpec()
	node := smallNode(footprints)
	node.DiskGB = 8
	inventory := &core.EnvironmentInventory{ComputeNodes: []core.EnvironmentComputeNode{node}}
	var asked *core.EnvironmentGap
	for _, gap := range capacityGaps(spec, inventory) {
		switch gap.Code {
		case "external_storage_required":
			gapCopy := gap
			asked = &gapCopy
		case "use_case_host_floor_unmet":
			t.Fatalf("disk-only miss became a compute gap: %s", gap.Message)
		}
	}
	if asked == nil || asked.Blocking {
		t.Fatalf("gap = %+v, want a non-blocking external_storage_required", asked)
	}
	node.Volumes = []core.StorageVolume{fleetVolume(int(math.Ceil(need)) - 1)}
	asked = nil
	for _, gap := range capacityGaps(spec, &core.EnvironmentInventory{ComputeNodes: []core.EnvironmentComputeNode{node}}) {
		if gap.Code == "external_storage_required" {
			gapCopy := gap
			asked = &gapCopy
		}
	}
	if asked == nil || asked.Blocking {
		t.Fatalf("too-small drive gap = %+v, want a non-blocking external_storage_required", asked)
	}
}

func TestExternalStorageKeepsAComputeShortfall(t *testing.T) {
	_, footprints := photosStorageFloor(t)
	spec := photosSpec()
	node := smallNode(footprints)
	node.RAMMB = 1024
	node.DiskGB = 8
	node.Volumes = []core.StorageVolume{fleetVolume(64)}
	for _, gap := range capacityGaps(spec, &core.EnvironmentInventory{ComputeNodes: []core.EnvironmentComputeNode{node}}) {
		if gap.Code == "external_storage_required" {
			t.Fatalf("RAM shortfall was reported as a disk: %s", gap.Message)
		}
		if gap.Code == "use_case_host_floor_unmet" {
			return
		}
	}
	t.Fatal("expected use_case_host_floor_unmet")
}

func TestExternalVolumeMustHoldTheWholeSelection(t *testing.T) {
	node := core.EnvironmentComputeNode{
		DiskGB:  8,
		Volumes: []core.StorageVolume{fleetVolume(20)},
	}
	if diskHolds(node, 10, 40) {
		t.Fatal("a 20 GB volume counted for a 40 GB selection")
	}
	if !diskHolds(node, 10, 20) {
		t.Fatal("a 20 GB volume did not cover a 20 GB selection")
	}
}

func TestPlaceExternalStorageRejectsUnsafeVolumes(t *testing.T) {
	node := core.EnvironmentComputeNode{DiskGB: 8}
	for _, volume := range []core.StorageVolume{
		{MountPoint: "/", SizeGB: 64, FreeGB: 64, Filesystem: "ext4", External: true},
		{MountPoint: "/mnt/../etc", SizeGB: 64, FreeGB: 64, Filesystem: "ext4", External: true},
		{MountPoint: "mnt/fleet-usb", SizeGB: 64, FreeGB: 64, Filesystem: "ext4", External: true},
		{MountPoint: "/mnt/fleet-usb", SizeGB: 64, FreeGB: 64, Filesystem: "ntfs", External: true},
		{MountPoint: "/mnt/fleet-usb", SizeGB: 64, FreeGB: 1, Filesystem: "ext4", External: true},
		{MountPoint: "/mnt/fleet-usb", SizeGB: 64, FreeGB: -1, Filesystem: "ext4", External: true},
		{MountPoint: "/mnt/fleet-usb", SizeGB: 64, FreeGB: 64, Filesystem: "ext4", External: false},
		{MountPoint: "/boot", SizeGB: 64, FreeGB: 64, Filesystem: "ext4", External: true},
		{MountPoint: "/var/lib/docker", SizeGB: 64, FreeGB: 64, Filesystem: "ext4", External: true},
	} {
		node.Volumes = []core.StorageVolume{volume}
		if placement := PlaceExternalStorage(node, 20); placement != nil {
			t.Fatalf("accepted %+v as %+v", volume, placement)
		}
	}
}

func TestFootprintFitsUsesExternalSpaceWithoutRootUsage(t *testing.T) {
	need, footprints := photosStorageFloor(t)
	if len(footprints) == 0 {
		t.Fatal("photos has no footprint")
	}
	size := int(math.Ceil(need))
	worker := core.Worker{
		Capabilities: core.WorkerCapabilities{
			CPU: 8, RAM: 16384, Disk: 8,
			Volumes: []core.StorageVolume{fleetVolume(size)},
		},
		Usage: core.WorkerUsage{Disk: size + 1},
	}
	shortfall, _ := footprintFits(footprints[0], worker, stackkitrelease.UseCaseResources{}, need)
	if containsString(shortfall, "storage") {
		t.Fatalf("root usage was subtracted from the external volume: %v", shortfall)
	}
	worker.Capabilities.Volumes = nil
	shortfall, _ = footprintFits(footprints[0], worker, stackkitrelease.UseCaseResources{}, need)
	if !containsString(shortfall, "storage") {
		t.Fatalf("8 GB internal disk reported as enough: %v", shortfall)
	}
}

func TestFootprintFitsCountsMeasuredExternalFreeSpace(t *testing.T) {
	need, footprints := photosStorageFloor(t)
	if len(footprints) == 0 {
		t.Fatal("photos has no footprint")
	}
	size := int(math.Ceil(need)) + 40
	free := int(math.Ceil(need))
	worker := core.Worker{
		Capabilities: core.WorkerCapabilities{
			CPU: 8, RAM: 16384, Disk: 8,
			Volumes: []core.StorageVolume{{
				MountPoint: "/mnt/fleet-usb", SizeGB: size, FreeGB: free, Filesystem: "ext4", External: true,
			}},
		},
		Usage: core.WorkerUsage{Disk: size},
	}
	shortfall, _ := footprintFits(footprints[0], worker, stackkitrelease.UseCaseResources{StorageGB: float64(free) + 1}, need)
	if !containsString(shortfall, "storage") {
		t.Fatalf("volume size hid the free-space shortfall: %v", shortfall)
	}
	shortfall, _ = footprintFits(footprints[0], worker, stackkitrelease.UseCaseResources{}, need)
	if containsString(shortfall, "storage") {
		t.Fatalf("free space at the storage floor was rejected: %v", shortfall)
	}
}

func TestApplyStackSpecStorageWritesSharedRootsOnly(t *testing.T) {
	placement := &core.ExternalStoragePlacement{
		MountPoint: "/mnt/fleet-usb", DataRoot: "/mnt/fleet-usb/data", BackupRoot: "/mnt/fleet-usb/backups",
		StacksRoot: "/mnt/fleet-usb/stacks", MediaRoot: "/mnt/fleet-usb/media", ContainerDataRoot: "/mnt/fleet-usb/docker",
	}
	document := map[string]interface{}{"storage": map[string]interface{}{"dataRoot": "/opt/data"}}
	ApplyStackSpecStorage(document, placement)
	storage := document["storage"].(map[string]interface{})
	if storage["dataRoot"] != placement.DataRoot || storage["mediaRoot"] != placement.MediaRoot || storage["volumeDriver"] != "local" {
		t.Fatalf("storage = %#v", storage)
	}
	external, _ := storage["external"].(map[string]interface{})
	if external["mountPoint"] != placement.MountPoint {
		t.Fatalf("external = %#v", external)
	}
	if _, ok := document["system"]; ok {
		t.Fatal("system.container was invented; container images live on the top-level container intent")
	}
	if _, ok := document["container"]; ok {
		t.Fatal("container intent was invented on a document that had none")
	}
	if got := StoragePlacementFromMetadata(map[string]string{"storage_data_root": placement.DataRoot}); got != nil {
		t.Fatal("partial metadata produced a placement")
	}
	escaped := map[string]string{
		"storage_external_mount": "/mnt/../etc",
		"storage_data_root":      "/etc/data",
		"storage_backup_root":    "/etc/backups",
		"storage_stacks_root":    "/etc/stacks",
		"storage_media_root":     "/etc/media",
	}
	if got := StoragePlacementFromMetadata(escaped); got != nil {
		t.Fatalf("escaped metadata produced %+v", got)
	}
	unsafe := *placement
	unsafe.MountPoint = "/boot"
	ApplyStackSpecStorage(document, &unsafe)
	if storage["dataRoot"] != placement.DataRoot {
		t.Fatalf("unsafe placement changed storage: %#v", storage)
	}
	if external["mountPoint"] != placement.MountPoint {
		t.Fatalf("unsafe placement changed the mount: %#v", external)
	}
	document["container"] = map[string]interface{}{"dataRoot": "/var/lib/docker", "engine": "docker"}
	ApplyStackSpecStorage(document, placement)
	container := document["container"].(map[string]interface{})
	if container["dataRoot"] != placement.ContainerDataRoot || container["engine"] != "docker" {
		t.Fatalf("container = %#v", container)
	}
	if _, ok := document["system"]; ok {
		t.Fatal("system.container was written")
	}
}

func TestImagesStayInternalAboveTheSmallDiskBound(t *testing.T) {
	need, _ := photosStorageFloor(t)
	if need <= float64(smallInternalDiskGB) {
		t.Skip("storage floor is inside the image-move bound")
	}
	node := core.EnvironmentComputeNode{DiskGB: smallInternalDiskGB + 1, Volumes: []core.StorageVolume{fleetVolume(int(math.Ceil(need)) + 1)}}
	if float64(node.DiskGB) >= need {
		t.Skip("internal disk already holds the floor")
	}
	placement := PlaceExternalStorage(node, need)
	if placement == nil || placement.DataRoot != "/mnt/fleet-usb/data" || placement.ContainerDataRoot != "" {
		t.Fatalf("placement = %+v", placement)
	}
}

func photosSpec() *core.KombinationSpec {
	return &core.KombinationSpec{Metadata: map[string]string{"use_cases": "photos", "compute_tier": "standard"}}
}

func photosStorageFloor(t *testing.T) (float64, []stackkitrelease.WorkloadFootprint) {
	t.Helper()
	catalog, err := stackkitrelease.ResolveUseCaseCatalog()
	if err != nil {
		t.Fatalf("catalog: %v", err)
	}
	footprints, err := selectedFootprints(catalog, []string{"photos"}, "standard", nil, nil)
	if err != nil || len(footprints) == 0 {
		t.Fatalf("photos footprints: %v %+v", err, footprints)
	}
	return selectionStorageNeed(footprints), footprints
}

func smallNode(footprints []stackkitrelease.WorkloadFootprint) core.EnvironmentComputeNode {
	node := core.EnvironmentComputeNode{ID: "igel", Status: "online", CPU: 1, RAMMB: 512, DiskGB: 8}
	for _, footprint := range footprints {
		if cores := int(math.Ceil(footprint.HostFloor.MinCPUCores)); cores > node.CPU {
			node.CPU = cores
		}
		if ram := int(math.Ceil(footprint.HostFloor.MinRAMGB * 1024)); ram > node.RAMMB {
			node.RAMMB = ram
		}
	}
	return node
}

func fleetVolume(sizeGB int) core.StorageVolume {
	return core.StorageVolume{MountPoint: "/mnt/fleet-usb", SizeGB: sizeGB, FreeGB: sizeGB, Filesystem: "ext4", Label: "fleet-usb", External: true}
}

func hasDecisionCode(reasons []core.DecisionReason, code string) bool {
	for _, reason := range reasons {
		if reason.Code == code {
			return true
		}
	}
	return false
}

func hasGuidanceCode(guidance []core.DecisionGuidance, code string) bool {
	for _, item := range guidance {
		if item.Code == code {
			return true
		}
	}
	return false
}
