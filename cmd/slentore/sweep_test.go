package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/aggregate"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
)

func TestRunSweepClosedLoopPreservesOrderWarmupAndSchemas(t *testing.T) {
	const secret = "private-sweep-api-key"
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+secret {
			t.Errorf("missing Authorization")
		}
		calls.Add(1)
		_, _ = io.WriteString(writer, normalCLIFixture("private-generated-sweep-content"))
	}))
	defer server.Close()
	outputRoot := filepath.Join(t.TempDir(), "artifacts")
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"sweep", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "private-sweep-prompt",
		"--api-key-env", "SWEEP_SECRET", "--concurrency-values", "2,1,3", "--requests", "3", "--warmup-requests", "1",
		"--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputRoot,
	}, &stdout, &stderr, func(name string) (string, bool) { return secret, name == "SWEEP_SECRET" })
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr.String(), stdout.String())
	}
	if calls.Load() != 12 {
		t.Fatalf("calls=%d, want 3 points * (1 warmup + 3 measured)", calls.Load())
	}
	experimentRoot := onlyExperimentDirectory(t, outputRoot)
	var manifest experiment.Manifest
	readJSON(t, filepath.Join(experimentRoot, "experiment.json"), &manifest)
	if manifest.ExperimentSchemaVersion != 1 || manifest.Status != experiment.StatusCompleted || !manifest.Complete || manifest.PlannedPoints != 3 || manifest.ExecutedPoints != 3 || manifest.ChildRunsCompleted != 3 {
		t.Fatalf("manifest=%+v", manifest)
	}
	for index, wantConcurrency := range []int{2, 1, 3} {
		point := manifest.Points[index]
		if point.Parameters.Concurrency == nil || *point.Parameters.Concurrency != wantConcurrency || point.PointStatus != experiment.PointExecuted || point.RunID == nil || point.RunPath == nil || point.RunStatus == nil || *point.RunStatus != artifacts.RunStatusCompleted {
			t.Fatalf("point %d=%+v", index+1, point)
		}
		var metadata artifacts.RunMetadata
		readJSON(t, filepath.Join(experimentRoot, filepath.FromSlash(*point.RunPath), "run.json"), &metadata)
		var summary aggregate.RunSummary
		readJSON(t, filepath.Join(experimentRoot, filepath.FromSlash(*point.RunPath), "summary.json"), &summary)
		if metadata.SchemaVersion != 7 || summary.SchemaVersion != 7 || metadata.RunID != *point.RunID || metadata.Load.ClosedLoop == nil || metadata.Load.ClosedLoop.RequestedConcurrency != wantConcurrency || metadata.Warmup.Requested != 1 || metadata.Warmup.Attempted != 1 {
			t.Fatalf("child metadata/summary=%+v/%+v", metadata, summary)
		}
	}
	records := readCSV(t, filepath.Join(experimentRoot, "summary.csv"))
	if len(records) != 4 || len(records[0]) != len(experiment.CSVColumns) {
		t.Fatalf("combined CSV dimensions=%dx%d", len(records), len(records[0]))
	}
	for index, point := range manifest.Points {
		childRecords := readCSV(t, filepath.Join(experimentRoot, filepath.FromSlash(*point.RunPath), "summary.csv"))
		if len(childRecords) != 2 || strings.Join(records[index+1][10:], "\x00") != strings.Join(childRecords[1], "\x00") {
			t.Fatalf("combined CSV point %d differs from child summary.csv", index+1)
		}
	}
	combined := stdout.String() + readTreeText(t, experimentRoot)
	for _, forbidden := range []string{"private-sweep-prompt", "private-generated-sweep-content", secret, "Authorization", "SWEEP_SECRET"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("sweep output/artifacts contain forbidden value %q", forbidden)
		}
	}
}

