package calibration

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
)

var CSVColumns = []string{
	"calibration_schema_version", "calibration_id", "point_index", "point_id", "point_status", "load_mode",
	"requested_concurrency", "configured_request_rate", "child_run_id", "child_run_path", "run_status", "delivery_clean", "delivery_reasons",
	"requested_or_planned", "started", "completed", "successful", "failed", "request_error", "request_timeout", "parent_cancelled", "drain_timeout",
	"successful_requests_per_second", "planned_arrivals", "processed_arrivals", "started_arrivals", "delivery_ratio", "actual_starts_per_second",
	"client_limited", "scheduler_limited", "unprocessed_due_to_cancellation", "scheduler_lag_p95_ms", "max_scheduler_lag_p95_ms",
	"resource_started_at", "resource_completed_at", "resource_elapsed_ns", "resource_sample_interval_ns", "resource_samples",
	"goroutines_start", "goroutines_observed_peak", "goroutines_end", "heap_alloc_start_bytes", "heap_alloc_observed_peak_bytes", "heap_alloc_end_bytes",
	"heap_sys_observed_peak_bytes", "sys_observed_peak_bytes", "total_alloc_delta_bytes", "mallocs_delta", "frees_delta", "num_gc_delta", "gc_pause_total_delta_ns",
}

type Writer struct {
	outputRoot string
}

func NewWriter(outputRoot string) *Writer { return &Writer{outputRoot: outputRoot} }

func (w *Writer) Write(manifest Manifest) (string, error) {
	if w == nil || w.outputRoot == "" {
		return "", fmt.Errorf("calibration output root is required")
	}
	if err := validateManifest(manifest); err != nil {
		return "", fmt.Errorf("validate calibration artifact: %w", err)
	}
	if err := w.validateReferencedEvidence(manifest); err != nil {
		return "", fmt.Errorf("validate calibration evidence: %w", err)
	}
	root := filepath.Join(w.outputRoot, "calibrations")
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", fmt.Errorf("create calibration artifact root: %w", err)
	}
	finalRoot := filepath.Join(root, manifest.CalibrationID)
	if _, err := os.Lstat(finalRoot); err == nil {
		return "", fmt.Errorf("calibration artifact directory already exists: %s", finalRoot)
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("inspect calibration artifact directory: %w", err)
	}
	stagingRoot, err := os.MkdirTemp(root, "."+manifest.CalibrationID+"-")
	if err != nil {
		return "", fmt.Errorf("create calibration staging directory: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = os.RemoveAll(stagingRoot)
		}
	}()

	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode calibration.json: %w", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(stagingRoot, "calibration.json"), encoded, 0o644); err != nil {
		return "", fmt.Errorf("write calibration.json: %w", err)
	}
	csvBytes, err := MarshalCSV(manifest)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(stagingRoot, "summary.csv"), csvBytes, 0o644); err != nil {
		return "", fmt.Errorf("write calibration summary.csv: %w", err)
	}
	if err := os.Rename(stagingRoot, finalRoot); err != nil {
		return "", fmt.Errorf("commit calibration artifact directory: %w", err)
	}
	committed = true
	return finalRoot, nil
}

