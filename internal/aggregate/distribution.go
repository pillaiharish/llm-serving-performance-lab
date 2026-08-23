package aggregate

import (
	"fmt"
	"math"
	"sort"
)

// SummarizeDistribution calculates exact nearest-rank percentiles without
// mutating the caller-owned samples.
func SummarizeDistribution(samples []float64, unavailableCount int, unit, emptyReason string) (Distribution, error) {
	result := Distribution{UnavailableCount: unavailableCount, Unit: unit}
	if unavailableCount < 0 {
		return Distribution{}, fmt.Errorf("unavailable sample count must not be negative")
	}
	if unit == "" {
		return Distribution{}, fmt.Errorf("distribution unit is required")
	}
	if len(samples) == 0 {
		if emptyReason == "" {
			return Distribution{}, fmt.Errorf("unavailable distribution reason is required")
		}
		result.Reason = emptyReason
		return result, nil
	}

	ordered := append([]float64(nil), samples...)
	for _, sample := range ordered {
		if math.IsNaN(sample) || math.IsInf(sample, 0) {
			return Distribution{}, fmt.Errorf("distribution samples must be finite")
		}
	}
	sort.Float64s(ordered)
	total := 0.0
	for _, sample := range ordered {
		total += sample
	}
	mean := total / float64(len(ordered))
	if math.IsNaN(mean) || math.IsInf(mean, 0) {
		return Distribution{}, fmt.Errorf("distribution mean must be finite")
	}
	result.Available = true
	result.SampleCount = len(ordered)
	result.Mean = mean
	result.Min = ordered[0]
	result.P50 = nearestRank(ordered, 0.50)
	result.P90 = nearestRank(ordered, 0.90)
	result.P95 = nearestRank(ordered, 0.95)
	result.P99 = nearestRank(ordered, 0.99)
	result.Max = ordered[len(ordered)-1]
	return result, nil
}

func nearestRank(ordered []float64, percentile float64) float64 {
	rank := int(math.Ceil(percentile * float64(len(ordered))))
	index := rank - 1
	if index < 0 {
		index = 0
	}
	return ordered[index]
}
