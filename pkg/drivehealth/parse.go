package drivehealth

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

type object map[string]any

func decode(raw []byte) (object, error) {
	if len(raw) == 0 || len(raw) > MaxOutputBytes {
		return nil, fmt.Errorf("drivehealth: invalid JSON size")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var out object
	if err := d.Decode(&out); err != nil || out == nil {
		return nil, fmt.Errorf("drivehealth: invalid JSON")
	}
	var trailing any
	if d.Decode(&trailing) != io.EOF {
		return nil, fmt.Errorf("drivehealth: trailing JSON")
	}
	return out, nil
}

func at(o object, keys ...string) any {
	var value any = map[string]any(o)
	for _, k := range keys {
		m, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = m[k]
	}
	return value
}
func textValue(v any) string { s, _ := v.(string); return strings.TrimSpace(s) }
func boolValue(v any) *bool {
	b, ok := v.(bool)
	if !ok {
		return nil
	}
	return &b
}
func counter(v any) *string {
	var s string
	switch n := v.(type) {
	case json.Number:
		s = n.String()
	case string:
		s = strings.TrimSpace(n)
	default:
		return nil
	}
	if !validCounter(s) {
		return nil
	}
	return &s
}
func uintValue(v any) *uint8 {
	s := counter(v)
	if s == nil {
		return nil
	}
	n, err := strconv.ParseUint(*s, 10, 8)
	if err != nil {
		return nil
	}
	b := uint8(n)
	return &b
}
func floatValue(v any) *float64 {
	n, ok := v.(json.Number)
	if !ok {
		return nil
	}
	f, err := n.Float64()
	if err != nil {
		return nil
	}
	return &f
}
func pointer[T any](v T) *T { return &v }

func baseReading(path, source string, now time.Time) Reading {
	return Reading{DevicePath: path, Source: source, ObservedAt: now.UTC(), Status: Incomplete, Missing: []string{}, WearAttributes: []WearAttribute{}}
}

// ParseSMART accepts smartctl -j -a JSON, including nonzero health-status bits.
// Bit 0..2 represent collection errors; bits 3..7 describe hardware/log evidence.
func ParseSMART(raw []byte, path string, now time.Time) (Reading, error) {
	r := baseReading(path, SourceSMART, now)
	o, err := decode(raw)
	if err != nil {
		return r, err
	}
	r.Model = textValue(at(o, "model_name"))
	if r.Model == "" {
		r.Model = textValue(at(o, "product"))
	}
	r.Serial = textValue(at(o, "serial_number"))
	if w, ok := at(o, "wwn").(map[string]any); ok {
		parts := []string{}
		for _, k := range []string{"naa", "oui", "id"} {
			if n := counter(w[k]); n != nil {
				parts = append(parts, *n)
			}
		}
		if len(parts) == 3 {
			r.WWN = strings.Join(parts, ":")
		}
	}
	if r.WWN == "" {
		r.WWN = textValue(at(o, "logical_unit_id"))
	}
	r.Protocol = textValue(at(o, "device", "protocol"))
	switch strings.ToLower(r.Protocol) {
	case "ata":
		r.Protocol = "ATA"
	case "nvme":
		r.Protocol = "NVMe"
	case "scsi":
		r.Protocol = "SCSI"
	default:
		r.Protocol = ""
	}
	r.DriveID = StableID(r.Model, r.Serial, r.WWN)
	r.SmartPassed = boolValue(at(o, "smart_status", "passed"))
	r.TemperatureCelsius = floatValue(at(o, "temperature", "current"))
	r.TemperatureWarningCelsius = floatValue(at(o, "temperature", "op_limit_max"))
	r.TemperatureCriticalCelsius = floatValue(at(o, "temperature", "critical_limit_max"))
	if r.TemperatureWarningCelsius == nil {
		r.TemperatureWarningCelsius = floatValue(at(o, "nvme_composite_temperature_threshold", "warning"))
	}
	if r.TemperatureCriticalCelsius == nil {
		r.TemperatureCriticalCelsius = floatValue(at(o, "nvme_composite_temperature_threshold", "critical"))
	}
	r.PowerOnHours = counter(at(o, "power_on_time", "hours"))
	exit := uintValue(at(o, "smartctl", "exit_status"))
	if exit != nil {
		if *exit&8 != 0 {
			r.SmartPassed = pointer(false)
		}
		if r.Protocol == "ATA" {
			r.AttributeFailed = pointer(*exit&16 != 0)
		}
		if *exit&128 != 0 {
			r.SelfTestFailed = pointer(true)
		}
	}
	if log, ok := at(o, "nvme_smart_health_information_log").(map[string]any); ok {
		r.Protocol = "NVMe"
		r.CriticalWarning = uintValue(log["critical_warning"])
		r.PercentageUsed = uintValue(log["percentage_used"])
		r.AvailableSpare = uintValue(log["available_spare"])
		r.SpareThreshold = uintValue(log["available_spare_threshold"])
		r.MediaErrors = counter(log["media_errors"])
		r.ErrorLogEntries = counter(log["num_err_log_entries"])
		if r.TemperatureCelsius == nil {
			r.TemperatureCelsius = floatValue(log["temperature"])
		}
		if r.PowerOnHours == nil {
			r.PowerOnHours = counter(log["power_on_hours"])
		}
	}
	if rows, ok := at(o, "ata_smart_attributes", "table").([]any); ok {
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			id := uintValue(m["id"])
			if id == nil {
				continue
			}
			raw, _ := m["raw"].(map[string]any)
			// ATA attribute numbers are vendor-specific. Require smartmontools'
			// decoded name as well, rather than interpreting a raw ID alone.
			name := textValue(m["name"])
			if len(r.WearAttributes) < 16 && attributeName.MatchString(name) && wearName(name) {
				r.WearAttributes = append(r.WearAttributes, WearAttribute{ID: *id, Name: name, Value: uintValue(m["value"]), Raw: counter(raw["value"]), Interpretation: "vendor_specific"})
			}
			switch {
			case *id == 5 && (name == "Reallocated_Sector_Ct" || name == "Reallocated_Sector_Count"):
				r.ReallocatedSectors = counter(raw["value"])
			case *id == 197 && (name == "Current_Pending_Sector" || name == "Current_Pending_Sector_Ct"):
				r.PendingSectors = counter(raw["value"])
			case *id == 198 && (name == "Offline_Uncorrectable" || name == "Offline_Uncorrectable_Ct"):
				r.UncorrectableSectors = counter(raw["value"])
			}
		}
	}
	if r.SelfTestFailed == nil {
		for _, kind := range []string{"standard", "extended"} {
			if _, ok := at(o, "ata_smart_self_test_log", kind).(map[string]any); ok && exit != nil {
				r.SelfTestFailed = pointer(*exit&128 != 0)
			}
		}
		if passed := boolValue(at(o, "ata_smart_data", "self_test", "status", "passed")); passed != nil {
			r.SelfTestFailed = pointer(!*passed)
		}
	}
	if r.ErrorLogEntries == nil {
		r.ErrorLogEntries = counter(at(o, "ata_smart_error_log", "summary", "count"))
	}
	if rows, ok := at(o, "nvme_self_test_log", "table").([]any); ok {
		for _, row := range rows {
			m, ok := row.(map[string]any)
			if !ok {
				continue
			}
			result, _ := m["self_test_result"].(map[string]any)
			value := uintValue(result["value"])
			if value == nil {
				continue
			}
			if *value >= 5 && *value <= 7 {
				r.SelfTestFailed = pointer(true)
				break
			}
			if *value == 0 && r.SelfTestFailed == nil {
				r.SelfTestFailed = pointer(false)
			}
		}
	}
	finish(&r)
	if support := boolValue(at(o, "smart_support", "available")); support != nil && !*support {
		r.Status = Unsupported
		r.Reason = "smart_unsupported"
	}
	if enabled := boolValue(at(o, "smart_support", "enabled")); enabled != nil && !*enabled {
		r.Status = Unsupported
		r.Reason = "smart_disabled"
	}
	if exit != nil && *exit&7 != 0 {
		if *exit&2 != 0 {
			r.Status = Inaccessible
			r.Reason = "device_open_failed"
		} else {
			r.Status = Incomplete
			r.Reason = "command_failed"
		}
	}
	if err := r.Validate(now); err != nil {
		return baseReading(path, SourceSMART, now), err
	}
	return r, nil
}

