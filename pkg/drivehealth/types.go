// Package drivehealth collects read-only SMART and NVMe observations. Collection
// availability is separate from hardware health; absent metrics are always null.
package drivehealth

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"math/big"
	"regexp"
	"strings"
	"time"
)

const (
	MaxDrives       = 128
	Available       = "available"
	Incomplete      = "incomplete"
	Unsupported     = "unsupported"
	Inaccessible    = "inaccessible"
	Missing         = "missing"
	SourceSMART     = "smartctl"
	SourceNVMe      = "nvme_cli"
	SourceInventory = "inventory"
)

// Collection describes an inventory attempt. Only Complete permits consumers
// to infer that a previously observed identity absent from Drives disappeared.
type Collection struct {
	ObservedAt time.Time `json:"observed_at"`
	Status     string    `json:"status"`
	Reason     string    `json:"reason,omitempty"`
	Complete   bool      `json:"complete"`
	Drives     []Reading `json:"drives"`
}

// Reading contains only current observations. Decimal strings preserve NVMe's
// 128-bit counters without rounding. PercentageUsed is a manufacturer estimate
// of consumed endurance (may exceed 100), never a remaining lifespan prediction.
type Reading struct {
	DriveID                    string          `json:"drive_id"`
	DevicePath                 string          `json:"device_path"`
	Protocol                   string          `json:"protocol"`
	Model                      string          `json:"model"`
	Serial                     string          `json:"serial"`
	WWN                        string          `json:"wwn"`
	Source                     string          `json:"source"`
	ObservedAt                 time.Time       `json:"observed_at"`
	Status                     string          `json:"status"`
	Reason                     string          `json:"reason,omitempty"`
	Missing                    []string        `json:"missing"`
	SmartPassed                *bool           `json:"smart_passed"`
	AttributeFailed            *bool           `json:"attribute_failed"`
	TemperatureCelsius         *float64        `json:"temperature_celsius"`
	TemperatureWarningCelsius  *float64        `json:"temperature_warning_celsius"`
	TemperatureCriticalCelsius *float64        `json:"temperature_critical_celsius"`
	CriticalWarning            *uint8          `json:"critical_warning"`
	PercentageUsed             *uint8          `json:"percentage_used"`
	AvailableSpare             *uint8          `json:"available_spare"`
	SpareThreshold             *uint8          `json:"spare_threshold"`
	MediaErrors                *string         `json:"media_errors"`
	ErrorLogEntries            *string         `json:"error_log_entries"`
	SelfTestFailed             *bool           `json:"self_test_failed"`
	ReallocatedSectors         *string         `json:"reallocated_sectors"`
	PendingSectors             *string         `json:"pending_sectors"`
	UncorrectableSectors       *string         `json:"uncorrectable_sectors"`
	PowerOnHours               *string         `json:"power_on_hours"`
	WearAttributes             []WearAttribute `json:"wear_attributes"`
}

// WearAttribute is a manufacturer's decoded ATA wear/endurance attribute.
// Normalized and raw values retain vendor meaning; no universal wear or life
// percentage is assigned to them.
type WearAttribute struct {
	ID             uint8   `json:"id"`
	Name           string  `json:"name"`
	Value          *uint8  `json:"value"`
	Raw            *string `json:"raw"`
	Interpretation string  `json:"interpretation"`
}

// StableID binds an identity across paths and reader tools, never to a device
// path. An anonymous device cannot participate in historical comparisons.
func StableID(model, serial, wwn string) string {
	model, serial, wwn = strings.TrimSpace(model), strings.TrimSpace(serial), strings.TrimSpace(wwn)
	identity := ""
	if serial != "" {
		identity = "serial\x00" + model + "\x00" + serial
	} else if wwn != "" {
		identity = "wwn\x00" + wwn
	}
	if identity == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(sum[:])
}

