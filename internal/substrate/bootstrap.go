package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"gopkg.in/yaml.v3"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// prepareUbuntuCloudInit writes data into an already enabled local snippets
// storage. These fixed cloud-init instructions execute inside the guest only;
// no command, package manager or StackKits process runs on the hypervisor.
func (c *Client) prepareUbuntuCloudInit(ctx context.Context, spec GuestSpec, bootstrap ...[]byte) (string, error) {
	storage := spec.ImageImport.Storage
	var cfg struct {
		Type    string `json:"type"`
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := c.request(ctx, http.MethodGet, "/storage/"+storage, nil, &cfg); err != nil {
		return "", err
	}
	if cfg.Type != "dir" || !filepath.IsAbs(cfg.Path) || !stringsContainsList(cfg.Content, "snippets") {
		return "", errors.New("substrate: Ubuntu bootstrap requires existing local snippets storage")
	}
	directory := filepath.Join(cfg.Path, "snippets")
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("substrate: local snippets directory is unavailable")
	}
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || filepath.Clean(resolved) != filepath.Clean(directory) {
		return "", errors.New("substrate: snippets path may not traverse symlinks")
	}
	key, _ := json.Marshal(spec.SSHPublicKey)
	body := fmt.Sprintf("#cloud-config\nhostname: %s\nssh_pwauth: false\ndisable_root: true\nusers:\n  - name: kombify\n    groups: [adm, sudo]\n    shell: /bin/bash\n    sudo: ['ALL=(ALL) NOPASSWD:ALL']\n    ssh_authorized_keys:\n      - %s\npackage_update: true\npackages: [qemu-guest-agent]\nruncmd:\n  - [systemctl, enable, --now, qemu-guest-agent]\n", spec.Name, key)

	if spec.Enrollment {
		if len(bootstrap) != 1 || len(bootstrap[0]) == 0 || len(bootstrap[0]) > 262144 {
			return "", ErrCapability
		}
		var document map[string]any
		if yaml.Unmarshal(bootstrap[0], &document) != nil {
			return "", ErrCapability
		}
		packages, _ := document["packages"].([]any)
		document["packages"] = append(packages, "qemu-guest-agent")
		commands, _ := document["runcmd"].([]any)
		document["runcmd"] = append([]any{[]string{"systemctl", "enable", "--now", "qemu-guest-agent"}}, commands...)
		document["ssh_pwauth"] = false
		document["disable_root"] = true
		if spec.SSHPublicKey != "" {
			document["users"] = []any{map[string]any{"name": "kombify", "groups": []string{"adm", "sudo"}, "shell": "/bin/bash", "sudo": []string{"ALL=(ALL) NOPASSWD:ALL"}, "ssh_authorized_keys": []string{spec.SSHPublicKey}}}
		} else {
			document["users"] = []any{}
		}
		encoded, err := yaml.Marshal(document)
		if err != nil {
			return "", err
		}
		body = "#cloud-config\n" + string(encoded)
	}
	filename := fmt.Sprintf("kombify-%d-%s.yaml", spec.ID, spec.OperationTag)
	path := filepath.Join(directory, filename)
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.Mode().IsRegular() {
			return "", ErrConflict
		}
		existing, readErr := os.ReadFile(path)
		if readErr != nil || string(existing) != body {
			return "", ErrConflict
		}
		return storage + ":snippets/" + filename, nil
	}
	if err != nil {
		return "", err
	}
	if _, err = file.WriteString(body); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return "", err
	}
	if closeErr != nil {
		return "", closeErr
	}
	return storage + ":snippets/" + filename, nil
}

type GuestObservation struct {
	Present      bool     `json:"present"`
	ID           int      `json:"id"`
	Name         string   `json:"name,omitempty"`
	Status       string   `json:"status,omitempty"`
	Locked       bool     `json:"locked"`
	DiskAttached bool     `json:"disk_attached"`
	Addresses    []string `json:"addresses,omitempty"`
}

func (c *Client) ObserveGuest(ctx context.Context, identity GuestIdentity) (GuestObservation, error) {
	guest, err := c.ownedGuest(ctx, identity)
	if errors.Is(err, ErrNotFound) {
		return GuestObservation{ID: identity.ID}, nil
	}
	if err != nil {
		return GuestObservation{}, err
	}
	observation := GuestObservation{Present: true, ID: guest.ID, Name: guest.Name, Status: guest.Status, Locked: guest.Lock != "", DiskAttached: stringConfig(guest.Config, "scsi0") != ""}
	if guest.Status != "running" {
		return observation, nil
	}
	var response struct {
		Result []struct {
			IPs []struct {
				Address string `json:"ip-address"`
			} `json:"ip-addresses"`
		} `json:"result"`
	}
	if err := c.request(ctx, http.MethodGet, c.vmPath(identity.ID)+"/agent/network-get-interfaces", nil, &response); err != nil {
		return observation, nil
	}
	for _, iface := range response.Result {
		for _, address := range iface.IPs {
			ip := net.ParseIP(address.Address)
			if ip != nil && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified() && !ip.IsMulticast() {
				observation.Addresses = append(observation.Addresses, strings.ToLower(ip.String()))
			}
		}
	}
	return observation, nil
}
