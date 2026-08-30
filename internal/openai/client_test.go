package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
)

func TestClientCapturesStreamingFixture(t *testing.T) {
	fixture := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer api-secret" {
			t.Errorf("Authorization = %q", got)
		}
		if got := request.Header.Get("Accept"); got != "text/event-stream" {
			t.Errorf("Accept = %q", got)
		}
		var payload chatCompletionRequest
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if !payload.Stream || !payload.StreamOptions.IncludeUsage || payload.Model != "test-model" || payload.MaxTokens != 64 || payload.Temperature != 0 {
			t.Errorf("unexpected request payload: %+v", payload)
		}
		if payload.ReturnTokenIDs != nil {
			t.Errorf("return_token_ids unexpectedly present: %v", *payload.ReturnTokenIDs)
		}
		if len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || payload.Messages[0].Content != "private prompt" {
			t.Errorf("unexpected messages: %+v", payload.Messages)
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	client, err := NewClient(server.Client(), server.URL+"/v1/", "api-secret")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	observation := newObservation()
	err = client.Execute(context.Background(), benchmark.Request{
		RunID: "run-001", RequestID: "req-000001", Model: "test-model", Prompt: "private prompt", MaxOutputTokens: 64,
	}, &observation)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if observation.RequestStartedAt == nil || observation.FirstByteAt == nil || observation.HeadersReceivedAt == nil || observation.CompletedAt == nil {
		t.Fatalf("missing HTTP timestamps: %+v", observation)
	}
	if observation.FirstByteAfterNS == nil || observation.HeadersAfterNS == nil || observation.CompletedAfterNS == nil {
		t.Fatalf("missing HTTP relative offsets: %+v", observation)
	}
	if observation.FirstStreamEventAt == nil || observation.FirstContentAt == nil || observation.LastContentAt == nil {
		t.Fatalf("missing stream timestamps: %+v", observation)
	}
	if observation.FirstStreamEventAfterNS == nil || observation.FirstContentAfterNS == nil || observation.LastContentAfterNS == nil {
		t.Fatalf("missing stream relative offsets: %+v", observation)
	}
	if observation.StatusCode != http.StatusOK || observation.ResponseBodyBytes != int64(len(fixture)) {
		t.Fatalf("status/body bytes = %d/%d", observation.StatusCode, observation.ResponseBodyBytes)
	}
	if len(observation.StreamEvents) != 4 {
		t.Fatalf("stream events = %d, want 4", len(observation.StreamEvents))
	}
	contentEvents := 0
	contentBytes := 0
	for index, event := range observation.StreamEvents {
		if event.Sequence != index+1 {
			t.Fatalf("event sequence = %d at index %d", event.Sequence, index)
		}
		if event.HasContent {
			contentEvents++
			contentBytes += event.ContentBytes
		}
		if event.ReceivedAfterNS < 0 || index > 0 && event.ReceivedAfterNS < observation.StreamEvents[index-1].ReceivedAfterNS {
			t.Fatalf("unordered stream relative offsets: %+v", observation.StreamEvents)
		}
	}
	if contentEvents != 2 || contentBytes != 11 {
		t.Fatalf("content events/bytes = %d/%d", contentEvents, contentBytes)
	}
	if !observation.Usage.Available || observation.Usage.Source != "server_usage" || observation.Usage.InputTokens != 5 || observation.Usage.OutputTokens != 2 || observation.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", observation.Usage)
	}
	if observation.TokenTiming.Requested || observation.TokenTiming.Source != benchmark.TokenTimingSourceUnavailable || !observation.TokenTiming.CompletedThroughDone {
		t.Fatalf("unexpected disabled token timing evidence: %+v", observation.TokenTiming)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("marshal observation: %v", err)
	}
	for _, sensitive := range []string{"private prompt", "Hello", " world", "api-secret"} {
		if strings.Contains(string(encoded), sensitive) {
			t.Fatalf("observation contains sensitive payload %q", sensitive)
		}
	}
}

