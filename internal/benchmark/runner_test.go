package benchmark

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"
)

type executorFunc func(context.Context, Request, *RequestObservation) error

func (function executorFunc) Execute(ctx context.Context, request Request, observation *RequestObservation) error {
	return function(ctx, request, observation)
}

func TestRunnerReturnsContentBearingObservation(t *testing.T) {
	runner := NewRunner(executorFunc(func(_ context.Context, request Request, observation *RequestObservation) error {
		observation.StreamEvents = append(observation.StreamEvents, StreamEvent{
			Sequence: 1, ReceivedAt: time.Now(), HasContent: true, ContentBytes: 5,
		})
		return nil
	}))

	result := runner.RunRequest(context.Background(), Request{RequestID: "req-000001"})
	if result.Err != nil {
		t.Fatalf("RunRequest returned error: %v", result.Err)
	}
	if result.Observation.RequestID != "req-000001" || result.Observation.Error != "" {
		t.Fatalf("unexpected observation: %+v", result.Observation)
	}
	if result.Observation.Usage.Source != TokenUsageSourceUnavailable {
		t.Fatalf("usage source = %q", result.Observation.Usage.Source)
	}
}

func TestRunnerClassifiesNoContentAsFailure(t *testing.T) {
	runner := NewRunner(executorFunc(func(context.Context, Request, *RequestObservation) error { return nil }))
	result := runner.RunRequest(context.Background(), Request{RequestID: "req-000001"})
	if !errors.Is(result.Err, ErrNoGeneratedContent) {
		t.Fatalf("error = %v, want ErrNoGeneratedContent", result.Err)
	}
	if result.Observation.Error != ErrNoGeneratedContent.Error() {
		t.Fatalf("observation error = %q", result.Observation.Error)
	}
}

func TestRunnerPreservesExecutorError(t *testing.T) {
	want := errors.New("request failed")
	runner := NewRunner(executorFunc(func(context.Context, Request, *RequestObservation) error { return want }))
	result := runner.RunRequest(context.Background(), Request{RequestID: "req-000001"})
	if !errors.Is(result.Err, want) || result.Observation.Error != want.Error() {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestIdentifiers(t *testing.T) {
	now := time.Date(2026, 8, 16, 12, 5, 1, 0, time.FixedZone("test", 5*60*60+30*60))
	runID, err := newRunID(now, bytes.NewReader([]byte{0xa3, 0x1f, 0x00, 0xff}))
	if err != nil {
		t.Fatalf("newRunID: %v", err)
	}
	if runID != "20260816T063501Z-a31f00ff" {
		t.Fatalf("run ID = %q", runID)
	}
	requestID, err := RequestID(2)
	if err != nil || requestID != "req-000002" {
		t.Fatalf("request ID = %q, err = %v", requestID, err)
	}
	if _, err := RequestID(0); err == nil {
		t.Fatal("RequestID(0) unexpectedly succeeded")
	}
}
