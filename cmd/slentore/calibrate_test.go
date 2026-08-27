package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/artifacts"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/calibration"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/experiment"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
)

func TestRunCalibrationClosedLoopSchemasSafetyAndRedaction(t *testing.T) {
	server := newCalibrationTestServer(t, 0)
	output := t.TempDir()
	var stdout, stderr bytes.Buffer
	exit := run([]string{
		"calibrate-client", "--base-url", server.URL + "/v1", "--model", "fixture-model", "--mode", "closed-loop",
		"--concurrency-values", "1,4,16", "--requests", "16", "--max-concurrency", "32", "--max-requests", "64", "--output-dir", output,
	}, &stdout, &stderr, func(string) (string, bool) { return "", false })
	if exit != 0 {
		t.Fatalf("exit=%d stderr=%s stdout=%s", exit, stderr.String(), stdout.String())
	}
	manifest := readCalibrationManifest(t, output)
	if manifest.CalibrationSchemaVersion != 1 || manifest.CleanPoints != 3 || manifest.DirtyPoints != 0 || manifest.UnevaluatedPoints != 0 {
		t.Fatalf("manifest = %+v", manifest)
	}
	experimentRoot := filepath.Join(output, filepath.FromSlash(manifest.ExperimentPath))
	var experimentManifest experiment.Manifest
	readJSONFile(t, filepath.Join(experimentRoot, "experiment.json"), &experimentManifest)
	if experimentManifest.ExperimentSchemaVersion != 1 || len(experimentManifest.Points) != 3 {
		t.Fatalf("experiment = %+v", experimentManifest)
	}
	for _, point := range experimentManifest.Points {
		if point.RunPath == nil {
			t.Fatalf("point lacks run path: %+v", point)
		}
		var runMetadata artifacts.RunMetadata
		readJSONFile(t, filepath.Join(experimentRoot, filepath.FromSlash(*point.RunPath), "run.json"), &runMetadata)
		if runMetadata.SchemaVersion != 7 || runMetadata.SafetyLimits.MaxConcurrency != 32 || runMetadata.SafetyLimits.MaxRequests != 64 || runMetadata.Workload.Output.RequestedMaxTokens != 1 || runMetadata.TokenTiming.Mode != "disabled" {
			t.Fatalf("child metadata = %+v", runMetadata)
		}
	}
	allArtifacts := readAllText(t, output)
	for _, forbidden := range []string{calibrationPrompt, "private-calibration-key", "Authorization", "987654321", `"choices"`, `"messages"`} {
		if strings.Contains(allArtifacts, forbidden) {
			t.Fatalf("artifacts contain %q", forbidden)
		}
	}
}

func TestRunCalibrationOpenLoopCleanAndClientLimitedContinuation(t *testing.T) {
	t.Run("clean", func(t *testing.T) {
		server := newCalibrationTestServer(t, 0)
		output := t.TempDir()
		var stdout, stderr bytes.Buffer
		exit := run([]string{
			"calibrate-client", "--base-url", server.URL + "/v1", "--model", "fixture-model", "--mode", "open-loop",
			"--request-rate-values", "20,40", "--duration", "250ms", "--max-in-flight", "16", "--output-dir", output,
		}, &stdout, &stderr, func(string) (string, bool) { return "", false })
		if exit != 0 {
			t.Fatalf("exit=%d stderr=%s stdout=%s", exit, stderr.String(), stdout.String())
		}
		manifest := readCalibrationManifest(t, output)
		if manifest.CleanPoints != 2 || manifest.DirtyPoints != 0 {
			t.Fatalf("manifest = %+v", manifest)
		}
	})

	t.Run("client limited", func(t *testing.T) {
		server := newCalibrationTestServer(t, 150*time.Millisecond)
		output := t.TempDir()
		var stdout, stderr bytes.Buffer
		exit := run([]string{
			"calibrate-client", "--base-url", server.URL + "/v1", "--model", "fixture-model", "--mode", "open-loop",
			"--request-rate-values", "100,200", "--duration", "100ms", "--max-in-flight", "1", "--output-dir", output,
		}, &stdout, &stderr, func(string) (string, bool) { return "", false })
		if exit != 1 {
			t.Fatalf("exit=%d stderr=%s stdout=%s", exit, stderr.String(), stdout.String())
		}
		manifest := readCalibrationManifest(t, output)
		if manifest.EvaluatedPoints != 2 || manifest.DirtyPoints != 2 || manifest.UnevaluatedPoints != 0 {
			t.Fatalf("manifest = %+v", manifest)
		}
		for _, point := range manifest.Points {
			if point.DeliveryClean == nil || *point.DeliveryClean || !containsString(point.DeliveryReasons, calibration.ReasonClientLimited) || point.RunStatus == nil || *point.RunStatus != artifacts.RunStatusFailed {
				t.Fatalf("point = %+v", point)
			}
		}
	})
}

func TestRunCalibrationRejectsInvalidModeAxisAndLagBeforeArtifacts(t *testing.T) {
	output := filepath.Join(t.TempDir(), "artifacts")
	for _, args := range [][]string{
		{"calibrate-client", "--base-url", "http://127.0.0.1:1/v1", "--model", "m", "--mode", "closed-loop", "--request-rate-values", "1", "--output-dir", output},
		{"calibrate-client", "--base-url", "http://127.0.0.1:1/v1", "--model", "m", "--mode", "closed-loop", "--concurrency-values", "1", "--max-scheduler-lag-p95", "1ms", "--output-dir", output},
	} {
		var stdout, stderr bytes.Buffer
		if exit := run(args, &stdout, &stderr, func(string) (string, bool) { return "", false }); exit != 2 {
			t.Fatalf("args=%v exit=%d stderr=%s", args, exit, stderr.String())
		}
	}
	if _, err := os.Stat(output); !os.IsNotExist(err) {
		t.Fatalf("invalid calibration created artifacts: %v", err)
	}
}

func TestVersionCommandReportsInjectedValues(t *testing.T) {
	oldVersion, oldRevision := version, revision
	version, revision = "v1.0.0", "abc123"
	defer func() { version, revision = oldVersion, oldRevision }()
	var stdout, stderr bytes.Buffer
	if exit := run([]string{"version"}, &stdout, &stderr, func(string) (string, bool) { return "", false }); exit != 0 {
		t.Fatalf("exit=%d stderr=%s", exit, stderr.String())
	}
	for _, required := range []string{"version: v1.0.0", "revision: abc123", "go: go"} {
		if !strings.Contains(stdout.String(), required) {
			t.Fatalf("version output %q missing %q", stdout.String(), required)
		}
	}
}

func newCalibrationTestServer(t *testing.T, firstContentDelay time.Duration) *httptest.Server {
	t.Helper()
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 0
	config.FirstContentDelay = firstContentDelay
	config.ChunkInterval = 0
	config.UsageDelay = 0
	config.DoneDelay = 0
	handler, err := fakeserver.NewHandler(config)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return server
}

func readCalibrationManifest(t *testing.T, output string) calibration.Manifest {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(output, "calibrations", "*", "calibration.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("calibration manifests=%v err=%v", matches, err)
	}
	var manifest calibration.Manifest
	readJSONFile(t, matches[0], &manifest)
	return manifest
}

func readJSONFile(t *testing.T, path string, target any) {
	t.Helper()
	encoded, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, target); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
}

func readAllText(t *testing.T, root string) string {
	t.Helper()
	var combined strings.Builder
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		encoded, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		combined.Write(encoded)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return combined.String()
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
