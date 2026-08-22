package fakeserver_test

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/fakeserver"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/metrics"
	"github.com/pillaiharish/llm-serving-performance-lab/internal/openai"
)

func TestSlentoreAgainstControlledNormalStream(t *testing.T) {
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 20 * time.Millisecond
	config.FirstContentDelay = 30 * time.Millisecond
	config.ChunkInterval = 15 * time.Millisecond
	config.UsageDelay = 5 * time.Millisecond
	config.DoneDelay = 5 * time.Millisecond
	result, calculated := execute(t, config)
	if result.Err != nil {
		t.Fatalf("RunRequest: %v", result.Err)
	}
	observation := result.Observation
	if observation.StatusCode != http.StatusOK || observation.FinishReason != "stop" {
		t.Fatalf("status/finish = %d/%q", observation.StatusCode, observation.FinishReason)
	}
	if !observation.Usage.Available || observation.Usage.InputTokens != 16 || observation.Usage.OutputTokens != 4 || observation.Usage.TotalTokens != 20 || observation.Usage.Source != "server_usage" {
		t.Fatalf("usage = %+v", observation.Usage)
	}
	if calculated.StreamEventCount != 7 || calculated.ContentEventCount != 4 || calculated.ResponseBytes != 4 {
		t.Fatalf("counts = %+v", calculated)
	}
	if observation.RequestStartedAt == nil || observation.HeadersReceivedAt == nil || observation.HeadersAfterNS == nil || observation.FirstByteAt == nil || observation.FirstByteAfterNS == nil || observation.FirstStreamEventAt == nil || observation.FirstStreamEventAfterNS == nil || observation.FirstContentAt == nil || observation.FirstContentAfterNS == nil || observation.LastContentAt == nil || observation.LastContentAfterNS == nil || observation.CompletedAt == nil || observation.CompletedAfterNS == nil {
		t.Fatalf("missing wall/relative timing evidence: %+v", observation)
	}
	previousOffset := int64(-1)
	for index, event := range observation.StreamEvents {
		if event.Sequence != index+1 || event.ReceivedAfterNS < previousOffset {
			t.Fatalf("unordered event evidence: %+v", observation.StreamEvents)
		}
		previousOffset = event.ReceivedAfterNS
	}
	for name, scalar := range map[string]metrics.Scalar{
		"headers":     calculated.TimeToHeaders,
		"ttfb":        calculated.TTFB,
		"ttft":        calculated.TTFT,
		"ttlt":        calculated.TTLT,
		"e2e":         calculated.E2E,
		"tpot":        calculated.TPOT,
		"decode rate": calculated.DecodeTokensPerSecond,
	} {
		if !scalar.Available || math.IsNaN(scalar.Value) || math.IsInf(scalar.Value, 0) {
			t.Fatalf("%s = %+v", name, scalar)
		}
	}
	assertAtLeast(t, calculated.TimeToHeaders.Value, config.HeaderDelay, 5*time.Millisecond)
	assertAtLeast(t, calculated.TTFB.Value, config.HeaderDelay, 5*time.Millisecond)
	assertAtLeast(t, calculated.TTFT.Value, config.HeaderDelay+config.FirstContentDelay, 5*time.Millisecond)
	minimumE2E := config.HeaderDelay + config.FirstContentDelay + time.Duration(config.ContentChunks-1)*config.ChunkInterval + config.UsageDelay + config.DoneDelay
	assertAtLeast(t, calculated.E2E.Value, minimumE2E, 8*time.Millisecond)
	assertAtLeast(t, calculated.TPOT.Value, config.ChunkInterval, 3*time.Millisecond)
	if !calculated.InterChunkLatency.Available || calculated.InterChunkLatency.Count != 3 || len(calculated.InterChunkLatency.ValuesMS) != 3 {
		t.Fatalf("inter-chunk latency = %+v", calculated.InterChunkLatency)
	}
	for _, value := range calculated.InterChunkLatency.ValuesMS {
		assertAtLeast(t, value, config.ChunkInterval, 3*time.Millisecond)
	}
	if calculated.ITL.Available || calculated.ITL.Source != "not_available" {
		t.Fatalf("true ITL = %+v", calculated.ITL)
	}
}

func TestSlentoreVLLMTokenEvidenceFixtures(t *testing.T) {
	tests := []struct {
		mode      fakeserver.TokenEvidenceMode
		available bool
		reason    string
	}{
		{mode: fakeserver.TokenEvidenceSingleton, available: true},
		{mode: fakeserver.TokenEvidenceBatched, reason: "multiple generated token IDs"},
		{mode: fakeserver.TokenEvidenceMissing, reason: "does not match"},
		{mode: fakeserver.TokenEvidenceMismatch, reason: "does not match"},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			config := fakeserver.DefaultConfig()
			config.HeaderDelay = 0
			config.FirstContentDelay = 0
			config.ChunkInterval = time.Millisecond
			config.UsageDelay = 0
			config.DoneDelay = 0
			config.TokenEvidence = test.mode
			result, calculated := executeWithMode(t, config, openai.TokenEvidenceVLLM)
			if result.Err != nil {
				t.Fatalf("request failed for evidence-only defect: %v", result.Err)
			}
			if calculated.ITL.Available != test.available {
				t.Fatalf("ITL = %+v", calculated.ITL)
			}
			if test.available {
				if calculated.ITL.Count != 3 || len(calculated.ITL.ValuesMS) != 3 || calculated.ITL.Source != benchmark.TokenTimingSourceVLLM {
					t.Fatalf("ITL = %+v", calculated.ITL)
				}
			} else if !strings.Contains(calculated.ITL.Reason, test.reason) {
				t.Fatalf("ITL = %+v, want reason containing %q", calculated.ITL, test.reason)
			}
			if !calculated.InterChunkLatency.Available || calculated.InterChunkLatency.Count != 3 {
				t.Fatalf("ICL changed: %+v", calculated.InterChunkLatency)
			}
			encoded, err := json.Marshal(result.Observation)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}
			for _, sentinel := range []string{"987654300", "987654301", "987654321", "987654322"} {
				if strings.Contains(string(encoded), sentinel) {
					t.Fatalf("observation retained sentinel %s: %s", sentinel, encoded)
				}
			}
		})
	}
}