func TestClientSerializesTokenEvidenceMode(t *testing.T) {
	tests := []struct {
		name        string
		mode        TokenEvidenceMode
		wantPresent bool
	}{
		{name: "disabled omits extension", mode: TokenEvidenceDisabled},
		{name: "vllm requests IDs", mode: TokenEvidenceVLLM, wantPresent: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				var payload map[string]json.RawMessage
				if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
					t.Errorf("decode request: %v", err)
				}
				encoded, present := payload["return_token_ids"]
				if present != test.wantPresent {
					t.Errorf("return_token_ids present = %t, want %t; payload=%s", present, test.wantPresent, payload)
				}
				if present && string(encoded) != "true" {
					t.Errorf("return_token_ids = %s, want true", encoded)
				}
				for _, forbidden := range []string{"logprobs", "prompt_logprobs", "return_tokens_as_token_ids", "stream_interval"} {
					if _, exists := payload[forbidden]; exists {
						t.Errorf("unexpected request field %q", forbidden)
					}
				}
				writer.Header().Set("Content-Type", "text/event-stream")
				_, _ = io.WriteString(writer, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\ndata: [DONE]\n\n")
			}))
			defer server.Close()
			client, err := NewClientWithOptions(server.Client(), server.URL+"/v1", "", ClientOptions{TokenEvidenceMode: test.mode})
			if err != nil {
				t.Fatalf("NewClientWithOptions: %v", err)
			}
			observation := newObservation()
			if err := client.Execute(context.Background(), benchmark.Request{Model: "model", Prompt: "prompt", MaxOutputTokens: 1}, &observation); err != nil {
				t.Fatalf("Execute: %v", err)
			}
		})
	}
}

func TestNewClientRejectsUnknownTokenEvidenceMode(t *testing.T) {
	if _, err := NewClientWithOptions(http.DefaultClient, "http://example.test/v1", "", ClientOptions{TokenEvidenceMode: "automatic"}); err == nil || !strings.Contains(err.Error(), "disabled or vllm") {
		t.Fatalf("error = %v", err)
	}
}

func TestClientCapturesVLLMTokenCardinalityWithoutPersistingIDs(t *testing.T) {
	fixture := "data: {\"prompt_token_ids\":[987654300,987654301],\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":1,\"delta\":{\"content\":\"ignored\"},\"token_ids\":[987654399]},{\"index\":0,\"delta\":{\"content\":\"x\"},\"token_ids\":[987654321]}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\",\"token_ids\":[987654322]}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"token_ids\":[]}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":2,\"completion_tokens\":2,\"total_tokens\":4}}\n\n" +
		"data: [DONE]\n\n"
	observation, err := executeFixtureWithMode(t, fixture, TokenEvidenceVLLM)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if !observation.TokenTiming.Requested || observation.TokenTiming.Source != benchmark.TokenTimingSourceVLLM || !observation.TokenTiming.CompletedThroughDone || observation.TokenTiming.InvalidReason != "" {
		t.Fatalf("token timing evidence = %+v", observation.TokenTiming)
	}
	wantPresent := []bool{false, true, true, true, false, false}
	wantCounts := []int{0, 1, 1, 0, 0, 0}
	if len(observation.StreamEvents) != len(wantCounts) {
		t.Fatalf("events = %+v", observation.StreamEvents)
	}
	for index, event := range observation.StreamEvents {
		if event.TokenIDsPresent != wantPresent[index] || event.GeneratedTokenCount != wantCounts[index] {
			t.Fatalf("event %d = %+v", index, event)
		}
	}
	if observation.FirstContentAt == nil || observation.LastContentAt == nil || !observation.FirstContentAt.Equal(*observation.LastContentAt) {
		t.Fatalf("TTFT content evidence changed by non-content token: %+v", observation)
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, sentinel := range []string{"987654300", "987654301", "987654321", "987654322", "987654399", "ignored"} {
		if strings.Contains(string(encoded), sentinel) {
			t.Fatalf("observation retained token/content sentinel %q: %s", sentinel, encoded)
		}
	}
}

func TestClientRecordsNegativeTokenIDAsInvalidEvidence(t *testing.T) {
	fixture := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"token_ids\":[-7]}]}\n\ndata: [DONE]\n\n"
	observation, err := executeFixtureWithMode(t, fixture, TokenEvidenceVLLM)
	if err != nil {
		t.Fatalf("negative semantic evidence failed request: %v", err)
	}
	if !strings.Contains(observation.TokenTiming.InvalidReason, "negative") || observation.StreamEvents[0].GeneratedTokenCount != 1 {
		t.Fatalf("invalid evidence = %+v, events=%+v", observation.TokenTiming, observation.StreamEvents)
	}
}

func TestClientCapturesDeterministicRelativeOffsets(t *testing.T) {
	fixture := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2,\"total_tokens\":7}}\n\n" +
		"data: [DONE]\n\n"
	start := time.Now()
	timestamps := []time.Time{
		start,
		start.Add(40 * time.Millisecond),
		start.Add(45 * time.Millisecond),
		start.Add(100 * time.Millisecond),
		start.Add(120 * time.Millisecond),
		start.Add(150 * time.Millisecond),
		start.Add(190 * time.Millisecond),
		start.Add(200 * time.Millisecond),
	}
	nextTimestamp := 0
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		trace := httptrace.ContextClientTrace(request.Context())
		if trace == nil || trace.GotFirstResponseByte == nil {
			t.Fatal("first-byte trace hook not installed")
		}
		trace.GotFirstResponseByte()
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(fixture)),
			Request:    request,
		}, nil
	})
	client, err := NewClient(&http.Client{Transport: transport}, "http://example.test/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	client.now = func() time.Time {
		if nextTimestamp >= len(timestamps) {
			t.Fatalf("clock sampled more than %d times", len(timestamps))
		}
		observed := timestamps[nextTimestamp]
		nextTimestamp++
		return observed
	}

	observation := newObservation()
	err = client.Execute(context.Background(), benchmark.Request{
		RunID: "run-001", RequestID: "req-000001", Model: "model", Prompt: "prompt", MaxOutputTokens: 64,
	}, &observation)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if nextTimestamp != len(timestamps) {
		t.Fatalf("clock sampled %d times, want %d", nextTimestamp, len(timestamps))
	}
	assertTimestampOffset(t, observation.RequestStartedAt, observation.FirstByteAt, observation.FirstByteAfterNS, 40*time.Millisecond)
	assertTimestampOffset(t, observation.RequestStartedAt, observation.HeadersReceivedAt, observation.HeadersAfterNS, 45*time.Millisecond)
	assertTimestampOffset(t, observation.RequestStartedAt, observation.FirstStreamEventAt, observation.FirstStreamEventAfterNS, 100*time.Millisecond)
	assertTimestampOffset(t, observation.RequestStartedAt, observation.FirstContentAt, observation.FirstContentAfterNS, 100*time.Millisecond)
	assertTimestampOffset(t, observation.RequestStartedAt, observation.LastContentAt, observation.LastContentAfterNS, 120*time.Millisecond)
	assertTimestampOffset(t, observation.RequestStartedAt, observation.CompletedAt, observation.CompletedAfterNS, 200*time.Millisecond)
	wantEventOffsets := []int64{
		(100 * time.Millisecond).Nanoseconds(),
		(120 * time.Millisecond).Nanoseconds(),
		(150 * time.Millisecond).Nanoseconds(),
		(190 * time.Millisecond).Nanoseconds(),
	}
	for index, want := range wantEventOffsets {
		if observation.StreamEvents[index].ReceivedAfterNS != want {
			t.Fatalf("event %d offset = %d, want %d", index, observation.StreamEvents[index].ReceivedAfterNS, want)
		}
	}
}

