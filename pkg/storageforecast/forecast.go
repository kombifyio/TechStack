// Package storageforecast projects measured filesystem usage without treating
// missing history, cleanup or a resize as evidence of sustained growth.
package storageforecast

import (
	"math"
	"sort"
	"time"
)

const (
	Window         = 14 * 24 * time.Hour
	Freshness      = 2 * time.Hour
	day            = 24 * time.Hour
	minimumBuckets = 5
	horizonDays    = 90
)

// Sample is one co-timed usage/capacity observation. A zero capacity means
// unknown, never unlimited. FreeBytes is the collector's reported free space.
type Sample struct {
	At            time.Time
	UsedBytes     float64
	CapacityBytes float64
	FreeBytes     *float64
}

// Exhaustion describes observed growth scenarios, not a confidence interval.
// Durations start at LatestAt; a missing upper bound is deliberately unbounded.
type Exhaustion struct {
	EarliestDays float64  `json:"earliest_days"`
	LatestDays   *float64 `json:"latest_days,omitempty"`
}

type Forecast struct {
	State                 string      `json:"state"`
	Reason                string      `json:"reason,omitempty"`
	Notes                 []string    `json:"notes"`
	UsedBytes             *float64    `json:"used_bytes,omitempty"`
	CapacityBytes         *float64    `json:"capacity_bytes,omitempty"`
	FreeBytes             *float64    `json:"free_bytes,omitempty"`
	LatestAt              *time.Time  `json:"latest_at,omitempty"`
	ObservedFrom          *time.Time  `json:"observed_from,omitempty"`
	ObservedTo            *time.Time  `json:"observed_to,omitempty"`
	SampleCount           int         `json:"sample_count"`
	BucketCount           int         `json:"bucket_count"`
	GrowthBytesPerDay     *float64    `json:"growth_bytes_per_day,omitempty"`
	GrowthLowBytesPerDay  *float64    `json:"growth_low_bytes_per_day,omitempty"`
	GrowthHighBytesPerDay *float64    `json:"growth_high_bytes_per_day,omitempty"`
	Exhaustion            *Exhaustion `json:"exhaustion,omitempty"`
	HorizonDays           float64     `json:"horizon_days"`
	CapacityChangedAt     *time.Time  `json:"capacity_changed_at,omitempty"`
}

