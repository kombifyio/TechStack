package substrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var usbHostPattern = regexp.MustCompile(`^[0-9]+-[0-9]+(?:\.[0-9]+)*$`)
var usbSlotPattern = regexp.MustCompile(`^usb[0-4]$`)

type USBTransferGrant struct {
	SourceID                int       `json:"source_id"`
	SourceName              string    `json:"source_name"`
	SourceBaseDigest        string    `json:"source_base_digest"`
	SourcePowerConfigDigest string    `json:"source_power_config_digest"`
	Slot                    string    `json:"slot"`
	HostPath                string    `json:"host_path"`
	Serial                  string    `json:"serial"`
	VendorID                string    `json:"vendor_id"`
	ProductID               string    `json:"product_id"`
	ExpiresAt               time.Time `json:"expires_at"`
}

// VerifyUSBPowerSource binds the transfer to the same enrolled node and exact
// separately authorized power source. The original digest stays immutable;
// observed revisions may change only inside the explicitly granted USB slot.
func (c *Client) VerifyUSBPowerSource(ctx context.Context, target *Client, usb USBTransferGrant, power ObservedGuestGrant) (ObservedGuestGrant, error) {
	if !c.SameNodeAuthority(target) || usb.SourceID != power.ID || usb.SourceName != power.Name ||
		power.ConfigDigest == "" || usb.SourcePowerConfigDigest != power.ConfigDigest ||
		!usbSlotPattern.MatchString(usb.Slot) || !usbHostPattern.MatchString(usb.HostPath) || usb.Serial == "" || usb.VendorID == "" || usb.ProductID == "" ||
		!power.ExpiresAt.After(time.Now()) || !usb.ExpiresAt.After(time.Now()) || !power.AllowStart || !power.AllowStop {
		return power, ErrOwnership
	}
	guest, err := c.Guest(ctx, power.ID)
	if err != nil {
		return power, err
	}
	if guest.Name != power.Name || USBSourceConfigDigest(guest, usb.Slot) != usb.SourceBaseDigest {
		return power, ErrOwnership
	}
	if value := stringConfig(guest.Config, usb.Slot); value != "" && !optionEquals(value, "host", usb.HostPath) {
		return power, ErrConflict
	}
	power.ConfigDigest = GuestConfigDigest(guest)
	return power, nil
}

// USBSourceConfigDigest binds every source configuration field except the
// explicitly granted USB slot and Proxmox's changing config revision digest.
// This permits interruption recovery after detaching that one device while
// rejecting unrelated source changes.
func USBSourceConfigDigest(guest Guest, slot string) string {
	config := map[string]any{}
	for k, v := range guest.Config {
		if k != slot && k != "digest" {
			config[k] = v
		}
	}
	payload, _ := json.Marshal(config)
	sum := sha256.Sum256(payload)
	return "sha256:" + hex.EncodeToString(sum[:])
}

