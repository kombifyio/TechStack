package guardbinding

import (
	"testing"
	"time"
)

func TestBindingRepairRequiredFollowsThePinReportAndThePairingDate(t *testing.T) {
	old := BindingPinsIntroduced.Add(-time.Second)
	fresh := BindingPinsIntroduced
	cases := []struct {
		name     string
		report   string
		pairedAt time.Time
		want     bool
	}{
		{name: "absent", report: BindingPinAbsent, pairedAt: fresh, want: true},
		{name: "unreadable", report: BindingPinUnreadable, pairedAt: fresh, want: true},
		{name: "present", report: BindingPinPresent, pairedAt: old, want: false},
		{name: "old pairing without a report", report: "", pairedAt: old, want: true},
		{name: "pairing at the cutoff without a report", report: "", pairedAt: fresh, want: false},
		{name: "later pairing without a report", report: "", pairedAt: fresh.Add(time.Hour), want: false},
		{name: "zero paired-at without a report", report: "", pairedAt: time.Time{}, want: false},
		{name: "unknown report uses the pairing date", report: "maybe", pairedAt: old, want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := BindingRepairRequired(tc.report, tc.pairedAt); got != tc.want {
				t.Fatalf("BindingRepairRequired(%q, %s) = %v, want %v", tc.report, tc.pairedAt.Format(time.RFC3339), got, tc.want)
			}
		})
	}
}

func TestPostureNormalizeKeepsOnlyKnownBindingPinReports(t *testing.T) {
	kept := Posture{Mode: PostureSplit, BindingPin: BindingPinAbsent}.Normalize()
	if kept.BindingPin != BindingPinAbsent {
		t.Fatalf("known report normalized to %q", kept.BindingPin)
	}
	dropped := Posture{Mode: PostureSplit, BindingPin: "secret-material"}.Normalize()
	if dropped.BindingPin != "" {
		t.Fatalf("unknown report normalized to %q", dropped.BindingPin)
	}
}