// ParseNVMe combines id-ctrl and smart-log JSON from nvme-cli. Unlike
// smartctl, nvme-cli reports SMART temperature in kelvin, converted here.
func ParseNVMe(identity, health []byte, path string, now time.Time) (Reading, error) {
	r := baseReading(path, SourceNVMe, now)
	r.Protocol = "NVMe"
	i, err := decode(identity)
	if err != nil {
		return r, err
	}
	h, err := decode(health)
	if err != nil {
		return r, err
	}
	r.Model = textValue(at(i, "mn"))
	r.Serial = textValue(at(i, "sn"))
	r.DriveID = StableID(r.Model, r.Serial, r.WWN)
	if t := floatValue(at(i, "wctemp")); t != nil && *t != 0 {
		r.TemperatureWarningCelsius = pointer(*t - 273.15)
	}
	if t := floatValue(at(i, "cctemp")); t != nil && *t != 0 {
		r.TemperatureCriticalCelsius = pointer(*t - 273.15)
	}
	r.CriticalWarning = uintValue(at(h, "critical_warning"))
	r.PercentageUsed = uintValue(at(h, "percentage_used"))
	r.AvailableSpare = uintValue(at(h, "avail_spare"))
	r.SpareThreshold = uintValue(at(h, "spare_thresh"))
	r.MediaErrors = counter(at(h, "media_errors"))
	r.ErrorLogEntries = counter(at(h, "num_err_log_entries"))
	r.PowerOnHours = counter(at(h, "power_on_hours"))
	if t := floatValue(at(h, "temperature")); t != nil && *t != 0 {
		r.TemperatureCelsius = pointer(*t - 273.15)
	}
	finish(&r)
	if err := r.Validate(now); err != nil {
		return baseReading(path, SourceNVMe, now), err
	}
	return r, nil
}

