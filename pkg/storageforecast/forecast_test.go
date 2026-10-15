package storageforecast_test

import (
	"math"
	"testing"
	"time"

	"github.com/kombifyio/techstack/pkg/storageforecast"
)

const gib = float64(1 << 30)

func history(now time.Time, used func(day float64) float64) []storageforecast.Sample {
	var samples []storageforecast.Sample
	for hour := 0; hour <= 10*24; hour++ {
		samples = append(samples, storageforecast.Sample{
			At:        now.Add(time.Duration(hour-10*24) * time.Hour),
			UsedBytes: used(float64(hour)/24) * gib, CapacityBytes: 100 * gib,
		})
	}
	return samples
}

// Exhaustion must not be invented when growth has stopped, history cannot
// support it, or the latest observation cannot safely anchor remaining space.
func TestEstimateWithholdsMisleadingDeadline(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	steady := func() []storageforecast.Sample { return history(now, func(d float64) float64 { return 20 + 3*d }) }
	resized := steady()
	for i := len(resized) - 24; i < len(resized); i++ {
		resized[i].CapacityBytes = 200 * gib
	}
	spike := steady()
	spike[len(spike)-1].UsedBytes = 95 * gib
	invalid := steady()
	invalid[len(invalid)-1].UsedBytes = math.NaN()
	conflicting := steady()
	conflicting = append(conflicting, storageforecast.Sample{At: now, UsedBytes: 60 * gib, CapacityBytes: 100 * gib})
	stale := steady()
	for i := range stale {
		stale[i].At = stale[i].At.Add(-3 * time.Hour)
	}
	var sparse []storageforecast.Sample
	for d := 0; d <= 10; d += 2 {
		sparse = append(sparse, storageforecast.Sample{At: now.Add(time.Duration(d-10) * 24 * time.Hour), UsedBytes: (20 + float64(d)) * gib, CapacityBytes: 100 * gib})
	}
	cases := map[string][]storageforecast.Sample{
		"flat":           history(now, func(float64) float64 { return 20 }),
		"falling":        history(now, func(d float64) float64 { return 40 - d }),
		"growth stopped": history(now, func(d float64) float64 { return 20 + 3*math.Min(d, 6) }),
		"growth reversed": history(now, func(d float64) float64 {
			if d <= 6 {
				return 20 + 3*d
			}
			return 38 - 2*(d-6)
		}),
		"resized": resized, "latest outlier": spike, "invalid latest": invalid,
		"conflicting latest": conflicting, "stale": stale, "sparse": sparse, "missing": nil,
		"missing capacity": {{At: now, UsedBytes: 20 * gib}},
	}
	for name, samples := range cases {
		t.Run(name, func(t *testing.T) {
			result := storageforecast.Estimate(samples, now)
			if result.Exhaustion != nil {
				t.Fatalf("misleading exhaustion for %s: %+v", name, result)
			}
			if name == "missing capacity" && (result.UsedBytes == nil || *result.UsedBytes != 20*gib) {
				t.Fatal("missing capacity hid measured usage")
			}
		})
	}
}

// A historical spike should not shift a stable daily trend's capacity window;
// the resulting range must contain the constant-rate physical exhaustion time.
func TestEstimateRobustGrowthScenarios(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for _, spike := range []bool{false, true} {
		samples := history(now, func(d float64) float64 {
			if spike && d >= 2 && d < 3 {
				return 20 + 3*d + 20
			}
			return 20 + 3*d
		})
		result := storageforecast.Estimate(samples, now)
		if result.Exhaustion == nil || result.Exhaustion.LatestDays == nil {
			t.Fatalf("stable trend lost its range: %+v", result)
		}
		physicalDays := (100. - 50.) / 3.
		if result.Exhaustion.EarliestDays > physicalDays || *result.Exhaustion.LatestDays < physicalDays {
			t.Fatalf("range excludes the measured growth scenario: %+v", result)
		}
		if math.Abs(*result.GrowthBytesPerDay/gib-3) > .01 {
			t.Fatalf("historical spike distorted growth: %v", *result.GrowthBytesPerDay/gib)
		}
	}
}

func TestEstimateFullVolumeRetainsGrowth(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	result := storageforecast.Estimate(history(now, func(d float64) float64 { return 70 + 3*d }), now)
	if result.State != "capacity_reached" || result.Exhaustion != nil {
		t.Fatalf("a measured full volume needs immediate action, not a future deadline: %+v", result)
	}
	if result.GrowthBytesPerDay == nil || math.Abs(*result.GrowthBytesPerDay/gib-3) > .01 {
		t.Fatalf("full volume lost its measured growth: %+v", result)
	}
}
