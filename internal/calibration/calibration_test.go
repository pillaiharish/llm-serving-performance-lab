package calibration

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
)

func TestRunnerPublishesCleanClosedLoopCalibration(t *testing.T) {
	fakeConfig := fakeserver.DefaultConfig()
	fakeConfig.HeaderDelay = 0
	fakeConfig.FirstContentDelay = 0
	fakeConfig.ChunkInterval = 0
	fakeConfig.UsageDelay = 0
	fakeConfig.DoneDelay = 0
	handler, err := fakeserver.NewHandler(fakeConfig)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	base := calibrationConfig(server.URL+"/v1", t.TempDir())
	base.Experiment.ConcurrencyValues = []int{1, 4, 16}
	base.Benchmark.Requests = 16
	plan, err := experiment.BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	result, err := NewRunner().Run(context.Background(), RunRequest{Plan: plan, OutputRoot: base.Capture.OutputDir, SlentoreVersion: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !result.Successful() || result.Manifest.CalibrationSchemaVersion != 1 || result.Experiment.Manifest.ExperimentSchemaVersion != 1 || result.Manifest.CleanPoints != 3 || result.Manifest.DirtyPoints != 0 || result.Manifest.UnevaluatedPoints != 0 {
		t.Fatalf("result = %+v", result.Manifest)
	}
	for _, point := range result.Manifest.Points {
		if point.DeliveryClean == nil || !*point.DeliveryClean || point.Resource == nil || point.Resource.Samples < 2 || point.ChildRunPath == nil || !strings.HasPrefix(*point.ChildRunPath, "experiments/") {
			t.Fatalf("point = %+v", point)
		}
		if point.Counts == nil || point.Counts.RequestedOrPlanned != 16 || point.Counts.Successful != 16 {
			t.Fatalf("counts = %+v", point.Counts)
		}
	}
	encoded, err := os.ReadFile(filepath.Join(result.ArtifactPath, "calibration.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "Slentore client calibration.") || strings.Contains(string(encoded), "choices") || strings.Contains(string(encoded), "token_ids\"") {
		t.Fatalf("calibration artifact leaked transient payload evidence: %s", encoded)
	}
	file, err := os.Open(filepath.Join(result.ArtifactPath, "summary.csv"))
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	rows, err := csv.NewReader(file).ReadAll()
	if err != nil || len(rows) != 4 || len(rows[0]) != len(CSVColumns) {
		t.Fatalf("CSV rows=%d err=%v", len(rows), err)
	}
	var persisted Manifest
	if err := json.Unmarshal(encoded, &persisted); err != nil || persisted.CalibrationSchemaVersion != SchemaVersion {
		t.Fatalf("persisted manifest: %+v, %v", persisted, err)
	}
}

func TestEvaluateOpenLoopLimitAndSchedulerBudget(t *testing.T) {
	result := benchmarkexec.Result{
		Metadata: artifacts.RunMetadata{Measurement: artifacts.PhaseMetadata{Attempted: 8}},
		Summary: aggregate.RunSummary{
			RunStatus: artifacts.RunStatusFailed, Complete: true,
			Counts: aggregate.RequestCounts{RequestedOrPlanned: 10, Started: 8, Completed: 8, Successful: 8},
			Load: aggregate.LoadSummary{Mode: benchmark.LoadModeOpenLoop, OpenLoop: &aggregate.OpenLoopSummary{
				PlannedArrivals: 10, ProcessedArrivals: 10, StartedArrivals: 8, ClientLimited: 1, SchedulerLimited: 1,
				DeliveryRatio: aggregate.Ratio{Available: true, Value: .8, Numerator: 8, Denominator: 10},
			}},
			SchedulerLag: aggregate.Distribution{Available: true, P95: 5, Unit: "ms"},
		},
	}
	budget := 4.0
	clean, reasons := Evaluate(result, &budget)
	if clean {
		t.Fatal("limited result unexpectedly clean")
	}
	for _, want := range []string{ReasonRunNotCompleted, ReasonDeliveryRatioBelowOne, ReasonClientLimited, ReasonSchedulerLimited, ReasonSchedulerLagExceeded} {
		if !contains(reasons, want) {
			t.Fatalf("reasons %v missing %s", reasons, want)
		}
	}
	budget = 5
	result.Summary.RunStatus = artifacts.RunStatusCompleted
	result.Summary.Load.OpenLoop.ClientLimited = 0
	result.Summary.Load.OpenLoop.SchedulerLimited = 0
	result.Summary.Load.OpenLoop.StartedArrivals = 10
	result.Summary.Load.OpenLoop.DeliveryRatio = aggregate.Ratio{Available: true, Value: 1, Numerator: 10, Denominator: 10}
	result.Summary.Counts = aggregate.RequestCounts{RequestedOrPlanned: 10, Started: 10, Completed: 10, Successful: 10}
	clean, reasons = Evaluate(result, &budget)
	if !clean || len(reasons) != 0 {
		t.Fatalf("inclusive budget result clean=%t reasons=%v", clean, reasons)
	}
}

func TestSamplerProducesMonotonicEvidenceAndStops(t *testing.T) {
	before := runtime.NumGoroutine()
	sampler := NewSampler()
	sampler.Interval = time.Millisecond
	evidence := sampler.Measure(func() {
		values := make([][]byte, 32)
		for index := range values {
			values[index] = make([]byte, 4096)
		}
		time.Sleep(4 * time.Millisecond)
		runtime.KeepAlive(values)
	})
	if evidence.StartedAt.IsZero() || evidence.CompletedAt.Before(evidence.StartedAt) || evidence.ElapsedNS < 0 || evidence.Samples < 3 {
		t.Fatalf("timing evidence = %+v", evidence)
	}
	if evidence.GoroutinesObservedPeak < evidence.GoroutinesStart || evidence.GoroutinesObservedPeak < evidence.GoroutinesEnd || evidence.HeapAllocObservedPeakBytes < evidence.HeapAllocStartBytes || evidence.HeapAllocObservedPeakBytes < evidence.HeapAllocEndBytes {
		t.Fatalf("peak evidence = %+v", evidence)
	}
	deadline := time.Now().Add(time.Second)
	for runtime.NumGoroutine() > before+1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runtime.NumGoroutine() > before+1 {
		t.Fatalf("goroutines before=%d after=%d", before, runtime.NumGoroutine())
	}
}

func TestValidatePlanRejectsModeAndLagConflicts(t *testing.T) {
	base := calibrationConfig("http://127.0.0.1:18080/v1", t.TempDir())
	base.Experiment.ConcurrencyValues = []int{1}
	plan, err := experiment.BuildPlan(base)
	if err != nil {
		t.Fatal(err)
	}
	budget := time.Millisecond
	if err := ValidatePlan(plan, &budget); err == nil || !strings.Contains(err.Error(), "open-loop") {
		t.Fatalf("ValidatePlan error = %v", err)
	}
}

func calibrationConfig(baseURL, output string) config.Config {
	value := config.Default()
	value.Endpoint.BaseURL = baseURL
	value.Endpoint.Model = "fixture-model"
	value.Request.Prompt = "transient"
	value.Request.MaxOutputTokens = 1
	value.Capture.OutputDir = output
	return value
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
