package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Profile struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Architecture string `json:"architecture"`
	MinCPU       int    `json:"min_cpu"`
	MinMemoryMiB int    `json:"min_memory_mib"`
	MinDiskGiB   int    `json:"min_disk_gib"`
	Appliance    bool   `json:"appliance"`
	CloudInit    bool   `json:"cloud_init"`
}

func Profiles() []Profile {
	return []Profile{
		{ID: "ubuntu-24.04", Title: "Ubuntu 24.04 LTS", Architecture: "amd64", MinCPU: 2, MinMemoryMiB: 2048, MinDiskGiB: 32, CloudInit: true},
		{ID: "haos", Title: "Home Assistant OS", Architecture: "amd64", MinCPU: 2, MinMemoryMiB: 4096, MinDiskGiB: 32, Appliance: true},
	}
}

// DefaultImage pins the vendor artifacts verified on 2026-09-10. Ubuntu's pin
// comes from release-20260826/SHA256SUMS; HAOS's from the 18.2 release asset
// digest. Updating these defaults never updates an existing customer's VM.
func DefaultImage(profileID string) (Image, error) {
	switch profileID {
	case "ubuntu-24.04":
		return Image{ProfileID: profileID, Version: "20260826", URL: "https://cloud-images.ubuntu.com/releases/noble/release-20260826/ubuntu-24.04-server-cloudimg-amd64.img", SHA256: "d0fe84bb5f80853425fa6be28e2c106f30104c3cfe8611933f2e65c9b63f0e30"}, nil
	case "haos":
		return Image{ProfileID: profileID, Version: "18.2", URL: "https://github.com/home-assistant/operating-system/releases/download/18.2/haos_ova-18.2.qcow2.xz", SHA256: "254e53f354df0739e3afc09be5431a07df53f0df6b703885404f665c454f254e", Compression: "xz"}, nil
	default:
		return Image{}, ErrCapability
	}
}

func profileByID(id string) (Profile, error) {
	for _, profile := range Profiles() {
		if profile.ID == id {
			return profile, nil
		}
	}
	return Profile{}, errors.New("substrate: unsupported guest profile")
}

// Image is an explicitly pinned release artifact selected by the product
// catalog. A moving latest URL or an unverified pre-existing disk is not a pin.
type Image struct {
	ProfileID   string `json:"profile_id"`
	Version     string `json:"version"`
	URL         string `json:"url"`
	SHA256      string `json:"sha256"`
	Compression string `json:"compression,omitempty"`
}

func (i Image) Validate() error {
	if _, err := profileByID(i.ProfileID); err != nil {
		return err
	}
	if i.Version == "" || len(i.Version) > 63 || strings.ContainsAny(i.Version, "/\\ \r\n") || strings.Contains(strings.ToLower(i.Version), "latest") {
		return errors.New("substrate: immutable image version required")
	}
	hash, err := hex.DecodeString(i.SHA256)
	if err != nil || len(hash) != 32 || strings.ToLower(i.SHA256) != i.SHA256 {
		return errors.New("substrate: SHA256 image pin required")
	}
	u, err := url.Parse(i.URL)
	if err != nil || u.Scheme != "https" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Port() != "" {
		return errors.New("substrate: official HTTPS image required")
	}
	if i.ProfileID == "ubuntu-24.04" {
		if u.Host != "cloud-images.ubuntu.com" || !strings.HasPrefix(u.Path, "/releases/noble/release-"+i.Version+"/") || !strings.HasSuffix(u.Path, "/ubuntu-24.04-server-cloudimg-amd64.img") || i.Compression != "" {
			return errors.New("substrate: pinned Ubuntu 24.04 amd64 cloud image required")
		}
	} else if u.Host != "github.com" || u.Path != "/home-assistant/operating-system/releases/download/"+i.Version+"/haos_ova-"+i.Version+".qcow2.xz" || i.Compression != "xz" {
		return errors.New("substrate: official versioned HAOS KVM image required")
	}
	return nil
}

func (i Image) Filename() string { return "kombify-" + i.ProfileID + "-" + i.SHA256 + ".qcow2" }

type ImageImport struct {
	Image   Image  `json:"image"`
	Storage string `json:"storage"`
	TaskRef string `json:"task_ref"`
}

