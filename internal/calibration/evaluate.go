package calibration

import (
	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
)

func Evaluate(result benchmarkexec.Result, maxSchedulerLagP95MS *float64) (bool, []string) {
	return EvaluateEvidence(result.Summary, result.Metadata, maxSchedulerLagP95MS)
}

// EvaluateEvidence applies the calibration delivery contract to the persisted
// schema-7 evidence used to publish a calibration point. Keeping this function
// independent of benchmarkexec.Result lets Writer defensively repeat the same
// evaluation after loading the referenced child artifacts.
func EvaluateEvidence(summary aggregate.RunSummary, metadata artifacts.RunMetadata, maxSchedulerLagP95MS *float64) (bool, []string) {
	reasons := make([]string, 0)
	if summary.RunStatus != artifacts.RunStatusCompleted || !summary.Complete {
		reasons = append(reasons, ReasonRunNotCompleted)
	}
	if summary.Counts.Failed != 0 || summary.Counts.RequestError != 0 || summary.Counts.RequestTimeout != 0 || summary.Counts.ParentCancelled != 0 || summary.Counts.DrainTimeout != 0 {
		reasons = append(reasons, ReasonRequestFailuresPresent)
	}

	switch config.LoadMode(summary.Load.Mode) {
	case config.LoadModeClosedLoop:
		if summary.Counts.RequestedOrPlanned != metadata.Measurement.Attempted || summary.Counts.RequestedOrPlanned != summary.Counts.Completed || summary.Counts.RequestedOrPlanned != summary.Counts.Successful {
			reasons = append(reasons, ReasonRequestsNotFullyAttempted)
		}
	case config.LoadModeOpenLoop:
		open := summary.Load.OpenLoop
		if open == nil {
			reasons = append(reasons, ReasonPlannedArrivalsNotProcessed)
			break
		}
		if open.ProcessedArrivals != open.PlannedArrivals {
			reasons = append(reasons, ReasonPlannedArrivalsNotProcessed)
		}
		if !open.DeliveryRatio.Available || open.DeliveryRatio.Numerator != open.DeliveryRatio.Denominator || open.DeliveryRatio.Value != 1 {
			reasons = append(reasons, ReasonDeliveryRatioBelowOne)
		}
		if open.ClientLimited != 0 {
			reasons = append(reasons, ReasonClientLimited)
		}
		if open.SchedulerLimited != 0 {
			reasons = append(reasons, ReasonSchedulerLimited)
		}
		if open.UnprocessedDueToCancellation != 0 {
			reasons = append(reasons, ReasonCancellationUnprocessed)
		}
		if maxSchedulerLagP95MS != nil {
			if !summary.SchedulerLag.Available {
				reasons = append(reasons, ReasonSchedulerLagUnavailable)
			} else if summary.SchedulerLag.P95 > *maxSchedulerLagP95MS {
				reasons = append(reasons, ReasonSchedulerLagExceeded)
			}
		}
	default:
		reasons = append(reasons, ReasonRunNotCompleted)
	}
	return len(reasons) == 0, reasons
}

func knownDeliveryReason(reason string) bool {
	switch reason {
	case ReasonRunNotCompleted,
		ReasonRequestsNotFullyAttempted,
		ReasonRequestFailuresPresent,
		ReasonPlannedArrivalsNotProcessed,
		ReasonDeliveryRatioBelowOne,
		ReasonClientLimited,
		ReasonSchedulerLimited,
		ReasonCancellationUnprocessed,
		ReasonSchedulerLagUnavailable,
		ReasonSchedulerLagExceeded:
		return true
	default:
		return false
	}
}

func pointFromResult(record experimentPoint, result benchmarkexec.Result, resource ResourceEvidence, experimentID string, maxSchedulerLagP95MS *float64) Point {
	clean, reasons := Evaluate(result, maxSchedulerLagP95MS)
	counts := result.Summary.Counts
	throughput := result.Summary.RequestRates.SuccessfulRequestThroughput
	point := Point{
		PointIndex:                  record.index,
		PointID:                     record.id,
		PointStatus:                 record.status,
		RequestedConcurrency:        copyInt(record.concurrency),
		ConfiguredRequestRate:       copyFloat(record.requestRate),
		ChildRunID:                  copyString(record.runID),
		ChildRunPath:                calibrationChildPath(experimentID, record.runPath),
		RunStatus:                   copyString(record.runStatus),
		DeliveryClean:               boolPointer(clean),
		DeliveryReasons:             reasons,
		Counts:                      &counts,
		SuccessfulRequestThroughput: &throughput,
		ClosedLoop:                  result.Summary.Load.ClosedLoop,
		OpenLoop:                    result.Summary.Load.OpenLoop,
		Resource:                    &resource,
	}
	if result.Summary.Load.OpenLoop != nil {
		lag := result.Summary.SchedulerLag
		point.SchedulerLag = &lag
	}
	return point
}

// Compile-time use keeps the authoritative aggregate type import visible in
// this evaluator's public artifact contract.
var _ aggregate.RequestCounts