// TransferUSB moves only the explicitly granted physical device between a
// separately authorized source and an owned destination. Both remain stopped
// through partial failure. Repeating it adopts an already completed move.
func (c *Client) TransferUSB(ctx context.Context, grant USBTransferGrant, target GuestIdentity, toTarget bool) error {
	var source, destination Guest
	var err error
	// Every observation rechecks identities, stopped state and global USB
	// assignments. A successful write response alone is never custody proof.
	observe := func() error {
		if !grant.ExpiresAt.After(time.Now()) || !usbHostPattern.MatchString(grant.HostPath) || !usbSlotPattern.MatchString(grant.Slot) || grant.Serial == "" || grant.VendorID == "" || grant.ProductID == "" || grant.SourceID == target.ID {
			return ErrOwnership
		}
		// A filtered VM list cannot prove that an adapter is globally unassigned.
		var permissions map[string]map[string]any
		if err := c.request(ctx, http.MethodGet, "/access/permissions?path=%2Fvms", nil, &permissions); err != nil {
			return err
		}
		if permissions["/vms"]["VM.Audit"] != float64(1) {
			return ErrCapability
		}
		source, err = c.Guest(ctx, grant.SourceID)
		if err != nil {
			return err
		}
		destination, err = c.ownedGuest(ctx, target)
		if err != nil {
			return err
		}
		if source.Name != grant.SourceName || USBSourceConfigDigest(source, grant.Slot) != grant.SourceBaseDigest || source.Status != "stopped" || destination.Status != "stopped" || source.Lock != "" || destination.Lock != "" {
			return ErrOwnership
		}
		var devices []struct {
			Path    string `json:"usbpath"`
			Serial  string `json:"serial"`
			Vendor  string `json:"vendid"`
			Product string `json:"prodid"`
		}
		if err := c.request(ctx, http.MethodGet, c.nodePath()+"/hardware/usb", nil, &devices); err != nil {
			return err
		}
		found := 0
		for _, device := range devices {
			if device.Serial == grant.Serial {
				if device.Path != grant.HostPath || device.Vendor != grant.VendorID || device.Product != grant.ProductID {
					return ErrConflict
				}
				found++
			}
		}
		if found != 1 {
			return errors.New("substrate: physical USB identity is missing or ambiguous")
		}
		var guests []Guest
		if err := c.request(ctx, http.MethodGet, c.nodePath()+"/qemu", nil, &guests); err != nil {
			return err
		}
		for _, guest := range guests {
			config := map[string]any{}
			if err := c.request(ctx, http.MethodGet, c.vmPath(guest.ID)+"/config", nil, &config); err != nil {
				return err
			}
			for key, value := range config {
				text, _ := value.(string)
				if !strings.HasPrefix(key, "usb") {
					continue
				}
				if optionEquals(text, "host", grant.HostPath) || optionEquals(text, "host", grant.VendorID+":"+grant.ProductID) {
					if (guest.ID != grant.SourceID && guest.ID != target.ID) || key != grant.Slot {
						return errors.New("substrate: USB device is already assigned outside this grant")
					}
				}
			}
		}
		// LXC device passthrough is outside this adapter. Any configured device or
		// custom mount prevents an exclusivity claim until explicitly resolved.
		var containers []struct {
			ID int `json:"vmid"`
		}
		if err := c.request(ctx, http.MethodGet, c.nodePath()+"/lxc", nil, &containers); err != nil {
			return err
		}
		for _, container := range containers {
			var config map[string]any
			path := strings.Replace(c.vmPath(container.ID), "/qemu/", "/lxc/", 1) + "/config"
			if err := c.request(ctx, http.MethodGet, path, nil, &config); err != nil {
				return err
			}
			for key := range config {
				if strings.HasPrefix(key, "dev") || strings.HasPrefix(key, "mp") || strings.HasPrefix(key, "lxc.mount") {
					return errors.New("substrate: container device custody prevents USB exclusivity proof")
				}
			}
		}
		return nil
	}
	if err := observe(); err != nil {
		return err
	}
	participants := func() (Guest, Guest) {
		if toTarget {
			return source, destination
		}
		return destination, source
	}
	from, to := participants()
	fromValue := stringConfig(from.Config, grant.Slot)
	toValue := stringConfig(to.Config, grant.Slot)
	if (toValue != "" && !optionEquals(toValue, "host", grant.HostPath)) ||
		(fromValue != "" && !optionEquals(fromValue, "host", grant.HostPath)) {
		return ErrConflict
	}
	if fromValue != "" {
		writeErr := c.request(ctx, http.MethodPut, c.vmPath(from.ID)+"/config", url.Values{"delete": {grant.Slot}, "digest": {stringConfig(from.Config, "digest")}}, nil)
		if err := observe(); err != nil {
			return errors.Join(writeErr, err)
		}
		from, to = participants()
		if stringConfig(from.Config, grant.Slot) != "" {
			return errors.Join(writeErr, ErrConflict)
		}
	}
	toValue = stringConfig(to.Config, grant.Slot)
	if toValue != "" && !optionEquals(toValue, "host", grant.HostPath) {
		return ErrConflict
	}
	if toValue == "" {
		writeErr := c.request(ctx, http.MethodPut, c.vmPath(to.ID)+"/config", url.Values{grant.Slot: {"host=" + grant.HostPath}, "digest": {stringConfig(to.Config, "digest")}}, nil)
		if err := observe(); err != nil {
			return errors.Join(writeErr, err)
		}
		from, to = participants()
		if stringConfig(from.Config, grant.Slot) != "" || !optionEquals(stringConfig(to.Config, grant.Slot), "host", grant.HostPath) {
			return errors.Join(writeErr, ErrConflict)
		}
	}
	return nil
}