func TestClientHandlesNonContentEvents(t *testing.T) {
	fixture := "data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"\"},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":0,\"total_tokens\":3}}\n\n" +
		"data: [DONE]\n\n"
	observation, err := executeFixture(t, fixture, http.StatusOK)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if observation.FinishReason != "stop" || !observation.Usage.Available || observation.FirstContentAt != nil {
		t.Fatalf("unexpected observation: %+v", observation)
	}
	if observation.FirstContentAfterNS != nil || observation.LastContentAfterNS != nil {
		t.Fatalf("non-content stream has content offsets: %+v", observation)
	}
	for _, event := range observation.StreamEvents {
		if event.HasContent || event.ContentBytes != 0 {
			t.Fatalf("non-content event marked as content: %+v", event)
		}
	}
}

func TestClientCountsUTF8ContentBytes(t *testing.T) {
	fixture := "data: {\"choices\":[{\"delta\":{\"content\":\"é\"}}]}\n\ndata: [DONE]\n\n"
	observation, err := executeFixture(t, fixture, http.StatusOK)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if len(observation.StreamEvents) != 2 || !observation.StreamEvents[0].HasContent || observation.StreamEvents[0].ContentBytes != len([]byte("é")) {
		t.Fatalf("unexpected UTF-8 byte accounting: %+v", observation.StreamEvents)
	}
}