func wearName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "wear") || strings.Contains(lower, "endurance") || strings.Contains(lower, "life")
}

// ApplyNVMeSelfTest decodes existing nvme-cli result entries (no test is run).
// Aborted, unused and absent entries are unknown, never a passing self-test.
func ApplyNVMeSelfTest(r *Reading, raw []byte) error {
	o, err := decode(raw)
	if err != nil {
		return err
	}
	rows, ok := at(o, "List of Valid Reports").([]any)
	if !ok {
		return fmt.Errorf("drivehealth: invalid NVMe self-test log")
	}
	if len(rows) > 20 {
		return fmt.Errorf("drivehealth: invalid NVMe self-test log bound")
	}
	for _, row := range rows {
		m, ok := row.(map[string]any)
		if !ok {
			continue
		}
		result := uintValue(m["Self test result"])
		if result == nil {
			continue
		}
		if *result >= 5 && *result <= 7 {
			r.SelfTestFailed = pointer(true)
			break
		}
		if *result == 0 && r.SelfTestFailed == nil {
			r.SelfTestFailed = pointer(false)
		}
	}
	return nil
}

func finish(r *Reading) {
	r.Missing = requiredMissing(*r)
	r.Status = Available
	if len(r.Missing) > 0 {
		r.Status = Incomplete
		r.Reason = "incomplete_telemetry"
	}
}
