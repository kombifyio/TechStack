package drivehealth_test

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/drivehealth"
)

func ata(serial string, failing bool) []byte {
	exit, passed, failures, pending := 0, true, false, 0
	if failing {
		exit, passed, failures, pending = 152, false, true, 7
	}
	return []byte(fmt.Sprintf(`{"smartctl":{"exit_status":%d},"device":{"protocol":"ATA"},"model_name":"Disk","serial_number":%q,"smart_support":{"available":true,"enabled":true},"smart_status":{"passed":%t},"temperature":{"current":35},"ata_smart_data":{"self_test":{"status":{"passed":%t}}},"ata_smart_attributes":{"table":[{"id":197,"name":"Current_Pending_Sector","raw":{"value":%d}}]},"ata_smart_self_test_log":{"standard":{}},"power_on_time":{"hours":1000}}`, exit, serial, passed, !failures, pending))
}

const nvmeSMART = `{"smartctl":{"exit_status":0},"device":{"protocol":"NVMe"},"model_name":"NVMe Disk","serial_number":"NVME-A","temperature":{"current":40},"smart_status":{"passed":true},"nvme_smart_health_information_log":{"critical_warning":0,"percentage_used":12,"available_spare":100,"available_spare_threshold":10,"media_errors":0,"num_err_log_entries":0,"power_on_hours":100}}`
const nvmeHealth = `{"critical_warning":4,"temperature":313,"avail_spare":8,"spare_thresh":10,"percentage_used":121,"media_errors":18446744073709551617,"num_err_log_entries":0,"power_on_hours":100}`