func (c *Client) SubmitImageImport(ctx context.Context, storage string, image Image) (ImageImport, error) {
	if err := image.Validate(); err != nil {
		return ImageImport{}, err
	}
	if !identifier.MatchString(storage) {
		return ImageImport{}, ErrCapability
	}
	inventory, err := c.Inventory(ctx)
	if err != nil {
		return ImageImport{}, err
	}
	found := false
	for _, s := range inventory.Storage {
		if s.ID == storage && s.Active == 1 && stringsContainsList(s.Content, "import") && (image.Compression == "" || stringsContainsList(s.Content, "vztmpl")) {
			found = true
		}
	}
	if !found {
		return ImageImport{}, errors.New("substrate: active import-capable storage required")
	}
	filename := downloadFilename(storage, image)
	// The native worker ID is the immutable filename. A lost HTTP result can
	// therefore recover the original UPID instead of starting a second download.
	var tasks []struct {
		UPID string `json:"upid"`
		Type string `json:"type"`
		ID   string `json:"id"`
		User string `json:"user"`
	}
	token, err := c.credential()
	if err != nil {
		return ImageImport{}, err
	}
	user := strings.SplitN(token, "=", 2)[0]
	if err = c.request(ctx, http.MethodGet, c.nodePath()+"/tasks?source=all&typefilter=download&limit=500", nil, &tasks); err != nil {
		return ImageImport{}, err
	}
	for _, task := range tasks {
		if task.Type == "download" && task.ID == filename && task.User == user {
			return ImageImport{Image: image, Storage: storage, TaskRef: task.UPID}, nil
		}
	}
	content := "import"
	if image.Compression != "" {
		content = "vztmpl"
	}
	values := url.Values{"content": {content}, "filename": {filename}, "url": {image.URL}, "checksum-algorithm": {"sha256"}, "checksum": {image.SHA256}, "verify-certificates": {"1"}}

	submission, err := c.submit(ctx, http.MethodPost, c.nodePath()+"/storage/"+storage+"/download-url", values)
	if err != nil {
		return ImageImport{}, err
	}
	return ImageImport{Image: image, Storage: storage, TaskRef: submission.TaskRef}, nil
}

type Storage struct {
	ID        string `json:"storage"`
	Content   string `json:"content"`
	Active    int    `json:"active"`
	Available uint64 `json:"avail"`
}
type Network struct {
	Name   string `json:"iface"`
	Type   string `json:"type"`
	Active int    `json:"active"`
}
type Inventory struct {
	Version                string    `json:"version"`
	Architecture           string    `json:"architecture"`
	HardwareVirtualization bool      `json:"hardware_virtualization"`
	CPU                    int       `json:"cpu"`
	MemoryAvailable        uint64    `json:"memory_available"`
	Storage                []Storage `json:"storage"`
	Networks               []Network `json:"networks"`
	Guests                 []Guest   `json:"guests"`
}

func (c *Client) Inventory(ctx context.Context) (Inventory, error) {
	var inventory Inventory
	var version struct {
		Version string `json:"version"`
	}
	if err := c.request(ctx, http.MethodGet, "/version", nil, &version); err != nil {
		return inventory, err
	}
	inventory.Version = version.Version
	var status struct {
		Kernel struct {
			Machine string `json:"machine"`
		} `json:"current-kernel"`
		CPUInfo struct {
			CPUs int             `json:"cpus"`
			HVM  json.RawMessage `json:"hvm"`
		} `json:"cpuinfo"`
		Memory struct {
			Available uint64 `json:"available"`
			Free      uint64 `json:"free"`
		} `json:"memory"`
	}
	if err := c.request(ctx, http.MethodGet, c.nodePath()+"/status", nil, &status); err != nil {
		return inventory, err
	}
	inventory.CPU = status.CPUInfo.CPUs
	if status.Kernel.Machine == "x86_64" {
		inventory.Architecture = "amd64"
	} else {
		inventory.Architecture = status.Kernel.Machine
	}
	// PVE::ProcFSTools derives hvm from the Intel VMX / AMD SVM flags.
	inventory.HardwareVirtualization = string(status.CPUInfo.HVM) == "1" || string(status.CPUInfo.HVM) == "true" || string(status.CPUInfo.HVM) == `"1"`
	inventory.MemoryAvailable = status.Memory.Available
	if inventory.MemoryAvailable == 0 {
		inventory.MemoryAvailable = status.Memory.Free
	}
	if err := c.request(ctx, http.MethodGet, c.nodePath()+"/storage", nil, &inventory.Storage); err != nil {
		return inventory, err
	}
	if err := c.request(ctx, http.MethodGet, c.nodePath()+"/network", nil, &inventory.Networks); err != nil {
		return inventory, err
	}
	if err := c.request(ctx, http.MethodGet, c.nodePath()+"/qemu", nil, &inventory.Guests); err != nil {
		return inventory, err
	}
	return inventory, nil
}