func MarshalCSV(manifest Manifest) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(CSVColumns); err != nil {
		return nil, fmt.Errorf("write calibration CSV header: %w", err)
	}
	for _, point := range manifest.Points {
		record := calibrationCSVRecord(manifest, point)
		if len(record) != len(CSVColumns) {
			return nil, fmt.Errorf("calibration CSV point %d has %d fields, want %d", point.PointIndex, len(record), len(CSVColumns))
		}
		if err := writer.Write(record); err != nil {
			return nil, fmt.Errorf("write calibration CSV point %d: %w", point.PointIndex, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flush calibration CSV: %w", err)
	}
	return buffer.Bytes(), nil
}

func calibrationCSVRecord(manifest Manifest, point Point) []string {
	record := []string{
		strconv.Itoa(manifest.CalibrationSchemaVersion), manifest.CalibrationID, strconv.Itoa(point.PointIndex), point.PointID, point.PointStatus, string(manifest.LoadMode),
		optionalInt(point.RequestedConcurrency), optionalFloat(point.ConfiguredRequestRate), optionalString(point.ChildRunID), optionalString(point.ChildRunPath), optionalString(point.RunStatus), optionalBool(point.DeliveryClean), strings.Join(point.DeliveryReasons, ";"),
	}
	if point.Counts == nil {
		record = append(record, make([]string, 9)...)
	} else {
		counts := point.Counts
		record = append(record, strconv.Itoa(counts.RequestedOrPlanned), strconv.Itoa(counts.Started), strconv.Itoa(counts.Completed), strconv.Itoa(counts.Successful), strconv.Itoa(counts.Failed), strconv.Itoa(counts.RequestError), strconv.Itoa(counts.RequestTimeout), strconv.Itoa(counts.ParentCancelled), strconv.Itoa(counts.DrainTimeout))
	}
	record = append(record, optionalRate(point.SuccessfulRequestThroughput))
	if point.OpenLoop == nil {
		record = append(record, make([]string, 8)...)
	} else {
		open := point.OpenLoop
		record = append(record, strconv.Itoa(open.PlannedArrivals), strconv.Itoa(open.ProcessedArrivals), strconv.Itoa(open.StartedArrivals), availableRatio(open.DeliveryRatio), availableRate(open.ActualStartRate), strconv.Itoa(open.ClientLimited), strconv.Itoa(open.SchedulerLimited), strconv.Itoa(open.UnprocessedDueToCancellation))
	}
	record = append(record, optionalDistributionP95(point.SchedulerLag), optionalFloat(manifest.MaxSchedulerLagP95MS))
	if point.Resource == nil {
		return append(record, make([]string, 18)...)
	}
	resource := point.Resource
	return append(record,
		resource.StartedAt.Format(timeFormat), resource.CompletedAt.Format(timeFormat), strconv.FormatInt(resource.ElapsedNS, 10), strconv.FormatInt(resource.SampleIntervalNS, 10), strconv.Itoa(resource.Samples),
		strconv.Itoa(resource.GoroutinesStart), strconv.Itoa(resource.GoroutinesObservedPeak), strconv.Itoa(resource.GoroutinesEnd), strconv.FormatUint(resource.HeapAllocStartBytes, 10), strconv.FormatUint(resource.HeapAllocObservedPeakBytes, 10), strconv.FormatUint(resource.HeapAllocEndBytes, 10),
		strconv.FormatUint(resource.HeapSysObservedPeakBytes, 10), strconv.FormatUint(resource.SysObservedPeakBytes, 10), strconv.FormatUint(resource.TotalAllocDeltaBytes, 10), strconv.FormatUint(resource.MallocsDelta, 10), strconv.FormatUint(resource.FreesDelta, 10), strconv.FormatUint(uint64(resource.NumGCDelta), 10), strconv.FormatUint(resource.GCPauseTotalDeltaNS, 10),
	)
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

func validateManifest(manifest Manifest) error {
	if manifest.CalibrationSchemaVersion != SchemaVersion || manifest.CalibrationID == "" || filepath.Base(manifest.CalibrationID) != manifest.CalibrationID || manifest.CalibrationID == "." {
		return fmt.Errorf("invalid calibration schema or identity")
	}
	if manifest.CreatedAt.IsZero() || manifest.CompletedAt.IsZero() || manifest.CompletedAt.Before(manifest.CreatedAt) {
		return fmt.Errorf("invalid calibration timestamps")
	}
	if manifest.ExperimentID == "" || manifest.ExperimentPath != filepath.ToSlash(filepath.Join("experiments", manifest.ExperimentID)) {
		return fmt.Errorf("invalid experiment identity or path")
	}
	if len(manifest.Points) != manifest.PlannedPoints {
		return fmt.Errorf("calibration point counts are inconsistent")
	}
	evaluated, clean, dirty, unevaluated := 0, 0, 0, 0
	for index, point := range manifest.Points {
		if point.PointIndex != index+1 || point.PointID == "" {
			return fmt.Errorf("point %d has invalid identity", index+1)
		}
		if point.DeliveryClean == nil {
			unevaluated++
			if len(point.DeliveryReasons) != 0 || point.Counts != nil || point.SuccessfulRequestThroughput != nil || point.ClosedLoop != nil || point.OpenLoop != nil || point.SchedulerLag != nil || point.RunStatus != nil {
				return fmt.Errorf("unevaluated point %d contains child result evidence", point.PointIndex)
			}
		} else if point.Counts == nil || point.RunStatus == nil || point.Resource == nil {
			return fmt.Errorf("evaluated point %d lacks result or resource evidence", point.PointIndex)
		} else {
			evaluated++
			if *point.DeliveryClean {
				clean++
				if len(point.DeliveryReasons) != 0 {
					return fmt.Errorf("clean point %d contains delivery reasons", point.PointIndex)
				}
			} else {
				dirty++
				if len(point.DeliveryReasons) == 0 {
					return fmt.Errorf("dirty point %d lacks delivery reasons", point.PointIndex)
				}
			}
			for _, reason := range point.DeliveryReasons {
				if !knownDeliveryReason(reason) {
					return fmt.Errorf("point %d contains unknown delivery reason %q", point.PointIndex, reason)
				}
			}
		}
		if point.ChildRunPath != nil {
			if point.ChildRunID == nil || filepath.Base(*point.ChildRunID) != *point.ChildRunID || *point.ChildRunID == "." || *point.ChildRunPath != filepath.ToSlash(filepath.Join("experiments", manifest.ExperimentID, "runs", *point.ChildRunID)) {
				return fmt.Errorf("point %d has invalid child identity or path", point.PointIndex)
			}
		}
		if point.Resource != nil {
			resource := point.Resource
			if resource.StartedAt.IsZero() || resource.CompletedAt.Before(resource.StartedAt) || resource.ElapsedNS < 0 || resource.SampleIntervalNS <= 0 || resource.Samples < 2 || resource.GoroutinesObservedPeak < resource.GoroutinesStart || resource.GoroutinesObservedPeak < resource.GoroutinesEnd || resource.HeapAllocObservedPeakBytes < resource.HeapAllocStartBytes || resource.HeapAllocObservedPeakBytes < resource.HeapAllocEndBytes {
				return fmt.Errorf("point %d contains invalid resource evidence", point.PointIndex)
			}
		}
	}
	if manifest.EvaluatedPoints != evaluated || manifest.CleanPoints != clean || manifest.DirtyPoints != dirty || manifest.UnevaluatedPoints != unevaluated {
		return fmt.Errorf("calibration point totals do not match point records")
	}
	return nil
}

func (w *Writer) validateReferencedEvidence(manifest Manifest) error {
	experimentRoot := filepath.Join(w.outputRoot, filepath.FromSlash(manifest.ExperimentPath))
	var experimentManifest experiment.Manifest
	if err := readJSON(filepath.Join(experimentRoot, "experiment.json"), &experimentManifest); err != nil {
		return fmt.Errorf("read referenced experiment: %w", err)
	}
	if experimentManifest.ExperimentSchemaVersion != experiment.SchemaVersion {
		return fmt.Errorf("referenced experiment schema is %d, want %d", experimentManifest.ExperimentSchemaVersion, experiment.SchemaVersion)
	}
	if experimentManifest.ExperimentID != manifest.ExperimentID {
		return fmt.Errorf("referenced experiment ID does not match calibration")
	}
	if experimentManifest.LoadMode != manifest.LoadMode {
		return fmt.Errorf("referenced experiment load mode does not match calibration")
	}
	if experimentManifest.Status != manifest.Status || experimentManifest.Complete != manifest.Complete {
		return fmt.Errorf("referenced experiment status or completeness does not match calibration")
	}
	if experimentManifest.PlannedPoints != manifest.PlannedPoints || len(experimentManifest.Points) != len(manifest.Points) {
		return fmt.Errorf("referenced experiment planned points do not match calibration")
	}
	if !slices.Equal(experimentManifest.Axes.ConcurrencyValues, manifest.ConcurrencyValues) || !slices.Equal(experimentManifest.Axes.RequestRateValues, manifest.RequestRateValues) {
		return fmt.Errorf("referenced experiment load axes do not match calibration")
	}

	for index := range manifest.Points {
		point := manifest.Points[index]
		experimentPoint := experimentManifest.Points[index]
		if point.PointIndex != experimentPoint.PointIndex || point.PointID != experimentPoint.PointID || point.PointStatus != experimentPoint.PointStatus {
			return fmt.Errorf("point %d identity or status does not match referenced experiment", point.PointIndex)
		}
		if !reflect.DeepEqual(point.RequestedConcurrency, experimentPoint.Parameters.Concurrency) || !reflect.DeepEqual(point.ConfiguredRequestRate, experimentPoint.Parameters.RequestRate) {
			return fmt.Errorf("point %d load parameters do not match referenced experiment", point.PointIndex)
		}
		expectedChildPath := calibrationChildPath(manifest.ExperimentID, experimentPoint.RunPath)
		if !reflect.DeepEqual(point.ChildRunID, experimentPoint.RunID) || !reflect.DeepEqual(point.ChildRunPath, expectedChildPath) || !reflect.DeepEqual(point.RunStatus, experimentPoint.RunStatus) {
			return fmt.Errorf("point %d child identity, path, or status does not match referenced experiment", point.PointIndex)
		}

		if point.DeliveryClean == nil {
			if experimentPoint.PointStatus == experiment.PointExecuted {
				return fmt.Errorf("unevaluated point %d references executed child evidence", point.PointIndex)
			}
			continue
		}
		if experimentPoint.PointStatus != experiment.PointExecuted || experimentPoint.RunID == nil || experimentPoint.RunPath == nil || experimentPoint.RunStatus == nil {
			return fmt.Errorf("evaluated point %d lacks executed experiment child evidence", point.PointIndex)
		}
		if filepath.Base(*experimentPoint.RunID) != *experimentPoint.RunID || *experimentPoint.RunID == "." || *experimentPoint.RunPath != filepath.ToSlash(filepath.Join("runs", *experimentPoint.RunID)) {
			return fmt.Errorf("evaluated point %d has unsafe experiment child identity or path", point.PointIndex)
		}
		if err := validatePointEvidence(experimentRoot, manifest, point, experimentPoint); err != nil {
			return fmt.Errorf("point %d: %w", point.PointIndex, err)
		}
	}
	return nil
}

func validatePointEvidence(experimentRoot string, manifest Manifest, point Point, experimentPoint experiment.PointRecord) error {
	childRoot := filepath.Join(experimentRoot, filepath.FromSlash(*experimentPoint.RunPath))
	var metadata artifacts.RunMetadata
	if err := readJSON(filepath.Join(childRoot, "run.json"), &metadata); err != nil {
		return fmt.Errorf("read child run.json: %w", err)
	}
	var summary aggregate.RunSummary
	if err := readJSON(filepath.Join(childRoot, "summary.json"), &summary); err != nil {
		return fmt.Errorf("read child summary.json: %w", err)
	}
	if metadata.SchemaVersion != artifacts.SchemaVersion || summary.SchemaVersion != aggregate.SchemaVersion {
		return fmt.Errorf("child schema is not %d", artifacts.SchemaVersion)
	}
	if metadata.RunID != *point.ChildRunID || summary.RunID != *point.ChildRunID || metadata.RunStatus != *point.RunStatus || summary.RunStatus != *point.RunStatus {
		return fmt.Errorf("child run identity or status does not match calibration")
	}
	if metadata.Load.Mode != summary.Load.Mode || config.LoadMode(summary.Load.Mode) != manifest.LoadMode {
		return fmt.Errorf("child load mode does not match calibration")
	}
	if point.Counts == nil || !reflect.DeepEqual(*point.Counts, summary.Counts) {
		return fmt.Errorf("request counts differ from child summary")
	}
	if point.SuccessfulRequestThroughput == nil || !reflect.DeepEqual(*point.SuccessfulRequestThroughput, summary.RequestRates.SuccessfulRequestThroughput) {
		return fmt.Errorf("successful request throughput differs from child summary")
	}
	switch manifest.LoadMode {
	case config.LoadModeClosedLoop:
		if point.OpenLoop != nil || point.SchedulerLag != nil || point.ClosedLoop == nil || summary.Load.ClosedLoop == nil || summary.Load.OpenLoop != nil || !reflect.DeepEqual(*point.ClosedLoop, *summary.Load.ClosedLoop) {
			return fmt.Errorf("closed-loop evidence differs from child summary")
		}
		if experimentPoint.Parameters.Concurrency == nil || experimentPoint.Parameters.RequestRate != nil || metadata.Load.ClosedLoop == nil || metadata.Load.OpenLoop != nil || metadata.Load.ClosedLoop.RequestedConcurrency != *experimentPoint.Parameters.Concurrency || summary.Load.ClosedLoop.RequestedConcurrency != *experimentPoint.Parameters.Concurrency {
			return fmt.Errorf("closed-loop child configuration differs from experiment point")
		}
	case config.LoadModeOpenLoop:
		if point.ClosedLoop != nil || point.OpenLoop == nil || summary.Load.OpenLoop == nil || summary.Load.ClosedLoop != nil || !reflect.DeepEqual(*point.OpenLoop, *summary.Load.OpenLoop) {
			return fmt.Errorf("open-loop evidence differs from child summary")
		}
		if point.SchedulerLag == nil || !reflect.DeepEqual(*point.SchedulerLag, summary.SchedulerLag) {
			return fmt.Errorf("scheduler-lag evidence differs from child summary")
		}
		if experimentPoint.Parameters.RequestRate == nil || experimentPoint.Parameters.Concurrency != nil || metadata.Load.OpenLoop == nil || metadata.Load.ClosedLoop != nil || metadata.Load.OpenLoop.RequestRate != *experimentPoint.Parameters.RequestRate || summary.Load.OpenLoop.ConfiguredRequestRate != *experimentPoint.Parameters.RequestRate {
			return fmt.Errorf("open-loop child configuration differs from experiment point")
		}
	default:
		return fmt.Errorf("invalid calibration load mode %q", manifest.LoadMode)
	}
	clean, reasons := EvaluateEvidence(summary, metadata, manifest.MaxSchedulerLagP95MS)
	if *point.DeliveryClean != clean || !reflect.DeepEqual(point.DeliveryReasons, reasons) {
		return fmt.Errorf("delivery-clean result or ordered reasons differ from child evidence")
	}
	return nil
}

func readJSON(path string, destination any) error {
	encoded, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(encoded, destination); err != nil {
		return err
	}
	return nil
}

func optionalInt(value *int) string {
	if value == nil {
		return ""
	}
	return strconv.Itoa(*value)
}

func optionalFloat(value *float64) string {
	if value == nil {
		return ""
	}
	return strconv.FormatFloat(*value, 'g', -1, 64)
}

func optionalString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func optionalBool(value *bool) string {
	if value == nil {
		return ""
	}
	return strconv.FormatBool(*value)
}

func optionalRate(value *aggregate.Rate) string {
	if value == nil || !value.Available {
		return ""
	}
	return strconv.FormatFloat(value.Value, 'g', -1, 64)
}

func availableRate(value aggregate.Rate) string {
	if !value.Available {
		return ""
	}
	return strconv.FormatFloat(value.Value, 'g', -1, 64)
}

func availableRatio(value aggregate.Ratio) string {
	if !value.Available {
		return ""
	}
	return strconv.FormatFloat(value.Value, 'g', -1, 64)
}

func optionalDistributionP95(value *aggregate.Distribution) string {
	if value == nil || !value.Available {
		return ""
	}
	return strconv.FormatFloat(value.P95, 'g', -1, 64)
}