func TestRunSweepOpenLoopContinuesAfterLoadDeliveryFailure(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		calls.Add(1)
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			_, _ = io.WriteString(writer, normalCLIFixture("pressure-content"))
		case <-request.Context().Done():
		}
	}))
	defer server.Close()
	outputRoot := filepath.Join(t.TempDir(), "artifacts")
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"sweep", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "pressure-prompt",
		"--mode", "open-loop", "--request-rate-values", "5,100,10", "--duration", "100ms", "--max-in-flight", "1",
		"--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputRoot,
	}, &stdout, &stderr, os.LookupEnv)
	if exit != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr.String(), stdout.String())
	}
	var manifest experiment.Manifest
	experimentRoot := onlyExperimentDirectory(t, outputRoot)
	readJSON(t, filepath.Join(experimentRoot, "experiment.json"), &manifest)
	if manifest.Status != experiment.StatusCompleted || !manifest.Complete || manifest.ExecutedPoints != 3 || manifest.ChildRunsCompleted != 2 || manifest.ChildRunsFailed != 1 {
		t.Fatalf("manifest=%+v", manifest)
	}
	wantStatuses := []string{artifacts.RunStatusCompleted, artifacts.RunStatusFailed, artifacts.RunStatusCompleted}
	for index, want := range wantStatuses {
		if manifest.Points[index].RunStatus == nil || *manifest.Points[index].RunStatus != want {
			t.Fatalf("point statuses=%+v", manifest.Points)
		}
	}
	if manifest.Points[1].ErrorClass != artifacts.ErrorClassLoadDelivery || calls.Load() < 3 {
		t.Fatalf("failure class/calls=%q/%d", manifest.Points[1].ErrorClass, calls.Load())
	}
}

func TestRunSweepTokenShapeRetainsITLAndSLOGoodput(t *testing.T) {
	serverConfig := fakeserver.DefaultConfig()
	serverConfig.HeaderDelay = 0
	serverConfig.FirstContentDelay = 0
	serverConfig.ChunkInterval = time.Millisecond
	serverConfig.UsageDelay = 0
	serverConfig.DoneDelay = 0
	serverConfig.TokenizerFixture = true
	serverConfig.TokenEvidence = fakeserver.TokenEvidenceSingleton
	handler, err := fakeserver.NewHandler(serverConfig)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	outputRoot := filepath.Join(t.TempDir(), "artifacts")
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"sweep", "--base-url", server.URL + "/v1", "--model", "fixture-model", "--workload-mode", "token-length",
		"--tokenizer-adapter", "vllm", "--tokenizer-url", server.URL + "/tokenize", "--input-token-values", "128,256",
		"--output-token-values", "32,64", "--requests", "1", "--token-timing", "vllm", "--slo-ttft", "10s", "--slo-tpot", "10s",
		"--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputRoot,
	}, &stdout, &stderr, os.LookupEnv)
	if exit != 0 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr.String(), stdout.String())
	}
	var manifest experiment.Manifest
	experimentRoot := onlyExperimentDirectory(t, outputRoot)
	readJSON(t, filepath.Join(experimentRoot, "experiment.json"), &manifest)
	want := [][2]int{{128, 32}, {128, 64}, {256, 32}, {256, 64}}
	if len(manifest.Points) != len(want) {
		t.Fatalf("points=%d", len(manifest.Points))
	}
	for index, values := range want {
		point := manifest.Points[index]
		if point.Parameters.InputTokens == nil || *point.Parameters.InputTokens != values[0] || point.Parameters.RequestedOutputTokens != values[1] {
			t.Fatalf("point %d=%+v", index+1, point.Parameters)
		}
		var summary aggregate.RunSummary
		readJSON(t, filepath.Join(experimentRoot, filepath.FromSlash(*point.RunPath), "summary.json"), &summary)
		if summary.Workload.InputTargetTokens == nil || *summary.Workload.InputTargetTokens != values[0] || summary.Workload.RequestedOutputMaxTokens != values[1] || summary.ITL.AvailableRequests != 1 || !summary.SLO.Configured || summary.SLO.GoodRequests != 1 || !summary.SLO.Goodput.Available {
			t.Fatalf("summary %d=%+v", index+1, summary)
		}
	}
	combined := stdout.String() + readTreeText(t, experimentRoot)
	for _, forbidden := range []string{"987654300", "987654301", "987654321", "987654322", "Authorization"} {
		if strings.Contains(combined, forbidden) {
			t.Fatalf("token sweep output/artifacts contain forbidden value %q", forbidden)
		}
	}
}