var (
	devicePath    = regexp.MustCompile(`^/dev/(?:[shv]d[a-z]+|xvd[a-z]+|nvme[0-9]+(?:n[0-9]+)?|sg[0-9]+)$`)
	deviceType    = regexp.MustCompile(`^(?:|auto|ata|scsi|sat(?:,(?:12|16))?|nvme)$`)
	idValue       = regexp.MustCompile(`^[a-f0-9]{64}$`)
	decimalValue  = regexp.MustCompile(`^(0|[1-9][0-9]{0,38})$`)
	reasonValue   = regexp.MustCompile(`^[a-z0-9_]{1,64}$`)
	attributeName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_ -]{0,127}$`)
)

// ValidateDevice admits only whole-device Linux paths and fixed read types.
// ExecProbe additionally checks the path is an actual device, never a file.
func ValidateDevice(path, kind string) error {
	if !devicePath.MatchString(path) || !deviceType.MatchString(kind) {
		return fmt.Errorf("drivehealth: invalid device")
	}
	return nil
}

func validText(s string) bool { return len(s) <= 256 && !strings.ContainsAny(s, "\x00\n\r") }
func validTime(at, now time.Time) bool {
	return !at.IsZero() && !at.After(now.Add(5*time.Minute)) && now.Sub(at) <= 15*24*time.Hour
}

// Validate checks the structured transport boundary, including identity binding
// and missing values, before a receiver accepts an observation.
func (c Collection) Validate(now time.Time) error {
	if !validTime(c.ObservedAt, now) || len(c.Drives) > MaxDrives {
		return fmt.Errorf("drivehealth: invalid collection bounds")
	}
	if c.Status != Available && c.Status != Incomplete && c.Status != Unsupported && c.Status != Inaccessible && c.Status != Missing {
		return fmt.Errorf("drivehealth: invalid collection status")
	}
	if c.Reason != "" && !reasonValue.MatchString(c.Reason) {
		return fmt.Errorf("drivehealth: invalid reason")
	}
	if c.Complete && c.Status != Available {
		return fmt.Errorf("drivehealth: unsuccessful inventory cannot be complete")
	}
	seen := map[string]bool{}
	for _, r := range c.Drives {
		if err := r.Validate(now); err != nil {
			return err
		}
		if r.DriveID != "" {
			if seen[r.DriveID] {
				return fmt.Errorf("drivehealth: duplicate identity")
			}
			seen[r.DriveID] = true
		}
	}
	return nil
}

// Validate verifies one bounded reading without deriving a hardware diagnosis.
func (r Reading) Validate(now time.Time) error {
	if ValidateDevice(r.DevicePath, "") != nil || !validTime(r.ObservedAt, now) {
		return fmt.Errorf("drivehealth: invalid reading bounds")
	}
	if r.Protocol != "" && r.Protocol != "ATA" && r.Protocol != "NVMe" && r.Protocol != "SCSI" {
		return fmt.Errorf("drivehealth: invalid protocol")
	}
	for _, s := range []string{r.Model, r.Serial, r.WWN} {
		if !validText(s) {
			return fmt.Errorf("drivehealth: invalid identity text")
		}
	}
	if r.DriveID != "" && (!idValue.MatchString(r.DriveID) || r.DriveID != StableID(r.Model, r.Serial, r.WWN)) {
		return fmt.Errorf("drivehealth: identity mismatch")
	}
	if r.DriveID == "" && r.Status == Available {
		return fmt.Errorf("drivehealth: available reading lacks identity")
	}
	if r.Source != SourceSMART && r.Source != SourceNVMe && r.Source != SourceInventory {
		return fmt.Errorf("drivehealth: invalid source")
	}
	if r.Status != Available && r.Status != Incomplete && r.Status != Unsupported && r.Status != Inaccessible && r.Status != Missing {
		return fmt.Errorf("drivehealth: invalid reading status")
	}
	if r.Reason != "" && !reasonValue.MatchString(r.Reason) {
		return fmt.Errorf("drivehealth: invalid reading reason")
	}
	for _, temperature := range []*float64{r.TemperatureCelsius, r.TemperatureWarningCelsius, r.TemperatureCriticalCelsius} {
		if temperature != nil && (math.IsNaN(*temperature) || math.IsInf(*temperature, 0) || *temperature < -273.15 || *temperature > 1000) {
			return fmt.Errorf("drivehealth: invalid temperature")
		}
	}
	if len(r.WearAttributes) > 16 {
		return fmt.Errorf("drivehealth: wear attributes exceed bound")
	}
	attrs := map[uint8]bool{}
	for _, attribute := range r.WearAttributes {
		if attribute.ID == 0 || attrs[attribute.ID] || !attributeName.MatchString(attribute.Name) || attribute.Interpretation != "vendor_specific" || attribute.Raw != nil && !validCounter(*attribute.Raw) {
			return fmt.Errorf("drivehealth: invalid wear attribute")
		}
		attrs[attribute.ID] = true
	}
	for _, p := range []*uint8{r.AvailableSpare, r.SpareThreshold} {
		if p != nil && *p > 100 {
			return fmt.Errorf("drivehealth: invalid spare percentage")
		}
	}
	for _, p := range []*string{r.MediaErrors, r.ErrorLogEntries, r.ReallocatedSectors, r.PendingSectors, r.UncorrectableSectors, r.PowerOnHours} {
		if p != nil && !validCounter(*p) {
			return fmt.Errorf("drivehealth: invalid counter")
		}
	}
	if len(r.Missing) > 20 {
		return fmt.Errorf("drivehealth: missing fields exceed bound")
	}
	seen := map[string]bool{}
	for _, f := range r.Missing {
		if !metricFields[f] || seen[f] || !r.fieldMissing(f) {
			return fmt.Errorf("drivehealth: invalid missing field")
		}
		seen[f] = true
	}
	if r.Status == Available && (len(r.Missing) != 0 || len(requiredMissing(r)) != 0) {
		return fmt.Errorf("drivehealth: available reading lacks required evidence")
	}
	if r.Status == Missing && r.hasMeasurements() {
		return fmt.Errorf("drivehealth: missing device carries measurements")
	}
	if r.Source == SourceInventory && r.Status != Missing {
		return fmt.Errorf("drivehealth: inventory cannot carry health readings")
	}
	return nil
}

func validCounter(value string) bool {
	if !decimalValue.MatchString(value) {
		return false
	}
	n, ok := new(big.Int).SetString(value, 10)
	return ok && n.BitLen() <= 128
}

func (r Reading) fieldMissing(name string) bool {
	switch name {
	case "identity":
		return r.DriveID == ""
	case "smart_passed":
		return r.SmartPassed == nil
	case "attribute_failed":
		return r.AttributeFailed == nil
	case "temperature_celsius":
		return r.TemperatureCelsius == nil
	case "critical_warning":
		return r.CriticalWarning == nil
	case "percentage_used":
		return r.PercentageUsed == nil
	case "available_spare":
		return r.AvailableSpare == nil
	case "spare_threshold":
		return r.SpareThreshold == nil
	case "media_errors":
		return r.MediaErrors == nil
	case "error_log_entries":
		return r.ErrorLogEntries == nil
	case "self_test_failed":
		return r.SelfTestFailed == nil
	case "reallocated_sectors":
		return r.ReallocatedSectors == nil
	case "pending_sectors":
		return r.PendingSectors == nil
	case "uncorrectable_sectors":
		return r.UncorrectableSectors == nil
	case "power_on_hours":
		return r.PowerOnHours == nil
	}
	return false
}

func (r Reading) hasMeasurements() bool {
	if len(r.WearAttributes) > 0 || r.TemperatureWarningCelsius != nil || r.TemperatureCriticalCelsius != nil {
		return true
	}
	for field := range metricFields {
		if field != "identity" && !r.fieldMissing(field) {
			return true
		}
	}
	return false
}

func requiredMissing(r Reading) []string {
	fields := []string{"identity", "temperature_celsius"}
	if r.Protocol == "NVMe" {
		fields = append(fields, "critical_warning", "percentage_used", "available_spare", "spare_threshold", "media_errors", "error_log_entries")
	} else {
		fields = append(fields, "smart_passed", "self_test_failed")
	}
	missing := []string{}
	for _, field := range fields {
		if r.fieldMissing(field) {
			missing = append(missing, field)
		}
	}
	return missing
}

var metricFields = map[string]bool{"identity": true, "smart_passed": true, "attribute_failed": true, "temperature_celsius": true, "critical_warning": true, "percentage_used": true, "available_spare": true, "spare_threshold": true, "media_errors": true, "error_log_entries": true, "self_test_failed": true, "reallocated_sectors": true, "pending_sectors": true, "uncorrectable_sectors": true, "power_on_hours": true}
