package bench

import (
	"fmt"
	"math"
	"sort"
)

// ComputeStats calculates p50/p95/p99/mean/stddev for a slice of measurements.
// Input slice is not modified - a copy is sorted internally.
// Returns zero Stats if measurements is empty.
func ComputeStats(measurements []Measurement) Stats {
	if len(measurements) == 0 {
		return Stats{}
	}

	// Extract values and sort for percentile computation
	values := make([]float64, len(measurements))
	for i, m := range measurements {
		values[i] = m.Value
	}
	sort.Float64s(values)

	n := len(values)
	mean := computeMean(values)

	return Stats{
		Count:  n,
		Mean:   mean,
		StdDev: computeStdDev(values, mean),
		Min:    values[0],
		Max:    values[n-1],
		P50:    percentile(values, 50),
		P95:    percentile(values, 95),
		P99:    percentile(values, 99),
	}
}

// percentile computes the Nth percentile of a sorted float64 slice
// using linear interpolation (same method as Go's testing/benchmark).
func percentile(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if len(sorted) == 1 {
		return sorted[0]
	}
	// Rank using the nearest-rank method
	rank := (p / 100.0) * float64(len(sorted)-1)
	lower := int(math.Floor(rank))
	upper := int(math.Ceil(rank))
	if lower == upper {
		return sorted[lower]
	}
	// Linear interpolation between adjacent values
	frac := rank - float64(lower)
	return sorted[lower] + frac*(sorted[upper]-sorted[lower])
}

// computeMean returns the arithmetic mean of a float64 slice.
// Uses Kahan compensated summation to reduce floating-point error
// for large slices with values spanning many orders of magnitude.
func computeMean(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	var sum, compensation float64
	for _, v := range values {
		y := v - compensation
		t := sum + y
		compensation = (t - sum) - y
		sum = t
	}
	return sum / float64(len(values))
}

// computeStdDev returns the population standard deviation.
func computeStdDev(values []float64, mean float64) float64 {
	if len(values) < 2 {
		return 0
	}
	var sumSqDiff float64
	for _, v := range values {
		diff := v - mean
		sumSqDiff += diff * diff
	}
	return math.Sqrt(sumSqDiff / float64(len(values)))
}

// FormatValue converts a raw value to a human-readable string with unit.
func FormatValue(value float64, unit string) string {
	switch unit {
	case "ms":
		return fmt.Sprintf("%.2f ms", value/1e6) // stored as nanoseconds
	case "ns":
		if value < 1000 {
			return fmt.Sprintf("%.0f ns", value)
		}
		if value < 1e6 {
			return fmt.Sprintf("%.2f µs", value/1000)
		}
		return fmt.Sprintf("%.2f ms", value/1e6)
	case "MB/s":
		return fmt.Sprintf("%.1f MB/s", value)
	case "tok/s":
		return fmt.Sprintf("%.1f tok/s", value)
	default:
		return fmt.Sprintf("%.2f %s", value, unit)
	}
}
