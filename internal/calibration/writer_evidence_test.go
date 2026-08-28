package calibration

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
)

func TestWriterRejectsCalibrationEvidenceCorruption(t *testing.T) {
	closedRoot, closed := generatedCalibration(t, config.LoadModeClosedLoop, []int{2}, nil, 0)
	openRoot, open := generatedCalibration(t, config.LoadModeOpenLoop, nil, []float64{200}, 100*time.Millisecond)
	if open.DirtyPoints != 1 || open.Points[0].DeliveryClean == nil || *open.Points[0].DeliveryClean {
		t.Fatalf("open fixture is not dirty: %+v", open)
	}

	tests := []struct {
		name   string
		root   string
		base   Manifest
		mutate func(*Manifest)
	}{
		{name: "concurrency", root: closedRoot, base: closed, mutate: func(value *Manifest) { *value.Points[0].RequestedConcurrency++ }},
		{name: "request rate", root: openRoot, base: open, mutate: func(value *Manifest) { *value.Points[0].ConfiguredRequestRate++ }},
		{name: "child identity and path", root: closedRoot, base: closed, mutate: func(value *Manifest) {
			childID := "other-run"
			childPath := filepath.ToSlash(filepath.Join("experiments", value.ExperimentID, "runs", childID))
			value.Points[0].ChildRunID = &childID
			value.Points[0].ChildRunPath = &childPath
		}},
		{name: "successful counts", root: closedRoot, base: closed, mutate: func(value *Manifest) { value.Points[0].Counts.Successful++ }},
		{name: "client limited", root: openRoot, base: open, mutate: func(value *Manifest) { value.Points[0].OpenLoop.ClientLimited++ }},
		{name: "scheduler lag", root: openRoot, base: open, mutate: func(value *Manifest) { value.Points[0].SchedulerLag.P95++ }},
		{name: "successful throughput", root: closedRoot, base: closed, mutate: func(value *Manifest) { value.Points[0].SuccessfulRequestThroughput.Value++ }},
		{name: "dirty changed clean", root: openRoot, base: open, mutate: func(value *Manifest) { clean := true; value.Points[0].DeliveryClean = &clean }},
		{name: "dirty reasons removed", root: openRoot, base: open, mutate: func(value *Manifest) { value.Points[0].DeliveryReasons = nil }},
		{name: "unknown reason", root: openRoot, base: open, mutate: func(value *Manifest) {
			value.Points[0].DeliveryReasons = append(value.Points[0].DeliveryReasons, "unknown_reason")
		}},
		{name: "known reason changed", root: openRoot, base: open, mutate: func(value *Manifest) {
			value.Points[0].DeliveryReasons[0] = ReasonSchedulerLagExceeded
		}},
		{name: "manifest total", root: closedRoot, base: closed, mutate: func(value *Manifest) { value.CleanPoints++ }},
	}

	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			manifest := cloneManifest(t, test.base)
			manifest.CalibrationID = fmt.Sprintf("rejected-%02d", index)
			test.mutate(&manifest)
			assertRejectedPublication(t, test.root, manifest)
		})
	}

	t.Run("experiment identity", func(t *testing.T) {
		manifest := cloneManifest(t, closed)
		manifest.CalibrationID = "rejected-experiment-identity"
		manifest.ExperimentID = "different-experiment"
		manifest.ExperimentPath = "experiments/different-experiment"
		existing := filepath.Join(closedRoot, filepath.FromSlash(closed.ExperimentPath))
		alias := filepath.Join(closedRoot, filepath.FromSlash(manifest.ExperimentPath))
		if err := os.Symlink(existing, alias); err != nil {
			t.Fatalf("create experiment alias: %v", err)
		}
		assertRejectedPublication(t, closedRoot, manifest)
	})
}