type GuestSpec struct {
	GuestIdentity
	ProfileID    string      `json:"profile_id"`
	CPU          int         `json:"cpu"`
	MemoryMiB    int         `json:"memory_mib"`
	DiskGiB      int         `json:"disk_gib"`
	Storage      string      `json:"storage"`
	Bridge       string      `json:"bridge"`
	Isolated     bool        `json:"isolated"`
	ImageImport  ImageImport `json:"image_import"`
	Enrollment   bool        `json:"enrollment,omitempty"`
	SSHPublicKey string      `json:"ssh_public_key,omitempty"`
}

// CreationDigest retains image, ownership and resource identity while excluding
// the native download task handle, which is transport evidence rather than intent.
func (s GuestSpec) CreationDigest() string {
	s.ImageImport.TaskRef = ""
	encoded, _ := json.Marshal(s)
	digest := sha256.Sum256(encoded)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func (c *Client) validateSpec(s GuestSpec) (Profile, error) {
	if err := c.validateIdentity(s.GuestIdentity); err != nil {
		return Profile{}, err
	}
	p, err := profileByID(s.ProfileID)
	if err != nil {
		return p, err
	}
	if s.CPU < p.MinCPU || s.CPU > 256 || s.MemoryMiB < p.MinMemoryMiB || s.MemoryMiB > 1048576 || s.DiskGiB < p.MinDiskGiB || s.DiskGiB > 65536 {
		return p, errors.New("substrate: guest resources outside profile limits")
	}
	if !identifier.MatchString(s.Storage) || !identifier.MatchString(s.Bridge) || !identifier.MatchString(s.ImageImport.Storage) {
		return p, ErrCapability
	}
	if err := s.ImageImport.Image.Validate(); err != nil {
		return p, err
	}
	if s.ImageImport.Image.ProfileID != s.ProfileID {
		return p, errors.New("substrate: image/profile mismatch")
	}
	if p.CloudInit {
		if (s.SSHPublicKey != "" || !s.Enrollment) && (len(s.SSHPublicKey) > 16384 || strings.ContainsAny(s.SSHPublicKey, "\r\n") || (!strings.HasPrefix(s.SSHPublicKey, "ssh-ed25519 ") && !strings.HasPrefix(s.SSHPublicKey, "ssh-rsa "))) {
			return p, errors.New("substrate: Ubuntu requires a public enrollment SSH key")
		}
	} else if s.SSHPublicKey != "" || s.Enrollment {
		return p, errors.New("substrate: appliance does not accept cloud-init credentials")
	}
	return p, nil
}

// SubmitCreate uses a client-selected reserved VM id and persists the exact
// request hash with ownership tags in Proxmox itself. Repeating a request after
// loss of its HTTP result recovers that VM; it never chooses a second id.
func (c *Client) SubmitCreate(ctx context.Context, spec GuestSpec, bootstrap ...[]byte) (Submission, error) {
	profile, err := c.validateSpec(spec)
	if err != nil {
		return Submission{}, err
	}
	guest, err := c.Guest(ctx, spec.ID)
	if err == nil {
		if !owns(spec.GuestIdentity, guest) || guest.Config["description"] != "kombify-spec-"+spec.CreationDigest() {
			return Submission{}, ErrConflict
		}
		return Submission{Recovered: true, GuestID: spec.ID}, nil
	}
	if !errors.Is(err, ErrNotFound) {
		return Submission{}, err
	}
	task, err := c.ObserveTask(ctx, spec.ImageImport.TaskRef)
	if err != nil {
		return Submission{}, err
	}
	if !task.Succeeded() {
		return Submission{}, errors.New("substrate: checksum-verified image import has not completed")
	}
	if spec.ImageImport.Image.Compression != "" {
		if err := c.materializeCompressedImage(ctx, spec.ImageImport); err != nil {
			return Submission{}, err
		}
	}
	inventory, err := c.Inventory(ctx)
	if err != nil {
		return Submission{}, err
	}
	if inventory.CPU < spec.CPU || inventory.MemoryAvailable < uint64(spec.MemoryMiB)*1024*1024 {
		return Submission{}, errors.New("substrate: insufficient host CPU or available memory")
	}
	if inventory.Architecture != profile.Architecture || !inventory.HardwareVirtualization {
		return Submission{}, ErrCapability
	}
	storageOK, bridgeOK := false, false
	for _, s := range inventory.Storage {
		if s.ID == spec.Storage && s.Active == 1 && stringsContainsList(s.Content, "images") && s.Available >= uint64(spec.DiskGiB)*1024*1024*1024 {
			storageOK = true
		}
	}
	for _, n := range inventory.Networks {
		if n.Name == spec.Bridge && n.Type == "bridge" && n.Active == 1 {
			bridgeOK = true
		}
	}
	if !storageOK || !bridgeOK {
		return Submission{}, errors.New("substrate: active bridge and sufficient VM storage required")
	}
	net0 := "virtio,bridge=" + spec.Bridge + ",firewall=1"
	if spec.Isolated {
		net0 += ",link_down=1"
	}
	values := url.Values{"vmid": {strconv.Itoa(spec.ID)}, "name": {spec.Name}, "tags": {"kombify-managed;" + spec.OperationTag}, "description": {"kombify-spec-" + spec.CreationDigest()}, "ostype": {"l26"}, "cpu": {"host"}, "cores": {strconv.Itoa(spec.CPU)}, "memory": {strconv.Itoa(spec.MemoryMiB)}, "scsihw": {"virtio-scsi-single"}, "scsi0": {spec.Storage + ":0,import-from=" + spec.ImageImport.Storage + ":import/" + spec.ImageImport.Image.Filename() + ",discard=on"}, "net0": {net0}, "agent": {"enabled=1"}, "serial0": {"socket"}, "bios": {"ovmf"}, "machine": {"q35"}, "efidisk0": {spec.Storage + ":0,efitype=4m,pre-enrolled-keys=0"}, "boot": {"order=scsi0"}, "start": {"0"}, "onboot": {"0"}}
	if profile.CloudInit {
		snippet, err := c.prepareUbuntuCloudInit(ctx, spec, bootstrap...)
		if err != nil {
			return Submission{}, err
		}
		values.Set("cicustom", "user="+snippet)
		values.Set("ide2", spec.Storage+":cloudinit")
		values.Set("ciuser", "kombify")
		values.Set("sshkeys", url.QueryEscape(spec.SSHPublicKey))
		values.Set("ipconfig0", "ip=dhcp")
	}
	submission, err := c.submit(ctx, http.MethodPost, c.nodePath()+"/qemu", values)
	submission.GuestID = spec.ID
	return submission, err
}

// ResizeGuestDisk grows the imported disk before first boot. It cannot shrink
// disks and is safe to repeat after an uncertain response.
func (c *Client) ResizeGuestDisk(ctx context.Context, identity GuestIdentity, diskGiB int) error {
	guest, err := c.ownedGuest(ctx, identity)
	if err != nil {
		return err
	}
	if guest.Status != "stopped" || guest.Lock != "" || diskGiB < 32 || diskGiB > 65536 {
		return ErrConflict
	}
	for _, part := range strings.Split(stringConfig(guest.Config, "scsi0"), ",") {
		if strings.HasPrefix(part, "size=") {
			size := strings.TrimPrefix(part, "size=")
			multiplier := float64(1)
			if strings.HasSuffix(size, "G") {
				size = strings.TrimSuffix(size, "G")
			} else if strings.HasSuffix(size, "M") {
				size = strings.TrimSuffix(size, "M")
				multiplier = 1.0 / 1024
			} else if strings.HasSuffix(size, "T") {
				size = strings.TrimSuffix(size, "T")
				multiplier = 1024
			} else {
				return ErrConflict
			}
			current, parseErr := strconv.ParseFloat(size, 64)
			if parseErr != nil {
				return ErrConflict
			}
			if current*multiplier == float64(diskGiB) {
				return nil
			}
			if current*multiplier > float64(diskGiB) {
				return ErrConflict
			}
		}
	}
	return c.request(ctx, http.MethodPut, c.vmPath(identity.ID)+"/resize", url.Values{"disk": {"scsi0"}, "size": {fmt.Sprintf("%dG", diskGiB)}}, nil)
}

func stringsContainsList(value, item string) bool {
	for _, part := range strings.Split(value, ",") {
		if part == item {
			return true
		}
	}
	return false
}
