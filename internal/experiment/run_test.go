package experiment

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmarkexec"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/config"
)

func TestRunnerExecutesSequentiallyAndContinuesAfterChildFailure(t *testing.T) {
	base := validBase()
	base.Experiment.ConcurrencyValues = []int{1, 2, 3}
	plan, err := BuildPlan(base)
	if err != nil {
		t.Fatalf("BuildPlan: %v", err)
	}
	var active atomic.Int64
	var maximum atomic.Int64
	order := make([]int, 0, 3)
	runner := deterministicRunner(func(_ context.Context, request benchmarkexec.Request) (benchmarkexec.Result, error) {
		current := active.Add(1)
		defer active.Add(-1)
		if current > maximum.Load() {
			maximum.Store(current)
		}
		order = append(order, request.Config.Benchmark.Concurrency)
		status := artifacts.RunStatusCompleted
		if request.Config.Benchmark.Concurrency == 2 {
			status = artifacts.RunStatusFailed
		}
		return writeFakeChild(t, request, fmt.Sprintf("run-%d", request.Config.Benchmark.Concurrency), status), nil
	})
	result, err := runner.Run(context.Background(), RunRequest{Plan: plan, OutputRoot: t.TempDir(), SlentoreVersion: "test"})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if maximum.Load() != 1 || fmt.Sprint(order) != "[1 2 3]" {
		t.Fatalf("maximum=%d order=%v", maximum.Load(), order)
	}
	manifest := result.Manifest
	if manifest.Status != StatusCompleted || !manifest.Complete || manifest.ExecutedPoints != 3 || manifest.ChildRunsCompleted != 2 || manifest.ChildRunsFailed != 1 || result.Successful() {
		t.Fatalf("manifest = %+v", manifest)
	}
	if _, err := os.Stat(filepath.Join(result.ArtifactPath, "experiment.json")); err != nil {
		t.Fatalf("published experiment: %v", err)
	}
}