func TestRunSweepOperationalPreflightFailureContinues(t *testing.T) {
	serverConfig := fakeserver.DefaultConfig()
	serverConfig.HeaderDelay = 0
	serverConfig.FirstContentDelay = 0
	serverConfig.ChunkInterval = 0
	serverConfig.UsageDelay = 0
	serverConfig.DoneDelay = 0
	serverConfig.TokenizerFixture = true
	handler, _ := fakeserver.NewHandler(serverConfig)
	server := httptest.NewServer(handler)
	defer server.Close()
	outputRoot := filepath.Join(t.TempDir(), "artifacts")
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"sweep", "--base-url", server.URL + "/v1", "--model", "fixture-model", "--workload-mode", "token-length",
		"--tokenizer-adapter", "vllm", "--tokenizer-url", server.URL + "/tokenize", "--input-token-values", "128,5000,256",
		"--max-output-tokens", "32", "--requests", "1", "--timeout", "2s", "--output-dir", outputRoot,
	}, &stdout, &stderr, os.LookupEnv)
	if exit != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr.String(), stdout.String())
	}
	var manifest experiment.Manifest
	experimentRoot := onlyExperimentDirectory(t, outputRoot)
	readJSON(t, filepath.Join(experimentRoot, "experiment.json"), &manifest)
	if manifest.Status != experiment.StatusCompleted || !manifest.Complete || manifest.ExecutedPoints != 2 || manifest.PreflightFailedPoints != 1 || manifest.Points[1].PointStatus != experiment.PointPreflightFailed || manifest.Points[1].RunID != nil || manifest.Points[2].PointStatus != experiment.PointExecuted {
		t.Fatalf("manifest=%+v", manifest)
	}
	rows := readCSV(t, filepath.Join(experimentRoot, "summary.csv"))
	if len(rows) != 4 {
		t.Fatalf("CSV rows=%d", len(rows))
	}
	for _, value := range rows[2][10:] {
		if value != "" {
			t.Fatalf("preflight-failed child summary cell is non-empty: %q", value)
		}
	}
}