func TestCalibrationCSVNumericallyMatchesJSONForEveryEvaluatedPoint(t *testing.T) {
	tests := []struct {
		name          string
		mode          config.LoadMode
		concurrencies []int
		rates         []float64
	}{
		{name: "closed loop", mode: config.LoadModeClosedLoop, concurrencies: []int{1, 4}},
		{name: "open loop", mode: config.LoadModeOpenLoop, rates: []float64{20, 40}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root, manifest := generatedCalibration(t, test.mode, test.concurrencies, test.rates, 0)
			path := filepath.Join(root, "calibrations", manifest.CalibrationID, "summary.csv")
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			rows, err := csv.NewReader(file).ReadAll()
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != len(manifest.Points)+1 {
				t.Fatalf("CSV rows=%d points=%d", len(rows), len(manifest.Points))
			}
			columns := make(map[string]int, len(rows[0]))
			for index, name := range rows[0] {
				columns[name] = index
			}
			for index, point := range manifest.Points {
				row := rows[index+1]
				if point.DeliveryClean == nil || point.Counts == nil || point.SuccessfulRequestThroughput == nil {
					t.Fatalf("point %d is not evaluated", point.PointIndex)
				}
				if point.RequestedConcurrency != nil {
					assertCSVInt(t, row, columns, "requested_concurrency", *point.RequestedConcurrency)
				} else {
					assertCSVFloat(t, row, columns, "configured_request_rate", *point.ConfiguredRequestRate)
				}
				assertCSVFloat(t, row, columns, "successful_requests_per_second", point.SuccessfulRequestThroughput.Value)
				assertCSVInt(t, row, columns, "requested_or_planned", point.Counts.RequestedOrPlanned)
				assertCSVInt(t, row, columns, "started", point.Counts.Started)
				assertCSVInt(t, row, columns, "successful", point.Counts.Successful)
				assertCSVInt(t, row, columns, "failed", point.Counts.Failed)
				if point.OpenLoop != nil {
					assertCSVFloat(t, row, columns, "delivery_ratio", point.OpenLoop.DeliveryRatio.Value)
					assertCSVFloat(t, row, columns, "actual_starts_per_second", point.OpenLoop.ActualStartRate.Value)
					assertCSVInt(t, row, columns, "client_limited", point.OpenLoop.ClientLimited)
					assertCSVInt(t, row, columns, "scheduler_limited", point.OpenLoop.SchedulerLimited)
					assertCSVFloat(t, row, columns, "scheduler_lag_p95_ms", point.SchedulerLag.P95)
				}
				if row[columns["delivery_clean"]] != strconv.FormatBool(*point.DeliveryClean) {
					t.Fatalf("point %d delivery_clean CSV=%q JSON=%t", point.PointIndex, row[columns["delivery_clean"]], *point.DeliveryClean)
				}
			}
		})
	}
}

func generatedCalibration(t *testing.T, mode config.LoadMode, concurrencies []int, rates []float64, delay time.Duration) (string, Manifest) {
	t.Helper()
	fakeConfig := fakeserver.DefaultConfig()
	fakeConfig.HeaderDelay = 0
	fakeConfig.FirstContentDelay = delay
	fakeConfig.ChunkInterval = 0
	fakeConfig.UsageDelay = 0
	fakeConfig.DoneDelay = 0
	handler, err := fakeserver.NewHandler(fakeConfig)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)

	root := t.TempDir()
	base := calibrationConfig(server.URL+"/v1", root)
	base.Benchmark.Mode = mode
	base.Benchmark.Requests = 2
	base.Benchmark.OpenLoop.Duration = 50 * time.Millisecond
	base.Benchmark.OpenLoop.MaxInFlight = 1
	base.Experiment.ConcurrencyValues = append([]int(nil), concurrencies...)
	base.Experiment.RequestRateValues = append([]float64(nil), rates...)
	plan, err := experiment.BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	result, err := NewRunner().Run(context.Background(), RunRequest{Plan: plan, OutputRoot: root, SlentoreVersion: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return root, result.Manifest
}

func cloneManifest(t *testing.T, value Manifest) Manifest {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var clone Manifest
	if err := json.Unmarshal(encoded, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func assertRejectedPublication(t *testing.T, root string, manifest Manifest) {
	t.Helper()
	experimentJSON := filepath.Join(root, filepath.FromSlash(manifest.ExperimentPath), "experiment.json")
	before, _ := os.ReadFile(experimentJSON)
	if _, err := NewWriter(root).Write(manifest); err == nil {
		t.Fatal("corrupted calibration evidence unexpectedly published")
	}
	finalRoot := filepath.Join(root, "calibrations", manifest.CalibrationID)
	if _, err := os.Stat(finalRoot); !os.IsNotExist(err) {
		t.Fatalf("rejected final calibration exists: %v", err)
	}
	staging, err := filepath.Glob(filepath.Join(root, "calibrations", "."+manifest.CalibrationID+"-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("rejected staging remains: %v, %v", staging, err)
	}
	after, _ := os.ReadFile(experimentJSON)
	if string(before) != string(after) {
		t.Fatal("rejected calibration changed referenced experiment evidence")
	}
}

func assertCSVInt(t *testing.T, row []string, columns map[string]int, name string, want int) {
	t.Helper()
	got, err := strconv.Atoi(row[columns[name]])
	if err != nil || got != want {
		t.Fatalf("%s CSV=%q JSON=%d err=%v", name, row[columns[name]], want, err)
	}
}

func assertCSVFloat(t *testing.T, row []string, columns map[string]int, name string, want float64) {
	t.Helper()
	got, err := strconv.ParseFloat(row[columns[name]], 64)
	if err != nil || got != want {
		t.Fatalf("%s CSV=%q JSON=%g err=%v", name, row[columns[name]], want, err)
	}
}
