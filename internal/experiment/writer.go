package experiment

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
)

var CSVColumns = append([]string{
	"experiment_id", "point_index", "point_id", "point_status", "point_error_class",
	"concurrency", "request_rate", "input_tokens", "requested_output_tokens", "child_run_path",
}, aggregate.CSVColumns...)

type publication struct {
	experimentRoot string
	stagingRoot    string
	finalRoot      string
	committed      bool
}

func beginPublication(outputRoot, experimentID string) (*publication, error) {
	if outputRoot == "" {
		return nil, fmt.Errorf("experiment output root is required")
	}
	if experimentID == "" || filepath.Base(experimentID) != experimentID || experimentID == "." {
		return nil, fmt.Errorf("invalid experiment ID")
	}
	experimentRoot := filepath.Join(outputRoot, "experiments")
	if err := os.MkdirAll(experimentRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create experiment artifact root: %w", err)
	}
	finalRoot := filepath.Join(experimentRoot, experimentID)
	if _, err := os.Lstat(finalRoot); err == nil {
		return nil, fmt.Errorf("experiment artifact directory already exists: %s", finalRoot)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect experiment artifact directory: %w", err)
	}
	stagingRoot, err := os.MkdirTemp(experimentRoot, "."+experimentID+"-")
	if err != nil {
		return nil, fmt.Errorf("create experiment staging directory: %w", err)
	}
	publication := &publication{experimentRoot: experimentRoot, stagingRoot: stagingRoot, finalRoot: finalRoot}
	if err := os.Mkdir(filepath.Join(stagingRoot, "runs"), 0o755); err != nil {
		publication.Abort()
		return nil, fmt.Errorf("create experiment child-run root: %w", err)
	}
	return publication, nil
}

func (p *publication) RunsRoot() string {
	if p == nil {
		return ""
	}
	return filepath.Join(p.stagingRoot, "runs")
}

func (p *publication) Abort() {
	if p != nil && !p.committed && p.stagingRoot != "" {
		_ = os.RemoveAll(p.stagingRoot)
	}
}

func (p *publication) Publish(manifest Manifest, plan Plan, summaries map[int]aggregate.RunSummary) (string, error) {
	if p == nil || p.stagingRoot == "" || p.finalRoot == "" {
		return "", fmt.Errorf("experiment publication is not initialized")
	}
	if err := validatePublication(p.stagingRoot, manifest, plan, summaries); err != nil {
		return "", fmt.Errorf("validate experiment artifact: %w", err)
	}
	encodedManifest, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode experiment.json: %w", err)
	}
	encodedManifest = append(encodedManifest, '\n')
	if err := os.WriteFile(filepath.Join(p.stagingRoot, "experiment.json"), encodedManifest, 0o644); err != nil {
		return "", fmt.Errorf("write experiment.json: %w", err)
	}
	encodedCSV, err := MarshalCSV(manifest, summaries)
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(p.stagingRoot, "summary.csv"), encodedCSV, 0o644); err != nil {
		return "", fmt.Errorf("write experiment summary.csv: %w", err)
	}
	if err := os.Rename(p.stagingRoot, p.finalRoot); err != nil {
		return "", fmt.Errorf("commit experiment artifact directory: %w", err)
	}
	p.committed = true
	return p.finalRoot, nil
}

func MarshalCSV(manifest Manifest, summaries map[int]aggregate.RunSummary) ([]byte, error) {
	var buffer bytes.Buffer
	writer := csv.NewWriter(&buffer)
	if err := writer.Write(CSVColumns); err != nil {
		return nil, fmt.Errorf("write experiment CSV header: %w", err)
	}
	for _, point := range manifest.Points {
		record := []string{
			manifest.ExperimentID,
			strconv.Itoa(point.PointIndex),
			point.PointID,
			point.PointStatus,
			point.ErrorClass,
			optionalInt(point.Parameters.Concurrency),
			optionalFloat(point.Parameters.RequestRate),
			optionalInt(point.Parameters.InputTokens),
			strconv.Itoa(point.Parameters.RequestedOutputTokens),
			optionalString(point.RunPath),
		}
		if summary, exists := summaries[point.PointIndex]; exists {
			record = append(record, aggregate.CSVRecord(summary)...)
		} else {
			record = append(record, make([]string, len(aggregate.CSVColumns))...)
		}
		if len(record) != len(CSVColumns) {
			return nil, fmt.Errorf("experiment CSV point %d has %d fields, want %d", point.PointIndex, len(record), len(CSVColumns))
		}
		if err := writer.Write(record); err != nil {
			return nil, fmt.Errorf("write experiment CSV point %d: %w", point.PointIndex, err)
		}
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return nil, fmt.Errorf("flush experiment CSV: %w", err)
	}
	return buffer.Bytes(), nil
}