func TestRunSweepCancellationPublishesPartialExperiment(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var calls atomic.Int64
	secondStarted := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		call := calls.Add(1)
		if call == 1 {
			_, _ = io.WriteString(writer, normalCLIFixture("first"))
			return
		}
		once.Do(func() { close(secondStarted) })
		select {
		case <-request.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	go func() {
		select {
		case <-secondStarted:
			cancel()
		case <-time.After(5 * time.Second):
			cancel()
		}
	}()
	outputRoot := filepath.Join(t.TempDir(), "artifacts")
	var stdout, stderr bytes.Buffer
	exit := runContext(ctx, []string{
		"sweep", "--base-url", server.URL + "/v1", "--model", "model", "--prompt", "cancel-prompt",
		"--concurrency-values", "1,2,3,4", "--requests", "1", "--timeout", "2s", "--drain-timeout", "1s", "--output-dir", outputRoot,
	}, &stdout, &stderr, os.LookupEnv)
	if exit != 1 || stderr.Len() != 0 {
		t.Fatalf("exit=%d stderr=%q stdout=%q", exit, stderr.String(), stdout.String())
	}
	var manifest experiment.Manifest
	experimentRoot := onlyExperimentDirectory(t, outputRoot)
	readJSON(t, filepath.Join(experimentRoot, "experiment.json"), &manifest)
	if manifest.Status != experiment.StatusCancelled || manifest.Complete || manifest.ExecutedPoints != 2 || manifest.ChildRunsCompleted != 1 || manifest.ChildRunsCancelled != 1 || manifest.NotStartedPoints != 2 || manifest.Points[2].PointStatus != experiment.PointNotStarted {
		t.Fatalf("manifest=%+v", manifest)
	}
}

func TestRunSweepRejectsInvalidAxesBeforeArtifacts(t *testing.T) {
	for _, test := range []struct {
		name  string
		extra []string
		want  string
	}{
		{name: "no axis", want: "at least one"},
		{name: "duplicate", extra: []string{"--concurrency-values", "1,2,2"}, want: "duplicate"},
		{name: "scalar conflict", extra: []string{"--concurrency", "4", "--concurrency-values", "1,2"}, want: "conflicts"},
		{name: "wrong mode", extra: []string{"--request-rate-values", "1,2"}, want: "forbidden"},
		{name: "max points", extra: []string{"--concurrency-values", "1,2", "--output-token-values", "16,32", "--max-sweep-points", "3"}, want: "4 points"},
	} {
		t.Run(test.name, func(t *testing.T) {
			outputRoot := filepath.Join(t.TempDir(), "artifacts")
			args := []string{"sweep", "--base-url", "http://127.0.0.1:1/v1", "--model", "model", "--prompt", "prompt", "--output-dir", outputRoot}
			args = append(args, test.extra...)
			var stdout, stderr bytes.Buffer
			if exit := run(args, &stdout, &stderr, os.LookupEnv); exit != 2 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit=%d stderr=%q", exit, stderr.String())
			}
			if _, err := os.Stat(outputRoot); !os.IsNotExist(err) {
				t.Fatalf("invalid sweep created artifacts: %v", err)
			}
		})
	}
}

func TestResolveSweepConfigCLIListsReplaceYAMLAndConflictsRemainExplicit(t *testing.T) {
	configPath := writeCLIConfig(t, `version: 1
endpoint: {base_url: http://127.0.0.1:8000/v1, model: model}
request: {prompt: prompt}
experiment:
  concurrency_values: [1, 2]
  output_token_values: [16]
  safety: {max_points: 8}
`)
	resolved, err := resolveSweepConfig(runFlagValues{configPath: configPath, concurrencyValues: "4,3", maxSweepPoints: 12}, map[string]bool{"concurrency-values": true, "max-sweep-points": true})
	if err != nil {
		t.Fatalf("resolveSweepConfig: %v", err)
	}
	if len(resolved.Experiment.ConcurrencyValues) != 2 || resolved.Experiment.ConcurrencyValues[0] != 4 || resolved.Experiment.ConcurrencyValues[1] != 3 || len(resolved.Experiment.OutputTokenValues) != 1 || resolved.Experiment.OutputTokenValues[0] != 16 || resolved.Experiment.Safety.MaxPoints != 12 {
		t.Fatalf("resolved experiment=%+v", resolved.Experiment)
	}
	_, err = resolveSweepConfig(runFlagValues{configPath: configPath, concurrency: 8}, map[string]bool{"concurrency": true})
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("YAML-axis/scalar conflict error=%v", err)
	}
}

func onlyExperimentDirectory(t *testing.T, outputRoot string) string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(outputRoot, "experiments"))
	if err != nil {
		t.Fatalf("ReadDir experiments: %v", err)
	}
	if len(entries) != 1 || !entries[0].IsDir() || strings.HasPrefix(entries[0].Name(), ".") {
		t.Fatalf("experiment entries=%v", entries)
	}
	return filepath.Join(outputRoot, "experiments", entries[0].Name())
}

func readCSV(t *testing.T, path string) [][]string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatalf("Open CSV: %v", err)
	}
	defer file.Close()
	records, err := csv.NewReader(file).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll CSV: %v", err)
	}
	return records
}