func TestRunnerContinuesAfterOperationalPreflightFailure(t *testing.T) {
	base := validBase()
	base.Experiment.ConcurrencyValues = []int{1, 2, 3}
	plan, _ := BuildPlan(base)
	var calls []int
	runner := deterministicRunner(func(_ context.Context, request benchmarkexec.Request) (benchmarkexec.Result, error) {
		value := request.Config.Benchmark.Concurrency
		calls = append(calls, value)
		if value == 2 {
			return benchmarkexec.Result{}, &benchmarkexec.ExecutionError{Stage: benchmarkexec.FailurePreparation, Op: "prepare workload", Err: errors.New("exact token construction failed")}
		}
		return writeFakeChild(t, request, fmt.Sprintf("run-%d", value), artifacts.RunStatusCompleted), nil
	})
	result, err := runner.Run(context.Background(), RunRequest{Plan: plan, OutputRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fmt.Sprint(calls) != "[1 2 3]" || result.Manifest.PreflightFailedPoints != 1 || result.Manifest.ExecutedPoints != 2 || !result.Manifest.Complete || result.Successful() {
		t.Fatalf("calls=%v manifest=%+v", calls, result.Manifest)
	}
	failed := result.Manifest.Points[1]
	if failed.PointStatus != PointPreflightFailed || failed.RunID != nil || failed.RunPath != nil || failed.ErrorClass != ErrorClassPreparation {
		t.Fatalf("preflight point = %+v", failed)
	}
}

func TestRunnerCancellationStopsFuturePointsAndPublishes(t *testing.T) {
	base := validBase()
	base.Experiment.ConcurrencyValues = []int{1, 2, 3, 4}
	plan, _ := BuildPlan(base)
	ctx, cancel := context.WithCancel(context.Background())
	var calls []int
	runner := deterministicRunner(func(_ context.Context, request benchmarkexec.Request) (benchmarkexec.Result, error) {
		value := request.Config.Benchmark.Concurrency
		calls = append(calls, value)
		if value == 2 {
			cancel()
			return benchmarkexec.Result{}, &benchmarkexec.ExecutionError{Stage: benchmarkexec.FailurePreparation, Op: "prepare workload", Err: context.Canceled}
		}
		return writeFakeChild(t, request, fmt.Sprintf("run-%d", value), artifacts.RunStatusCompleted), nil
	})
	result, err := runner.Run(ctx, RunRequest{Plan: plan, OutputRoot: t.TempDir()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if fmt.Sprint(calls) != "[1 2]" || result.Manifest.Status != StatusCancelled || result.Manifest.Complete || result.Manifest.ExecutedPoints != 1 || result.Manifest.CancelledPoints != 1 || result.Manifest.NotStartedPoints != 2 {
		t.Fatalf("calls=%v manifest=%+v", calls, result.Manifest)
	}
	if result.Manifest.Points[1].PointStatus != PointCancelled || result.Manifest.Points[2].PointStatus != PointNotStarted || result.Manifest.Points[3].PointStatus != PointNotStarted {
		t.Fatalf("points = %+v", result.Manifest.Points)
	}
}

func TestRunnerCleansStagingWhenChildEvidenceIsMissing(t *testing.T) {
	base := validBase()
	base.Experiment.ConcurrencyValues = []int{1}
	plan, _ := BuildPlan(base)
	outputRoot := t.TempDir()
	runner := deterministicRunner(func(_ context.Context, request benchmarkexec.Request) (benchmarkexec.Result, error) {
		metadata, summary := fakeChildEvidence(request, "run-missing", artifacts.RunStatusCompleted)
		return benchmarkexec.Result{Metadata: metadata, Summary: summary}, nil
	})
	if _, err := runner.Run(context.Background(), RunRequest{Plan: plan, OutputRoot: outputRoot}); err == nil {
		t.Fatal("missing child evidence unexpectedly published")
	}
	entries, err := os.ReadDir(filepath.Join(outputRoot, "experiments"))
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("staging/final entries remain: %v", entries)
	}
}

func TestExperimentIDUsesIndependentSafeIdentity(t *testing.T) {
	now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.FixedZone("offset", 5*60*60))
	id, err := newID(now, bytesReader([]byte{0xa3, 0x1f, 0x00, 0xff}))
	if err != nil || id != "20260823T070000Z-exp-a31f00ff" {
		t.Fatalf("id=%q err=%v", id, err)
	}
}

type byteReader struct{ values []byte }

func bytesReader(values []byte) *byteReader {
	return &byteReader{values: append([]byte(nil), values...)}
}
func (r *byteReader) Read(target []byte) (int, error) {
	if len(r.values) == 0 {
		return 0, errors.New("empty")
	}
	count := copy(target, r.values)
	r.values = r.values[count:]
	return count, nil
}

func deterministicRunner(execute ExecuteFunc) *Runner {
	return &Runner{
		Execute: execute,
		NewID:   func() (string, error) { return "20260823T120000Z-exp-a31f00ff", nil },
		Now:     func() time.Time { return time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC) },
	}
}

func writeFakeChild(t *testing.T, request benchmarkexec.Request, runID, status string) benchmarkexec.Result {
	return writeFakeChildWithMutation(t, request, runID, status, nil)
}

type fakeChildMutation func(*artifacts.RunMetadata, *aggregate.RunSummary)

func writeFakeChildWithMutation(t *testing.T, request benchmarkexec.Request, runID, status string, mutate fakeChildMutation) benchmarkexec.Result {
	t.Helper()
	metadata, summary := fakeChildEvidence(request, runID, status)
	if mutate != nil {
		mutate(&metadata, &summary)
	}
	root := filepath.Join(request.ArtifactRoot, runID)
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	encodedMetadata, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	encodedMetadata = append(encodedMetadata, '\n')
	if err := os.WriteFile(filepath.Join(root, "run.json"), encodedMetadata, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	encodedJSON, err := json.MarshalIndent(summary, "", "  ")
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	encodedJSON = append(encodedJSON, '\n')
	if err := os.WriteFile(filepath.Join(root, "summary.json"), encodedJSON, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	encodedCSV, err := aggregate.MarshalCSV(summary)
	if err != nil {
		t.Fatalf("MarshalCSV: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "summary.csv"), encodedCSV, 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	return benchmarkexec.Result{Metadata: metadata, Summary: summary, ArtifactPath: root}
}

func fakeChildEvidence(request benchmarkexec.Request, runID, status string) (artifacts.RunMetadata, aggregate.RunSummary) {
	metadata := artifacts.RunMetadata{
		SchemaVersion: artifacts.SchemaVersion,
		RunID:         runID,
		RunStatus:     status,
		Workload: artifacts.WorkloadMetadata{
			Mode:   string(request.Config.Workload.Mode),
			Output: artifacts.WorkloadOutputMetadata{RequestedMaxTokens: request.Config.Request.MaxOutputTokens},
		},
		Load: artifacts.LoadMetadata{Mode: benchmark.LoadMode(request.Config.Benchmark.Mode)},
	}
	summary := aggregate.RunSummary{
		SchemaVersion: artifacts.SchemaVersion,
		RunID:         runID,
		RunStatus:     status,
		Workload: aggregate.WorkloadSummary{
			Mode:                     string(request.Config.Workload.Mode),
			RequestedOutputMaxTokens: request.Config.Request.MaxOutputTokens,
		},
		Load: aggregate.LoadSummary{Mode: benchmark.LoadMode(request.Config.Benchmark.Mode)},
	}
	if request.Config.Workload.Mode == config.WorkloadModeTokenLength {
		inputTokens := request.Config.Workload.InputTokens
		metadata.Workload.Input = &artifacts.WorkloadInputMetadata{TargetTokens: inputTokens, ResolvedTokens: inputTokens}
		summary.Workload.InputTargetTokens = intPointer(inputTokens)
		summary.Workload.InputResolvedTokens = intPointer(inputTokens)
	}
	switch request.Config.Benchmark.Mode {
	case config.LoadModeClosedLoop:
		metadata.Load.ClosedLoop = &artifacts.ClosedLoopLoadMetadata{RequestedConcurrency: request.Config.Benchmark.Concurrency}
		summary.Load.ClosedLoop = &aggregate.ClosedLoopSummary{RequestedConcurrency: request.Config.Benchmark.Concurrency}
	case config.LoadModeOpenLoop:
		requestRate := request.Config.Benchmark.OpenLoop.RequestRate
		metadata.Load.OpenLoop = &artifacts.OpenLoopLoadMetadata{RequestRate: requestRate}
		summary.Load.OpenLoop = &aggregate.OpenLoopSummary{ConfiguredRequestRate: requestRate}
	}
	return metadata, summary
}
