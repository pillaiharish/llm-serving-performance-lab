package openai

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
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
		RequestID: "req-000001", Model: "test-model", Prompt: "private prompt", MaxOutputTokens: 64,
	}, &observation)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if observation.RequestStartedAt == nil || observation.FirstByteAt == nil || observation.HeadersReceivedAt == nil || observation.CompletedAt == nil {
		t.Fatalf("missing HTTP timestamps: %+v", observation)
	}
	if observation.FirstStreamEventAt == nil || observation.FirstContentAt == nil || observation.LastContentAt == nil {
		t.Fatalf("missing stream timestamps: %+v", observation)
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
	}
	if contentEvents != 2 || contentBytes != 11 {
		t.Fatalf("content events/bytes = %d/%d", contentEvents, contentBytes)
	}
	if !observation.Usage.Available || observation.Usage.Source != "server_usage" || observation.Usage.InputTokens != 5 || observation.Usage.OutputTokens != 2 || observation.Usage.TotalTokens != 7 {
		t.Fatalf("unexpected usage: %+v", observation.Usage)
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
	if observation.RequestStartedAt == nil || observation.CompletedAt == nil || observation.FirstByteAt != nil || observation.StatusCode != 0 {
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
		if observation.RequestStartedAt == nil || observation.CompletedAt == nil || observation.StatusCode != 0 {
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
		if observation.RequestStartedAt == nil || observation.CompletedAt == nil || observation.StatusCode != 0 {
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
		RequestID: "req-000001", Model: "model", Prompt: "prompt", MaxOutputTokens: 64,
	}, &observation)
	return observation, err
}

func newObservation() benchmark.RequestObservation {
	return benchmark.RequestObservation{
		RequestID:    "req-000001",
		StreamEvents: make([]benchmark.StreamEvent, 0),
		Usage:        benchmark.TokenUsage{Source: benchmark.TokenUsageSourceUnavailable},
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}
