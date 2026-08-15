package agent

import "math"

func clampUint64ToInt64(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	// #nosec G115 -- value is range-checked against math.MaxInt64 above.
	return int64(value)
}

func clampIntToInt32(value int) int32 {
	if value > math.MaxInt32 {
		return math.MaxInt32
	}
	if value < math.MinInt32 {
		return math.MinInt32
	}
	// #nosec G115 -- value is clamped to the int32 range above.
	return int32(value)
}