// Estimate uses daily medians and pairwise slopes at least a day apart. This
// prevents frequent scrapes from outweighing a day of observations. At least
// five observed days over four elapsed days are needed. The scenario envelope
// uses slope quartiles and a minimum 20% rate spread, not statistical confidence.
func Estimate(input []Sample, now time.Time) (f Forecast) {
	f = Forecast{State: "insufficient_data", Reason: "no_history", Notes: []string{}, HorizonDays: horizonDays}
	samples := make([]Sample, 0, len(input))
	invalid := false
	for _, s := range input {
		if s.At.Before(now.Add(-Window)) {
			continue
		}
		if s.At.IsZero() || s.At.After(now) || !finite(s.UsedBytes) || s.UsedBytes < 0 ||
			!finite(s.CapacityBytes) || s.CapacityBytes < 0 ||
			(s.FreeBytes != nil && (!finite(*s.FreeBytes) || *s.FreeBytes < 0 ||
				(s.CapacityBytes > 0 && *s.FreeBytes > s.CapacityBytes))) {
			invalid = true
			continue
		}
		s.At = s.At.UTC()
		samples = append(samples, s)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
	unique := samples[:0]
	conflict := false
	for _, s := range samples {
		if len(unique) > 0 && unique[len(unique)-1].At.Equal(s.At) {
			p := unique[len(unique)-1]
			if p.UsedBytes != s.UsedBytes || p.CapacityBytes != s.CapacityBytes || !sameOptional(p.FreeBytes, s.FreeBytes) {
				conflict = true
			}
			continue
		}
		unique = append(unique, s)
	}
	samples = unique
	if len(samples) == 0 {
		if invalid {
			f.Reason = "invalid_measurements"
		}
		return f
	}
	last := samples[len(samples)-1]
	f.UsedBytes, f.LatestAt = ptr(last.UsedBytes), &last.At
	f.ObservedFrom, f.ObservedTo = &samples[0].At, &last.At
	f.SampleCount = len(samples)
	if last.CapacityBytes > 0 {
		f.CapacityBytes = ptr(last.CapacityBytes)
	}
	if last.FreeBytes != nil {
		f.FreeBytes = ptr(*last.FreeBytes)
	}
	if invalid {
		f.Reason = "invalid_measurements"
		return f
	}
	if conflict {
		f.Reason = "conflicting_measurements"
		return f
	}
	if now.Sub(last.At) > Freshness {
		f.Reason = "stale_measurement"
		return f
	}
	if last.CapacityBytes <= 0 {
		f.Reason = "capacity_unknown"
		return f
	}
	remaining := math.Max(0, last.CapacityBytes-last.UsedBytes)
	if last.FreeBytes != nil {
		remaining = math.Min(remaining, *last.FreeBytes)
	}
	if remaining <= 0 {
		// Keep the measured urgency while still explaining the growth and
		// observation period when history supports them.
		defer func() {
			f.State = "capacity_reached"
			f.Exhaustion = nil
		}()
	}

	// A new size is a new period, even if an earlier period had the same size.
	// Missing capacity also breaks continuity; percentages are never rescaled
	// using today's capacity to invent past byte observations.
	for i := len(samples) - 2; i >= 0; i-- {
		if samples[i].CapacityBytes != last.CapacityBytes {
			if samples[i].CapacityBytes > 0 {
				changed := samples[i+1].At
				f.CapacityChangedAt = &changed
				f.Notes = append(f.Notes, "capacity_changed")
			}
			samples = samples[i+1:]
			break
		}
	}
	f.ObservedFrom, f.SampleCount = &samples[0].At, len(samples)
	buckets := dailyMedians(samples)
	f.BucketCount = len(buckets)
	if len(buckets) < minimumBuckets || last.At.Sub(samples[0].At) < 4*day {
		f.Reason = "insufficient_history"
		if f.CapacityChangedAt != nil {
			f.Reason = "capacity_changed"
		}
		return f
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].At.Sub(samples[i-1].At) > 36*time.Hour {
			f.Reason = "sparse_history"
			return f
		}
	}
	slopes := make([]float64, 0, len(buckets)*len(buckets)/2)
	for i, a := range buckets {
		for _, b := range buckets[i+1:] {
			elapsed := b.at.Sub(a.at)
			if elapsed >= day {
				slopes = append(slopes, (b.used-a.used)/(elapsed.Hours()/24))
			}
		}
	}
	if len(slopes) < minimumBuckets-1 {
		f.Reason = "sparse_history"
		return f
	}
	sort.Float64s(slopes)
	middle := quantile(slopes, .5)
	low, high := quantile(slopes, .25), quantile(slopes, .75)
	f.GrowthBytesPerDay = ptr(middle)
	f.GrowthLowBytesPerDay, f.GrowthHighBytesPerDay = ptr(low), ptr(high)
	f.Notes = append(f.Notes, "robust_trend")

	// Historical spikes have little influence on the median fit. A suspect
	// *latest* value cannot safely anchor remaining space, so withhold its ETA.
	intercepts := make([]float64, 0, len(buckets))
	for _, b := range buckets {
		intercepts = append(intercepts, b.used-middle*b.at.Sub(last.At).Hours()/24)
	}
	center := median(intercepts)
	deviations := make([]float64, 0, len(intercepts))
	for _, value := range intercepts {
		deviations = append(deviations, math.Abs(value-center))
	}
	residualLimit := math.Max(6*median(deviations), math.Max(last.CapacityBytes*.02, 3*math.Abs(middle)))
	if math.Abs(last.UsedBytes-center) > residualLimit {
		f.Reason = "recent_outlier"
		return f
	}
	// A one-off import or cleanup is not a continuing workload rate. Nor is a
	// long-window rise evidence of growth after a recent sustained reversal.
	if middle > 0 && (dominatedByOneIncrease(buckets) || recentNotGrowing(buckets)) {
		f.Reason = "unstable_growth"
		return f
	}
	if high <= 0 {
		f.State, f.Reason = "flat", ""
		if middle < 0 {
			f.State = "shrinking"
		}
		return f
	}
	if middle <= 0 {
		f.Reason = "unstable_growth"
		return f
	}
	low, high = math.Min(low, middle*.8), math.Max(high, middle*1.2)
	f.GrowthLowBytesPerDay, f.GrowthHighBytesPerDay = ptr(low), ptr(high)
	f.State, f.Reason = "growing", ""
	if low <= 0 || high > low*2 {
		f.Notes = append(f.Notes, "variable_growth")
	}
	earliest := remaining / high
	if !finite(earliest) || earliest > horizonDays {
		f.State = "beyond_horizon"
		f.Notes = append(f.Notes, "limited_horizon")
		return f
	}
	f.Exhaustion = &Exhaustion{EarliestDays: earliest}
	if low > 0 {
		latest := remaining / low
		if finite(latest) && latest <= horizonDays {
			f.Exhaustion.LatestDays = ptr(latest)
		} else {
			f.Notes = append(f.Notes, "limited_horizon")
		}
	}
	return f
}

type bucket struct {
	at   time.Time
	used float64
}

func dailyMedians(samples []Sample) []bucket {
	var result []bucket
	for start := 0; start < len(samples); {
		end := start + 1
		date := samples[start].At.Truncate(day)
		for end < len(samples) && samples[end].At.Truncate(day).Equal(date) {
			end++
		}
		values := make([]float64, 0, end-start)
		for _, s := range samples[start:end] {
			values = append(values, s.UsedBytes)
		}
		// Midpoint of the observed interval, not midnight or a manufactured scrape.
		at := samples[start].At.Add(samples[end-1].At.Sub(samples[start].At) / 2)
		result = append(result, bucket{at: at, used: median(values)})
		start = end
	}
	return result
}

func dominatedByOneIncrease(buckets []bucket) bool {
	var total, largest float64
	for i := 1; i < len(buckets); i++ {
		increase := math.Max(0, buckets[i].used-buckets[i-1].used)
		total += increase
		largest = math.Max(largest, increase)
	}
	return total > 0 && largest/total > .8
}

func recentNotGrowing(buckets []bucket) bool {
	n := len(buckets)
	return n >= 3 && buckets[n-1].used <= buckets[n-2].used && buckets[n-2].used <= buckets[n-3].used
}

func median(values []float64) float64 { sort.Float64s(values); return quantile(values, .5) }
func quantile(sorted []float64, q float64) float64 {
	position := float64(len(sorted)-1) * q
	left := int(position)
	if left+1 >= len(sorted) {
		return sorted[left]
	}
	weight := position - float64(left)
	return sorted[left]*(1-weight) + sorted[left+1]*weight
}
func finite(value float64) bool  { return !math.IsNaN(value) && !math.IsInf(value, 0) }
func ptr(value float64) *float64 { return &value }
func sameOptional(a, b *float64) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}
