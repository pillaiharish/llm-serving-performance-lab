package benchmark

import (
	"fmt"
	"math"
	"time"
)

// PlannedArrivalCount returns the number of targets i for which
// (i-1)/requestRate is strictly less than duration.
func PlannedArrivalCount(requestRate float64, duration time.Duration) (int, error) {
	if math.IsNaN(requestRate) || math.IsInf(requestRate, 0) || requestRate <= 0 {
		return 0, fmt.Errorf("request rate must be a finite number greater than zero")
	}
	if duration <= 0 {
		return 0, fmt.Errorf("duration must be greater than zero")
	}
	product := requestRate * (float64(duration) / float64(time.Second))
	if math.IsNaN(product) || math.IsInf(product, 0) || product <= 0 || product >= float64(maxInt()) {
		return 0, fmt.Errorf("planned arrival count overflows int")
	}
	count := int(math.Ceil(product))
	if count <= 0 {
		return 0, fmt.Errorf("planned arrival count must be greater than zero")
	}
	return count, nil
}

// ArrivalOffsets derives every target independently from the original phase
// epoch. Conversion to time.Duration floors the mathematical nanosecond value.
func ArrivalOffsets(requestRate float64, count int) ([]time.Duration, error) {
	if math.IsNaN(requestRate) || math.IsInf(requestRate, 0) || requestRate <= 0 {
		return nil, fmt.Errorf("request rate must be a finite number greater than zero")
	}
	if count < 0 {
		return nil, fmt.Errorf("arrival count must not be negative")
	}
	offsets := make([]time.Duration, count)
	for index := 0; index < count; index++ {
		nanoseconds := math.Floor(float64(index) * float64(time.Second) / requestRate)
		if math.IsNaN(nanoseconds) || math.IsInf(nanoseconds, 0) || nanoseconds < 0 || nanoseconds > float64(math.MaxInt64) {
			return nil, fmt.Errorf("arrival offset %d overflows time.Duration", index+1)
		}
		offsets[index] = time.Duration(nanoseconds)
		if index > 0 && offsets[index] < offsets[index-1] {
			return nil, fmt.Errorf("arrival offsets decrease at sequence %d", index+1)
		}
	}
	return offsets, nil
}

func DurationArrivalOffsets(requestRate float64, duration time.Duration) ([]time.Duration, error) {
	count, err := PlannedArrivalCount(requestRate, duration)
	if err != nil {
		return nil, err
	}
	offsets, err := ArrivalOffsets(requestRate, count)
	if err != nil {
		return nil, err
	}
	for sequence, offset := range offsets {
		if offset >= duration {
			return nil, fmt.Errorf("arrival offset %d is not strictly before the duration boundary", sequence+1)
		}
	}
	return offsets, nil
}

func maxInt() int {
	return int(^uint(0) >> 1)
}