func TestClientStreamingErrorsPreservePartialObservation(t *testing.T) {
	tests := []struct {
		name      string
		fixture   string
		wantError string
		forbidden string
	}{
		{
			name:      "malformed JSON",
			fixture:   "data: SECRET_PAYLOAD\n\ndata: [DONE]\n\n",
			wantError: "decode SSE JSON",
			forbidden: "SECRET_PAYLOAD",
		},
		{
			name:      "unexpected EOF",
			fixture:   "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n",
			wantError: ErrUnexpectedEOF.Error(),
			forbidden: "partial",
		},
		{
			name:      "data after done",
			fixture:   "data: [DONE]\n\ndata: {\"choices\":[]}\n\n",
			wantError: "after [DONE]",
		},
		{
			name:      "negative usage",
			fixture:   "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":-1,\"completion_tokens\":0,\"total_tokens\":-1}}\n\ndata: [DONE]\n\n",
			wantError: "negative token count",
		},
		{
			name:      "malformed token_ids type",
			fixture:   "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"},\"token_ids\":\"bad\"}]}\n\ndata: [DONE]\n\n",
			wantError: "decode SSE JSON",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observation, err := executeFixture(t, test.fixture, http.StatusOK)
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want substring %q", err, test.wantError)
			}
			if test.forbidden != "" && strings.Contains(err.Error(), test.forbidden) {
				t.Fatalf("error leaks payload %q: %v", test.forbidden, err)
			}
			if observation.RequestStartedAt == nil || observation.HeadersReceivedAt == nil || observation.CompletedAt == nil {
				t.Fatalf("partial timestamps missing: %+v", observation)
			}
		})
	}
}

func TestClientNon2xxDiscardsBodyWithoutLeakingIt(t *testing.T) {
	const privateBody = "private upstream failure detail"
	observation, err := executeFixture(t, privateBody, http.StatusBadGateway)
	if err == nil || !strings.Contains(err.Error(), "HTTP status 502") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(err.Error(), privateBody) {
		t.Fatalf("error leaked response body: %v", err)
	}
	if observation.StatusCode != http.StatusBadGateway || observation.ResponseBodyBytes != int64(len(privateBody)) || observation.CompletedAt == nil {
		t.Fatalf("unexpected partial observation: %+v", observation)
	}
}

func TestClientContextCancellation(t *testing.T) {
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return nil, request.Context().Err()
	})
	client, err := NewClient(&http.Client{Transport: transport}, "http://example.test/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	observation := newObservation()
	err = client.Execute(ctx, benchmark.Request{Model: "model", Prompt: "prompt", MaxOutputTokens: 1}, &observation)
	if err == nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if observation.RunID != "run-001" || observation.RequestID != "req-000001" || observation.RequestStartedAt == nil || observation.CompletedAt == nil || observation.CompletedAfterNS == nil || observation.FirstByteAt != nil || observation.FirstByteAfterNS != nil || observation.HeadersReceivedAt != nil || observation.HeadersAfterNS != nil || observation.StatusCode != 0 {
		t.Fatalf("unexpected cancellation observation: %+v", observation)
	}
}

func TestClientTransportAndDeadlineFailures(t *testing.T) {
	t.Run("DNS failure", func(t *testing.T) {
		transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
			return nil, &net.DNSError{Err: "no such host", Name: "example.test", IsNotFound: true}
		})
		client, err := NewClient(&http.Client{Transport: transport}, "http://example.test/v1", "")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		observation := newObservation()
		err = client.Execute(context.Background(), benchmark.Request{Model: "model", Prompt: "prompt", MaxOutputTokens: 1}, &observation)
		var dnsError *net.DNSError
		if err == nil || !errors.As(err, &dnsError) {
			t.Fatalf("error = %v, want DNS error", err)
		}
		if observation.RunID != "run-001" || observation.RequestID != "req-000001" || observation.RequestStartedAt == nil || observation.CompletedAt == nil || observation.CompletedAfterNS == nil || observation.HeadersReceivedAt != nil || observation.HeadersAfterNS != nil || observation.StatusCode != 0 {
			t.Fatalf("unexpected DNS failure observation: %+v", observation)
		}
	})

	t.Run("expired deadline", func(t *testing.T) {
		transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
			return nil, request.Context().Err()
		})
		client, err := NewClient(&http.Client{Transport: transport}, "http://example.test/v1", "")
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
		defer cancel()
		observation := newObservation()
		err = client.Execute(ctx, benchmark.Request{Model: "model", Prompt: "prompt", MaxOutputTokens: 1}, &observation)
		if err == nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("error = %v, want context.DeadlineExceeded", err)
		}
		if observation.RequestStartedAt == nil || observation.CompletedAt == nil || observation.CompletedAfterNS == nil || observation.HeadersReceivedAt != nil || observation.HeadersAfterNS != nil || observation.StatusCode != 0 {
			t.Fatalf("unexpected deadline observation: %+v", observation)
		}
	})
}