func validatePublication(stagingRoot string, manifest Manifest, plan Plan, summaries map[int]aggregate.RunSummary) error {
	if manifest.ExperimentSchemaVersion != SchemaVersion || manifest.ExperimentID == "" || filepath.Base(manifest.ExperimentID) != manifest.ExperimentID || manifest.ExperimentID == "." {
		return fmt.Errorf("invalid experiment schema or identity")
	}
	if manifest.CreatedAt.IsZero() || manifest.CompletedAt.IsZero() || manifest.CompletedAt.Before(manifest.CreatedAt) {
		return fmt.Errorf("invalid experiment timestamps")
	}
	if len(manifest.Points) != len(plan.Points) || manifest.PlannedPoints != len(plan.Points) || !reflect.DeepEqual(manifest.Axes, plan.Axes) {
		return fmt.Errorf("manifest does not match experiment plan")
	}
	counts := manifest
	updateManifestCounts(&counts)
	if counts.ExecutedPoints != manifest.ExecutedPoints || counts.PreflightFailedPoints != manifest.PreflightFailedPoints || counts.CancelledPoints != manifest.CancelledPoints || counts.NotStartedPoints != manifest.NotStartedPoints || counts.ChildRunsCompleted != manifest.ChildRunsCompleted || counts.ChildRunsFailed != manifest.ChildRunsFailed || counts.ChildRunsCancelled != manifest.ChildRunsCancelled {
		return fmt.Errorf("manifest count fields do not match point records")
	}
	switch manifest.Status {
	case StatusCompleted:
		if !manifest.Complete || manifest.NotStartedPoints != 0 || manifest.CancelledPoints != 0 {
			return fmt.Errorf("completed experiment has incomplete point dispositions")
		}
	case StatusCancelled:
		if manifest.Complete || (manifest.ChildRunsCancelled == 0 && manifest.CancelledPoints == 0 && manifest.NotStartedPoints == 0) {
			return fmt.Errorf("cancelled experiment has inconsistent dispositions")
		}
	case StatusFailed:
		if manifest.Complete || manifest.ErrorClass == "" {
			return fmt.Errorf("failed experiment lacks failure evidence")
		}
	default:
		return fmt.Errorf("invalid experiment status %q", manifest.Status)
	}

	seenPointIDs := make(map[string]struct{}, len(manifest.Points))
	seenRunIDs := make(map[string]struct{}, manifest.ExecutedPoints)
	seenPaths := make(map[string]struct{}, manifest.ExecutedPoints)
	for index, record := range manifest.Points {
		planned := plan.Points[index]
		if record.PointIndex != index+1 || record.PointIndex != planned.Index || record.PointID != fmt.Sprintf("point-%06d", index+1) || record.PointID != planned.ID || !reflect.DeepEqual(record.Parameters, planned.Parameters) {
			return fmt.Errorf("point %d identity or parameters do not match the plan", index+1)
		}
		if _, exists := seenPointIDs[record.PointID]; exists {
			return fmt.Errorf("duplicate point ID %s", record.PointID)
		}
		seenPointIDs[record.PointID] = struct{}{}
		summary, hasSummary := summaries[record.PointIndex]
		switch record.PointStatus {
		case PointExecuted:
			if record.RunID == nil || record.RunPath == nil || record.RunStatus == nil || !hasSummary {
				return fmt.Errorf("executed point %s lacks child run evidence", record.PointID)
			}
			expectedPath := filepath.ToSlash(filepath.Join("runs", *record.RunID))
			if *record.RunPath != expectedPath || filepath.Base(*record.RunID) != *record.RunID || *record.RunID == "." || summary.RunID != *record.RunID || summary.RunStatus != *record.RunStatus || summary.SchemaVersion != artifacts.SchemaVersion {
				return fmt.Errorf("executed point %s has inconsistent child identity", record.PointID)
			}
			if _, exists := seenRunIDs[*record.RunID]; exists {
				return fmt.Errorf("duplicate child run ID %s", *record.RunID)
			}
			if _, exists := seenPaths[*record.RunPath]; exists {
				return fmt.Errorf("duplicate child run path %s", *record.RunPath)
			}
			seenRunIDs[*record.RunID] = struct{}{}
			seenPaths[*record.RunPath] = struct{}{}
			if err := validateChildSummary(stagingRoot, *record.RunPath, summary); err != nil {
				return fmt.Errorf("point %s: %w", record.PointID, err)
			}
		case PointPreflightFailed:
			if record.RunID != nil || record.RunPath != nil || record.RunStatus != nil || hasSummary || record.ErrorClass == "" || record.Error == "" {
				return fmt.Errorf("preflight-failed point %s has invalid evidence", record.PointID)
			}
		case PointCancelled:
			if record.RunID != nil || record.RunPath != nil || record.RunStatus != nil || hasSummary || record.ErrorClass != ErrorClassCancellation {
				return fmt.Errorf("cancelled point %s has invalid evidence", record.PointID)
			}
		case PointNotStarted:
			if record.RunID != nil || record.RunPath != nil || record.RunStatus != nil || hasSummary || record.ErrorClass != "" || record.Error != "" {
				return fmt.Errorf("not-started point %s contains execution evidence", record.PointID)
			}
		default:
			return fmt.Errorf("point %s has invalid status %q", record.PointID, record.PointStatus)
		}
	}
	if len(summaries) != manifest.ExecutedPoints {
		return fmt.Errorf("child summary count does not match executed points")
	}
	entries, err := os.ReadDir(filepath.Join(stagingRoot, "runs"))
	if err != nil {
		return fmt.Errorf("read child-run root: %w", err)
	}
	if len(entries) != manifest.ExecutedPoints {
		return fmt.Errorf("child-run directory count does not match executed points")
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			return fmt.Errorf("child-run root contains non-directory %s", entry.Name())
		}
		if _, exists := seenRunIDs[entry.Name()]; !exists {
			return fmt.Errorf("unreferenced child-run directory %s", entry.Name())
		}
	}
	return nil
}

