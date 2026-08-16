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
		RequestID:         observation.RequestID,
		TimeToHeaders:     durationScalar(observation.RequestStartedAt, observation.HeadersReceivedAt, "headers timestamp not available"),
		TTFB:              durationScalar(observation.RequestStartedAt, observation.FirstByteAt, "true first-byte timestamp not available"),
		TTFT:              durationScalar(observation.RequestStartedAt, observation.FirstContentAt, "first content timestamp not available"),
		TTLT:              durationScalar(observation.RequestStartedAt, observation.LastContentAt, "last content timestamp not available"),
		E2E:               durationScalar(observation.RequestStartedAt, observation.CompletedAt, "completion timestamp not available"),
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

func durationScalar(start, end *time.Time, missingReason string) Scalar {
	if start == nil || end == nil {
		return unavailable("ms", missingReason)
	}
	if end.Before(*start) {
		return unavailable("ms", "end timestamp precedes start timestamp")
	}
	return available(end.Sub(*start).Seconds()*1000, "ms")
}

func calculateInterChunkLatency(contentEvents []benchmark.StreamEvent) InterChunkLatency {
	result := InterChunkLatency{ValuesMS: make([]float64, 0)}
	if len(contentEvents) < 2 {
		result.Reason = "at least two content events are required"
		return result
	}

	values := make([]float64, 0, len(contentEvents)-1)
	for index := 1; index < len(contentEvents); index++ {
		gap := contentEvents[index].ReceivedAt.Sub(contentEvents[index-1].ReceivedAt)
		if gap < 0 {
			result.Reason = "content event timestamps are not ordered"
			return result
		}
		values = append(values, gap.Seconds()*1000)
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
	window, reason := decodeWindow(observation, contentEventCount)
	if reason != "" {
		return unavailable("ms/token", reason)
	}
	return available(window.Seconds()*1000/float64(observation.Usage.OutputTokens-1), "ms/token")
}

func calculateOutputRate(observation benchmark.RequestObservation) Scalar {
	if !observation.Usage.Available {
		return unavailable("tokens/s", "server token usage not available")
	}
	if observation.Usage.OutputTokens < 0 {
		return unavailable("tokens/s", "output token count is negative")
	}
	if observation.RequestStartedAt == nil || observation.CompletedAt == nil {
		return unavailable("tokens/s", "end-to-end timestamps not available")
	}
	duration := observation.CompletedAt.Sub(*observation.RequestStartedAt)
	if duration <= 0 {
		return unavailable("tokens/s", "end-to-end duration must be positive")
	}
	return available(float64(observation.Usage.OutputTokens)/duration.Seconds(), "tokens/s")
}

func calculateDecodeRate(observation benchmark.RequestObservation, contentEventCount int) Scalar {
	window, reason := decodeWindow(observation, contentEventCount)
	if reason != "" {
		return unavailable("tokens/s", reason)
	}
	return available(float64(observation.Usage.OutputTokens-1)/window.Seconds(), "tokens/s")
}

func decodeWindow(observation benchmark.RequestObservation, contentEventCount int) (time.Duration, string) {
	if !observation.Usage.Available {
		return 0, "server token usage not available"
	}
	if observation.Usage.OutputTokens <= 1 {
		return 0, "more than one output token is required"
	}
	if contentEventCount < 2 {
		return 0, "at least two content events are required"
	}
	if observation.FirstContentAt == nil || observation.LastContentAt == nil {
		return 0, "first and last content timestamps are required"
	}
	window := observation.LastContentAt.Sub(*observation.FirstContentAt)
	if window <= 0 {
		return 0, "content generation duration must be positive"
	}
	return window, ""
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