func executeFixture(t *testing.T, fixture string, status int) (benchmark.RequestObservation, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(status)
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()
	client, err := NewClient(server.Client(), server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	observation := newObservation()
	err = client.Execute(context.Background(), benchmark.Request{
		RunID: "run-001", RequestID: "req-000001", Model: "model", Prompt: "prompt", MaxOutputTokens: 64,
	}, &observation)
	return observation, err
}

func executeFixtureWithMode(t *testing.T, fixture string, mode TokenEvidenceMode) (benchmark.RequestObservation, error) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()
	client, err := NewClientWithOptions(server.Client(), server.URL+"/v1", "", ClientOptions{TokenEvidenceMode: mode})
	if err != nil {
		t.Fatalf("NewClientWithOptions: %v", err)
	}
	observation := newObservation()
	err = client.Execute(context.Background(), benchmark.Request{Model: "model", Prompt: "prompt", MaxOutputTokens: 64}, &observation)
	return observation, err
}

func newObservation() benchmark.RequestObservation {
	return benchmark.RequestObservation{
		RunID:        "run-001",
		RequestID:    "req-000001",
		StreamEvents: make([]benchmark.StreamEvent, 0),
		Usage:        benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable},
	}
}

func assertTimestampOffset(t *testing.T, started, observed *time.Time, offset *int64, want time.Duration) {
	t.Helper()
	if started == nil || observed == nil || offset == nil {
		t.Fatalf("missing timestamp/offset pair: started=%v observed=%v offset=%v", started, observed, offset)
	}
	if got := observed.Sub(*started); got != want {
		t.Fatalf("wall timestamp delta = %s, want %s", got, want)
	}
	if *offset != want.Nanoseconds() {
		t.Fatalf("relative offset = %d, want %d", *offset, want.Nanoseconds())
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestClientFirstByteObservationIsRaceFreeUnderConcurrency(t *testing.T) {
	const workers = 50
	fixture := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}\n\n" +
		"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":1,\"total_tokens\":6}}\n\n" +
		"data: [DONE]\n\n"
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		// Flush headers so the transport's reader goroutine receives the first
		// response byte and fires GotFirstResponseByte on a separate goroutine
		// while Execute is still blocked in httpClient.Do.
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		time.Sleep(2 * time.Millisecond)
		_, _ = io.WriteString(writer, fixture)
	}))
	defer server.Close()

	client, err := NewClient(server.Client(), server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var wg sync.WaitGroup
	errs := make(chan error, workers)
	observations := make([]benchmark.RequestObservation, workers)
	start := make(chan struct{})
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		observations[i] = newObservation()
		go func(i int) {
			defer wg.Done()
			<-start
			errs <- client.Execute(context.Background(), benchmark.Request{
				RunID: "run-001", RequestID: "req-000001", Model: "model", Prompt: "prompt", MaxOutputTokens: 64,
			}, &observations[i])
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	completed := 0
	for err := range errs {
		if err != nil {
			t.Fatalf("Execute returned error under concurrency: %v", err)
		}
		completed++
	}
	if completed != workers {
		t.Fatalf("completed = %d, want %d", completed, workers)
	}
	for i := range observations {
		if observations[i].FirstByteAt == nil || observations[i].FirstByteAfterNS == nil {
			t.Fatalf("observation %d missing first-byte timestamp: %+v", i, observations[i])
		}
		if observations[i].FirstByteAfterNS != nil && *observations[i].FirstByteAfterNS < 0 {
			t.Fatalf("observation %d has negative first-byte offset: %d", i, *observations[i].FirstByteAfterNS)
		}
	}
}

func TestClientFirstByteObservationCapturedOnCancelledRequest(t *testing.T) {
	const workers = 25
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		if flusher, ok := writer.(http.Flusher); ok {
			flusher.Flush()
		}
		// Hold the response body open so the transport fires GotFirstResponseByte
		// (headers flushed) but the request is cancelled before the body completes.
		select {
		case <-request.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer server.Close()

	client, err := NewClient(server.Client(), server.URL+"/v1", "")
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}

	var wg sync.WaitGroup
	start := make(chan struct{})
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		observation := newObservation()
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			defer wg.Done()
			defer cancel()
			<-start
			_ = client.Execute(ctx, benchmark.Request{
				RunID: "run-001", RequestID: "req-000001", Model: "model", Prompt: "prompt", MaxOutputTokens: 64,
			}, &observation)
		}()
	}
	close(start)

	// Cancel all in-flight requests shortly after they start, racing the
	// GotFirstResponseByte callback against Execute's deferred cleanup.
	time.Sleep(10 * time.Millisecond)
	wg.Wait()

	// The test passes if no race is detected; the cancelled requests may or may
	// not have received a first byte, but Execute must not race on the
	// observation struct.
}