func TestFakeTokenFixtureHonorsDisabledClientRequest(t *testing.T) {
	config := fakeserver.DefaultConfig()
	config.HeaderDelay = 0
	config.FirstContentDelay = 0
	config.ChunkInterval = 0
	config.UsageDelay = 0
	config.DoneDelay = 0
	config.TokenEvidence = fakeserver.TokenEvidenceSingleton
	result, calculated := executeWithMode(t, config, openai.TokenEvidenceDisabled)
	if result.Err != nil {
		t.Fatalf("RunRequest: %v", result.Err)
	}
	for _, event := range result.Observation.StreamEvents {
		if event.TokenIDsPresent || event.GeneratedTokenCount != 0 {
			t.Fatalf("generic request received token fixture evidence: %+v", result.Observation.StreamEvents)
		}
	}
	if calculated.ITL.Available || calculated.ITL.Source != benchmark.TokenTimingSourceUnavailable {
		t.Fatalf("ITL = %+v", calculated.ITL)
	}
}

func TestSlentoreAgainstFailureModes(t *testing.T) {
	tests := []struct {
		mode          fakeserver.Mode
		wantError     string
		wantStatus    int
		wantNoContent bool
		wantEOF       bool
		wantEvents    bool
	}{
		{mode: fakeserver.ModeNoContent, wantError: benchmark.ErrNoGeneratedContent.Error(), wantStatus: http.StatusOK, wantNoContent: true},
		{mode: fakeserver.ModeHTTPError, wantError: "unexpected HTTP status 503", wantStatus: http.StatusServiceUnavailable},
		{mode: fakeserver.ModeMalformedJSON, wantError: "decode SSE JSON", wantStatus: http.StatusOK},
		{mode: fakeserver.ModeEOFBeforeDone, wantError: openai.ErrUnexpectedEOF.Error(), wantStatus: http.StatusOK, wantEOF: true, wantEvents: true},
		{mode: fakeserver.ModeDataAfterDone, wantError: "after [DONE]", wantStatus: http.StatusOK, wantEvents: true},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			config := fakeserver.DefaultConfig()
			config.Mode = test.mode
			config.HeaderDelay = 0
			config.FirstContentDelay = 0
			config.ChunkInterval = 0
			config.ContentChunks = 1
			config.UsageDelay = 0
			config.DoneDelay = 0
			result, calculated := execute(t, config)
			if result.Err == nil || !strings.Contains(result.Err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", result.Err, test.wantError)
			}
			if test.wantNoContent && !errors.Is(result.Err, benchmark.ErrNoGeneratedContent) {
				t.Fatalf("error = %v, want ErrNoGeneratedContent", result.Err)
			}
			if test.wantEOF && !errors.Is(result.Err, openai.ErrUnexpectedEOF) {
				t.Fatalf("error = %v, want ErrUnexpectedEOF", result.Err)
			}
			if result.Observation.StatusCode != test.wantStatus || result.Observation.RequestStartedAt == nil || result.Observation.CompletedAt == nil || result.Observation.CompletedAfterNS == nil {
				t.Fatalf("partial observation = %+v", result.Observation)
			}
			if test.wantStatus != 0 && (result.Observation.HeadersReceivedAt == nil || result.Observation.HeadersAfterNS == nil) {
				t.Fatalf("missing header evidence: %+v", result.Observation)
			}
			if test.wantEvents && calculated.StreamEventCount == 0 {
				t.Fatalf("partial stream events were not retained: %+v", result.Observation)
			}
			if calculated.RunID != result.Observation.RunID || calculated.RequestID != result.Observation.RequestID {
				t.Fatalf("identity mismatch: observation=%+v metrics=%+v", result.Observation, calculated)
			}
		})
	}
}

func execute(t *testing.T, config fakeserver.Config) (benchmark.Result, metrics.RequestMetrics) {
	return executeWithMode(t, config, openai.TokenEvidenceDisabled)
}

func executeWithMode(t *testing.T, config fakeserver.Config, mode openai.TokenEvidenceMode) (benchmark.Result, metrics.RequestMetrics) {
	t.Helper()
	handler, err := fakeserver.NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()
	client, err := openai.NewClientWithOptions(server.Client(), server.URL+"/v1", "", openai.ClientOptions{TokenEvidenceMode: mode})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	runner := benchmark.NewRunner(client)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	result := runner.RunRequest(ctx, benchmark.Request{
		RunID:           "run-integration",
		RequestID:       "req-000001",
		Model:           "fake-model",
		Prompt:          "private integration prompt",
		MaxOutputTokens: 4,
		Temperature:     0,
	})
	return result, metrics.Calculate(result.Observation)
}

func assertAtLeast(t *testing.T, actualMS float64, target, allowance time.Duration) {
	t.Helper()
	minimum := float64((target - allowance).Nanoseconds()) / float64(time.Millisecond)
	if actualMS < minimum {
		t.Fatalf("duration = %.3fms, want at least %.3fms", actualMS, minimum)
	}
}