func validateChildSummary(stagingRoot, relativePath string, expected aggregate.RunSummary) error {
	childRoot := filepath.Join(stagingRoot, filepath.FromSlash(relativePath))
	encodedMetadata, err := os.ReadFile(filepath.Join(childRoot, "run.json"))
	if err != nil {
		return fmt.Errorf("read child run.json: %w", err)
	}
	var metadata artifacts.RunMetadata
	if err := json.Unmarshal(encodedMetadata, &metadata); err != nil {
		return fmt.Errorf("decode child run.json: %w", err)
	}
	if metadata.SchemaVersion != artifacts.SchemaVersion || metadata.RunID != expected.RunID || metadata.RunStatus != expected.RunStatus {
		return fmt.Errorf("child run.json has inconsistent schema, identity, or status")
	}
	encodedJSON, err := os.ReadFile(filepath.Join(childRoot, "summary.json"))
	if err != nil {
		return fmt.Errorf("read child summary.json: %w", err)
	}
	var actual aggregate.RunSummary
	if err := json.Unmarshal(encodedJSON, &actual); err != nil {
		return fmt.Errorf("decode child summary.json: %w", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		return fmt.Errorf("child summary.json differs from executor RunSummary")
	}
	wantCSV, err := aggregate.MarshalCSV(expected)
	if err != nil {
		return fmt.Errorf("encode expected child summary.csv: %w", err)
	}
	actualCSV, err := os.ReadFile(filepath.Join(childRoot, "summary.csv"))
	if err != nil {
		return fmt.Errorf("read child summary.csv: %w", err)
	}
	if !bytes.Equal(actualCSV, wantCSV) {
		return fmt.Errorf("child summary.csv differs from executor RunSummary")
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
