package artifacts

import (
	"fmt"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
)

// CalculateSummary adapts validated lifecycle artifacts to the pure aggregate
// package. The aggregate engine does not depend on artifact persistence.
func CalculateSummary(metadata RunMetadata, requests []RequestArtifact, arrivals []benchmark.ArrivalRecord) (aggregate.RunSummary, error) {
	aggregateRequests := make([]aggregate.Request, 0, len(requests))
	for _, request := range requests {
		aggregateRequests = append(aggregateRequests, aggregate.Request{
			Sequence: request.Sequence,
			Phase:    request.Phase,
			Outcome:  request.Outcome,
			Metrics:  request.Metrics,
		})
	}
	input := aggregate.Input{
		RunID:      metadata.RunID,
		RunStatus:  metadata.RunStatus,
		ErrorClass: metadata.ErrorClass,
		Complete:   summaryComplete(metadata),
		Workload: aggregate.WorkloadInput{
			Mode:                     metadata.Workload.Mode,
			RequestedOutputMaxTokens: metadata.Workload.Output.RequestedMaxTokens,
		},
		Load: aggregate.LoadInput{Mode: metadata.Load.Mode},
		Measurement: aggregate.MeasurementInput{
			Requested:            metadata.Measurement.Requested,
			Attempted:            metadata.Measurement.Attempted,
			Completed:            metadata.Measurement.Completed,
			ElapsedNS:            metadata.Measurement.ElapsedNS,
			Outcomes:             metadata.Measurement.Outcomes,
			RequestedConcurrency: metadata.Measurement.RequestedConcurrency,
			WorkerCount:          metadata.Measurement.EffectiveWorkers,
			MaxObservedActive:    metadata.Measurement.MaxObservedActive,
			ArrivalCounts:        copyArrivalCounts(metadata.Measurement.Arrivals),
		},
		Requests: aggregateRequests,
		Arrivals: append([]benchmark.ArrivalRecord(nil), arrivals...),
		SLO:      metadata.SLO,
	}
	if metadata.Workload.Input != nil {
		input.Workload.InputTargetTokens = intPointer(metadata.Workload.Input.TargetTokens)
		input.Workload.InputResolvedTokens = intPointer(metadata.Workload.Input.ResolvedTokens)
	}
	switch metadata.Load.Mode {
	case benchmark.LoadModeClosedLoop:
		if metadata.Load.ClosedLoop != nil {
			input.Load.ClosedLoop = &aggregate.ClosedLoopInput{
				RequestedConcurrency: metadata.Load.ClosedLoop.RequestedConcurrency,
				RequestedRequests:    metadata.Load.ClosedLoop.RequestedRequests,
			}
		}
	case benchmark.LoadModeOpenLoop:
		if metadata.Load.OpenLoop != nil {
			duration, err := time.ParseDuration(metadata.Load.OpenLoop.Duration)
			if err != nil {
				return aggregate.RunSummary{}, fmt.Errorf("parse open-loop duration for summary: %w", err)
			}
			input.Load.OpenLoop = &aggregate.OpenLoopInput{
				ConfiguredRequestRate: metadata.Load.OpenLoop.RequestRate,
				Duration:              duration,
				MaxInFlight:           metadata.Load.OpenLoop.MaxInFlight,
				PlannedArrivals:       metadata.Load.OpenLoop.PlannedArrivals,
			}
		}
	}
	return aggregate.Calculate(input)
}

func summaryComplete(metadata RunMetadata) bool {
	if metadata.Drain.TimedOut || metadata.Drain.ParentCancelled {
		return false
	}
	switch metadata.Load.Mode {
	case benchmark.LoadModeClosedLoop:
		return metadata.Measurement.Attempted == metadata.Measurement.Requested
	case benchmark.LoadModeOpenLoop:
		return metadata.Measurement.Arrivals != nil && metadata.Measurement.Arrivals.UnprocessedDueToCancellation == 0 && metadata.Measurement.Arrivals.Processed == metadata.Measurement.Arrivals.Planned
	default:
		return false
	}
}

func copyArrivalCounts(value *benchmark.ArrivalCounts) *benchmark.ArrivalCounts {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func intPointer(value int) *int {
	copy := value
	return &copy
}