// Current evidence must distinguish healthy, failing and unavailable hardware,
// preserve counters exactly and never manufacture zeros for absent readings.
func TestHardwareEvidenceIsPreservedWithoutTurningCollectionFailureIntoHealth(t *testing.T) {
	now := time.Now().UTC()
	t.Run("healthy ATA", func(t *testing.T) {
		r, err := drivehealth.ParseSMART(ata("ATA-A", false), "/dev/sda", now)
		if err != nil || r.Status != drivehealth.Available || r.SmartPassed == nil || !*r.SmartPassed || r.SelfTestFailed == nil || *r.SelfTestFailed {
			t.Fatalf("healthy evidence lost: %+v %v", r, err)
		}
	})
	t.Run("failing ATA exit is evidence", func(t *testing.T) {
		r, err := drivehealth.ParseSMART(ata("ATA-A", true), "/dev/sda", now)
		if err != nil || r.Status != drivehealth.Available || r.SmartPassed == nil || *r.SmartPassed || r.SelfTestFailed == nil || !*r.SelfTestFailed || r.AttributeFailed == nil || !*r.AttributeFailed || r.PendingSectors == nil || *r.PendingSectors != "7" {
			t.Fatalf("failing evidence discarded: %+v %v", r, err)
		}
	})
	t.Run("healthy NVMe", func(t *testing.T) {
		r, err := drivehealth.ParseSMART([]byte(nvmeSMART), "/dev/nvme0n1", now)
		if err != nil || r.Status != drivehealth.Available || r.CriticalWarning == nil || *r.CriticalWarning != 0 || r.PercentageUsed == nil || *r.PercentageUsed != 12 || r.MediaErrors == nil || *r.MediaErrors != "0" {
			t.Fatalf("NVMe evidence lost: %+v %v", r, err)
		}
	})
	t.Run("NVMe critical and consumed endurance", func(t *testing.T) {
		r, err := drivehealth.ParseNVMe([]byte(`{"mn":"NVMe Disk","sn":"NVME-A"}`), []byte(nvmeHealth), "/dev/nvme0n1", now)
		if err != nil || r.Status != drivehealth.Available || *r.CriticalWarning != 4 || *r.PercentageUsed != 121 || *r.MediaErrors != "18446744073709551617" || math.Abs(*r.TemperatureCelsius-39.85) > 0.0001 {
			t.Fatalf("NVMe precision/units lost: %+v %v", r, err)
		}
		smart, _ := drivehealth.ParseSMART([]byte(nvmeSMART), "/dev/nvme0n1", now)
		if smart.DriveID != r.DriveID {
			t.Fatal("reader fallback changed drive identity")
		}
	})
	t.Run("reported thermal limits and vendor wear retain their meaning", func(t *testing.T) {
		raw := strings.Replace(string(ata("ATA-A", false)), `"temperature":{"current":35}`, `"temperature":{"current":35,"op_limit_max":60,"critical_limit_max":75}`, 1)
		raw = strings.Replace(raw, `"table":[`, `"table":[{"id":177,"name":"Wear_Leveling_Count","value":96,"raw":{"value":34}},`, 1)
		r, err := drivehealth.ParseSMART([]byte(raw), "/dev/sda", now)
		if err != nil || r.TemperatureWarningCelsius == nil || *r.TemperatureWarningCelsius != 60 || r.TemperatureCriticalCelsius == nil || *r.TemperatureCriticalCelsius != 75 || r.WearAttributes[0].Raw == nil || *r.WearAttributes[0].Raw != "34" || r.WearAttributes[0].Value == nil || *r.WearAttributes[0].Value != 96 || r.WearAttributes[0].Interpretation != "vendor_specific" || r.PercentageUsed != nil {
			t.Fatalf("vendor wear or thermal threshold reinterpreted: %+v %v", r, err)
		}
		nvme, err := drivehealth.ParseNVMe([]byte(`{"mn":"NVMe Disk","sn":"NVME-A","wctemp":343,"cctemp":353}`), []byte(nvmeHealth), "/dev/nvme0n1", now)
		if err != nil || nvme.TemperatureWarningCelsius == nil || math.Abs(*nvme.TemperatureWarningCelsius-69.85) > 0.001 || nvme.TemperatureCriticalCelsius == nil || math.Abs(*nvme.TemperatureCriticalCelsius-79.85) > 0.001 {
			t.Fatalf("NVMe thermal threshold units lost: %+v %v", nvme, err)
		}
	})
	t.Run("NVMe CLI fallback includes existing self-test failure", func(t *testing.T) {
		c := drivehealth.Collector{Probe: func(_ context.Context, kind, path, deviceType string) ([]byte, error) {
			switch kind {
			case drivehealth.ProbeScan:
				return []byte(`{"devices":[{"name":"/dev/nvme0n1","type":"nvme"}]}`), nil
			case drivehealth.ProbeSMART:
				return nil, &drivehealth.ProbeError{Reason: "tool_missing"}
			case drivehealth.ProbeNVMeIdentify:
				return []byte(`{"mn":"NVMe Disk","sn":"NVME-A"}`), nil
			case drivehealth.ProbeNVMeSMART:
				return []byte(nvmeHealth), nil
			case drivehealth.ProbeNVMeSelfTest:
				return []byte(`{"List of Valid Reports":[{"Self test result":15},{"Self test result":7,"Self test code":1}]}`), nil
			default:
				t.Fatal("unexpected probe")
				return nil, nil
			}
		}}
		result, err := c.Collect(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		r := result.Drives[0]
		if r.Source != drivehealth.SourceNVMe || r.SelfTestFailed == nil || !*r.SelfTestFailed || r.MediaErrors == nil {
			t.Fatalf("optional NVMe self-test evidence lost: %+v", r)
		}
		r.SelfTestFailed = nil
		if err := drivehealth.ApplyNVMeSelfTest(&r, []byte(`{"List of Valid Reports":[{"Self test result":15},{"Self test result":2,"Self test code":1}]}`)); err != nil || r.SelfTestFailed != nil {
			t.Fatal("unused/aborted NVMe test was treated as passing or failing")
		}
	})
	t.Run("incomplete remains null", func(t *testing.T) {
		r, err := drivehealth.ParseSMART([]byte(`{"device":{"protocol":"NVMe"},"model_name":"NVMe Disk","serial_number":"NVME-A","nvme_smart_health_information_log":{"critical_warning":0}}`), "/dev/nvme0n1", now)
		if err != nil || r.Status != drivehealth.Incomplete || r.PercentageUsed != nil || r.MediaErrors != nil || r.TemperatureCelsius != nil {
			t.Fatalf("missing readings became values: %+v %v", r, err)
		}
		raw, _ := json.Marshal(r)
		var wire map[string]any
		_ = json.Unmarshal(raw, &wire)
		if wire["media_errors"] != nil || wire["percentage_used"] != nil {
			t.Fatal("missing fields did not remain null on wire")
		}
	})
	t.Run("permission denied", func(t *testing.T) {
		c := drivehealth.Collector{Probe: func(_ context.Context, kind, path, deviceType string) ([]byte, error) {
			if kind == drivehealth.ProbeScan {
				return []byte(`{"devices":[{"name":"/dev/sda","type":"sat"}]}`), nil
			}
			return nil, &drivehealth.ProbeError{Reason: "permission_denied"}
		}}
		result, err := c.Collect(context.Background())
		if err != nil || !result.Complete || result.Drives[0].Status != drivehealth.Inaccessible || result.Drives[0].DriveID != "" || result.Drives[0].SmartPassed != nil {
			t.Fatalf("collection denied became hardware health: %+v %v", result, err)
		}
	})
	t.Run("self-test aborted does not fail hardware", func(t *testing.T) {
		raw := strings.Replace(nvmeSMART, `"smart_status"`, `"nvme_self_test_log":{"table":[{"self_test_result":{"value":2}}]},"smart_status"`, 1)
		r, err := drivehealth.ParseSMART([]byte(raw), "/dev/nvme0n1", now)
		if err != nil || r.SelfTestFailed != nil {
			t.Fatalf("aborted test was reported as hardware failure: %+v %v", r, err)
		}
		raw = strings.Replace(raw, `"value":2`, `"value":7`, 1)
		r, err = drivehealth.ParseSMART([]byte(raw), "/dev/nvme0n1", now)
		if err != nil || r.SelfTestFailed == nil || !*r.SelfTestFailed {
			t.Fatalf("failed NVMe self-test evidence lost: %+v %v", r, err)
		}
	})
}

// Immutable hardware identity is the continuity boundary, including replacement,
// disappearance, reconnect and failed discovery. Paths cannot carry a cache.
func TestCollectorDoesNotTransferMeasurementsAcrossReplacementOrReconnect(t *testing.T) {
	serial := "A"
	present := true
	discoveryFails := false
	readsFail := false
	c := drivehealth.Collector{Probe: func(_ context.Context, kind, path, deviceType string) ([]byte, error) {
		if kind == drivehealth.ProbeScan {
			if discoveryFails {
				return nil, &drivehealth.ProbeError{Reason: "permission_denied"}
			}
			if !present {
				return []byte(`{"devices":[]}`), nil
			}
			return []byte(`{"devices":[{"name":"/dev/sda","type":"sat"}]}`), nil
		}
		if readsFail {
			return nil, &drivehealth.ProbeError{Reason: "permission_denied"}
		}
		return ata(serial, serial == "A"), nil
	}}
	first, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	old := first.Drives[0]
	serial = "B"
	replaced, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	newDrive := replaced.Drives[0]
	if old.DriveID == newDrive.DriveID || newDrive.PendingSectors == nil || *newDrive.PendingSectors != "0" || newDrive.SmartPassed == nil || !*newDrive.SmartPassed {
		t.Fatalf("replacement inherited old reading: %+v", newDrive)
	}
	readsFail = true
	denied, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if denied.Drives[0].DriveID != "" || denied.Drives[0].PendingSectors != nil {
		t.Fatal("permission failure inherited identity or measurement")
	}
	readsFail = false
	_, err = c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	discoveryFails = true
	failed, err := c.Collect(context.Background())
	if err == nil || failed.Complete || failed.Status != drivehealth.Inaccessible {
		t.Fatal("failed scan asserted complete inventory")
	}
	discoveryFails = false
	present = false
	absent, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if absent.Drives[0].DriveID != newDrive.DriveID || absent.Drives[0].Status != drivehealth.Missing || absent.Drives[0].PendingSectors != nil {
		t.Fatalf("missing drive reused cached health: %+v", absent)
	}
	present = true
	back, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if back.Drives[0].DriveID != newDrive.DriveID || back.Drives[0].Status != drivehealth.Available {
		t.Fatalf("reconnect lost hardware continuity: %+v", back)
	}
}

func TestFixedProbeRejectsCallerControlledCommandsAndConfusedIdentity(t *testing.T) {
	for _, request := range [][3]string{{"format", "/dev/sda", ""}, {drivehealth.ProbeScan, "/dev/sda", ""}, {drivehealth.ProbeSMART, "/dev/sda;reboot", ""}, {drivehealth.ProbeSMART, "/etc/passwd", ""}, {drivehealth.ProbeSMART, "/dev/sda", "sat -t long"}, {drivehealth.ProbeNVMeSMART, "/dev/sda", ""}} {
		if drivehealth.ValidateProbe(request[0], request[1], request[2]) == nil {
			t.Fatalf("unsafe probe admitted: %v", request)
		}
	}
	now := time.Now().UTC()
	r, err := drivehealth.ParseSMART(ata("A", false), "/dev/sda", now)
	if err != nil {
		t.Fatal(err)
	}
	r.Serial = "B"
	if r.Validate(now) == nil {
		t.Fatal("mismatched immutable identity admitted")
	}
	identifyCount := 0
	c := drivehealth.Collector{Probe: func(_ context.Context, kind, path, deviceType string) ([]byte, error) {
		switch kind {
		case drivehealth.ProbeScan:
			return []byte(`{"devices":[{"name":"/dev/nvme0n1","type":"nvme"}]}`), nil
		case drivehealth.ProbeSMART:
			return nil, &drivehealth.ProbeError{Reason: "tool_missing"}
		case drivehealth.ProbeNVMeSMART:
			return []byte(nvmeHealth), nil
		default:
			identifyCount++
			serial := "A"
			if identifyCount > 1 {
				serial = "B"
			}
			return []byte(fmt.Sprintf(`{"mn":"NVMe Disk","sn":%q}`, serial)), nil
		}
	}}
	result, err := c.Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Drives[0].DriveID != "" || result.Drives[0].MediaErrors != nil || result.Drives[0].Status != drivehealth.Inaccessible {
		t.Fatal("identity swap between commands acquired old health")
	}
}
