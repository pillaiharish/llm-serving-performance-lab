package metrics

import (
	"fmt"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
)

func Calculate(observation benchmark.RequestObservation) RequestMetrics {
	contentEvents := make([]benchmark.StreamEvent, 0, len(observation.StreamEvents))
	responseBytes := 0
	for _, event := range observation.StreamEvents {
		if event.HasContent {
			contentEvents = append(contentEvents, event)
			responseBytes += event.ContentBytes
		}
	}

	result := RequestMetrics{
		RunID:             observation.RunID,
		RequestID:         observation.RequestID,
		TimeToHeaders:     durationScalar(observation.HeadersAfterNS, observation.RequestStartedAt, observation.HeadersReceivedAt, "headers timestamp not available"),
		TTFB:              durationScalar(observation.FirstByteAfterNS, observation.RequestStartedAt, observation.FirstByteAt, "true first-byte timestamp not available"),
		TTFT:              durationScalar(observation.FirstContentAfterNS, observation.RequestStartedAt, observation.FirstContentAt, "first content timestamp not available"),
		TTLT:              durationScalar(observation.LastContentAfterNS, observation.RequestStartedAt, observation.LastContentAt, "last content timestamp not available"),
		E2E:               durationScalar(observation.CompletedAfterNS, observation.RequestStartedAt, observation.CompletedAt, "completion timestamp not available"),
		TokenUsage:        observation.Usage,
		StreamEventCount:  len(observation.StreamEvents),
		ContentEventCount: len(contentEvents),
		ResponseBytes:     responseBytes,
		ResponseBodyBytes: observation.ResponseBodyBytes,
		ITL: ITLAvailability{
			Available: false,
			Source:    benchmark.TokenUsageSourceUnavailable,
			Reason:    TrueITLReason,
		},
	}
	result.InterChunkLatency = calculateInterChunkLatency(contentEvents)
	result.TPOT = calculateTPOT(observation, len(contentEvents))
	result.OutputTokensPerSecond = calculateOutputRate(observation)
	result.DecodeTokensPerSecond = calculateDecodeRate(observation, len(contentEvents))
	return result
}

func durationScalar(offsetNS *int64, start, end *time.Time, missingReason string) Scalar {
	durationNS, reason := elapsedDurationNS(offsetNS, start, end, missingReason)
	if reason != "" {
		return unavailable("ms", reason)
	}
	return available(float64(durationNS)/float64(time.Millisecond), "ms")
}

func calculateInterChunkLatency(contentEvents []benchmark.StreamEvent) InterChunkLatency {
	result := InterChunkLatency{ValuesMS: make([]float64, 0)}
	if len(contentEvents) < 2 {
		result.Reason = "at least two content events are required"
		return result
	}

	values := make([]float64, 0, len(contentEvents)-1)
	if contentEvents[0].ReceivedAfterNS < 0 {
		result.Reason = "content event relative offsets must not be negative"
		return result
	}
	for index := 1; index < len(contentEvents); index++ {
		if contentEvents[index].ReceivedAfterNS < 0 {
			result.Reason = "content event relative offsets must not be negative"
			return result
		}
		gapNS := contentEvents[index].ReceivedAfterNS - contentEvents[index-1].ReceivedAfterNS
		if gapNS < 0 {
			result.Reason = "content event relative offsets are not ordered"
			return result
		}
		values = append(values, float64(gapNS)/float64(time.Millisecond))
	}

	minimum := values[0]
	maximum := values[0]
	total := 0.0
	for _, value := range values {
		total += value
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	result.Available = true
	result.Count = len(values)
	result.MeanMS = total / float64(len(values))
	result.MinMS = minimum
	result.MaxMS = maximum
	result.ValuesMS = values
	return result
}

func calculateTPOT(observation benchmark.RequestObservation, contentEventCount int) Scalar {
	windowNS, reason := decodeWindowNS(observation, contentEventCount)
	if reason != "" {
		return unavailable("ms/token", reason)
	}
	return available(float64(windowNS)/float64(time.Millisecond)/float64(observation.Usage.OutputTokens-1), "ms/token")
}

func calculateOutputRate(observation benchmark.RequestObservation) Scalar {
	if !observation.Usage.Available {
		return unavailable("tokens/s", "server token usage not available")
	}
	if observation.Usage.OutputTokens < 0 {
		return unavailable("tokens/s", "output token count is negative")
	}
	durationNS, reason := elapsedDurationNS(observation.CompletedAfterNS, observation.RequestStartedAt, observation.CompletedAt, "end-to-end timestamps not available")
	if reason != "" {
		return unavailable("tokens/s", reason)
	}
	if durationNS <= 0 {
		return unavailable("tokens/s", "end-to-end duration must be positive")
	}
	return available(float64(observation.Usage.OutputTokens)/(float64(durationNS)/float64(time.Second)), "tokens/s")
}

func calculateDecodeRate(observation benchmark.RequestObservation, contentEventCount int) Scalar {
	windowNS, reason := decodeWindowNS(observation, contentEventCount)
	if reason != "" {
		return unavailable("tokens/s", reason)
	}
	return available(float64(observation.Usage.OutputTokens-1)/(float64(windowNS)/float64(time.Second)), "tokens/s")
}

func decodeWindowNS(observation benchmark.RequestObservation, contentEventCount int) (int64, string) {
	if !observation.Usage.Available {
		return 0, "server token usage not available"
	}
	if observation.Usage.OutputTokens <= 1 {
		return 0, "more than one output token is required"
	}
	if contentEventCount < 2 {
		return 0, "at least two content events are required"
	}
	if observation.FirstContentAfterNS != nil || observation.LastContentAfterNS != nil {
		if observation.FirstContentAfterNS == nil || observation.LastContentAfterNS == nil {
			return 0, "first and last content relative offsets must both be available"
		}
		if *observation.FirstContentAfterNS < 0 || *observation.LastContentAfterNS < 0 {
			return 0, "content relative offsets must not be negative"
		}
		windowNS := *observation.LastContentAfterNS - *observation.FirstContentAfterNS
		if windowNS <= 0 {
			return 0, "content generation duration must be positive"
		}
		return windowNS, ""
	}
	if observation.FirstContentAt == nil || observation.LastContentAt == nil {
		return 0, "first and last content timestamps are required"
	}
	window := observation.LastContentAt.Sub(*observation.FirstContentAt)
	if window <= 0 {
		return 0, "content generation duration must be positive"
	}
	return window.Nanoseconds(), ""
}

func elapsedDurationNS(offsetNS *int64, start, end *time.Time, missingReason string) (int64, string) {
	if offsetNS != nil {
		if *offsetNS < 0 {
			return 0, "relative offset must not be negative"
		}
		return *offsetNS, ""
	}
	if start == nil || end == nil {
		return 0, missingReason
	}
	if end.Before(*start) {
		return 0, "end timestamp precedes start timestamp"
	}
	return end.Sub(*start).Nanoseconds(), ""
}

func available(value float64, unit string) Scalar {
	return Scalar{Available: true, Value: value, Unit: unit}
}

func unavailable(unit, reason string) Scalar {
	if reason == "" {
		reason = fmt.Sprintf("%s metric is not available", unit)
	}
	return Scalar{Available: false, Unit: unit, Reason: reason}
}
