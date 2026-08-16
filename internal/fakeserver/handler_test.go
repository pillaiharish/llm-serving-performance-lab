package fakeserver

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestHandlerNormalStreamAndFlushing(t *testing.T) {
	config := zeroDelayConfig()
	handler := mustHandler(t, config)
	recorder := newFlushRecorder()
	request := validRequest(t, "private fixture prompt")
	request.Header.Set("Authorization", "Bearer private-api-secret")

	handler.ServeHTTP(recorder, request)

	if recorder.status != http.StatusOK {
		t.Fatalf("status = %d, body = %q", recorder.status, recorder.body.String())
	}
	if got := recorder.header.Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("Content-Type = %q", got)
	}
	events := parseDataEvents(t, recorder.body.String())
	if len(events) != 7 {
		t.Fatalf("events = %d, want 7: %q", len(events), events)
	}
	contentEvents := 0
	for index := 0; index < 4; index++ {
		var chunk streamChunk
		decodeEvent(t, events[index], &chunk)
		if len(chunk.Choices) != 1 || chunk.Choices[0].Delta["content"] != "x" {
			t.Fatalf("content event %d = %+v", index, chunk)
		}
		contentEvents++
	}
	var finish streamChunk
	decodeEvent(t, events[4], &finish)
	if len(finish.Choices) != 1 || finish.Choices[0].FinishReason == nil || *finish.Choices[0].FinishReason != "stop" || len(finish.Choices[0].Delta) != 0 {
		t.Fatalf("finish event = %+v", finish)
	}
	var usage streamChunk
	decodeEvent(t, events[5], &usage)
	if usage.Usage == nil || usage.Usage.PromptTokens != 16 || usage.Usage.CompletionTokens != 4 || usage.Usage.TotalTokens != 20 || len(usage.Choices) != 0 {
		t.Fatalf("usage event = %+v", usage)
	}
	if events[6] != "[DONE]" {
		t.Fatalf("last event = %q", events[6])
	}
	if contentEvents != config.ContentChunks {
		t.Fatalf("content events = %d", contentEvents)
	}
	if recorder.flushes != 8 {
		t.Fatalf("flush count = %d, want header plus seven events", recorder.flushes)
	}
	for _, forbidden := range []string{"private fixture prompt", "private-api-secret", "Authorization"} {
		if strings.Contains(recorder.body.String(), forbidden) {
			t.Fatalf("response contains forbidden value %q", forbidden)
		}
	}
}

func TestHandlerModes(t *testing.T) {
	tests := []struct {
		mode       Mode
		wantStatus int
		wantEvents int
		check      func(*testing.T, *flushRecorder, []string)
	}{
		{
			mode: ModeNoContent, wantStatus: http.StatusOK, wantEvents: 3,
			check: func(t *testing.T, recorder *flushRecorder, events []string) {
				var finish streamChunk
				decodeEvent(t, events[0], &finish)
				if len(finish.Choices) != 1 || finish.Choices[0].FinishReason == nil {
					t.Fatalf("missing finish event: %+v", finish)
				}
				if events[2] != "[DONE]" || recorder.flushes != 4 {
					t.Fatalf("no-content stream = %q, flushes = %d", events, recorder.flushes)
				}
			},
		},
		{
			mode: ModeHTTPError, wantStatus: http.StatusServiceUnavailable,
			check: func(t *testing.T, recorder *flushRecorder, _ []string) {
				if recorder.body.String() != "fake server configured HTTP error\n" || recorder.flushes != 0 {
					t.Fatalf("HTTP error body/flushes = %q/%d", recorder.body.String(), recorder.flushes)
				}
			},
		},
		{
			mode: ModeMalformedJSON, wantStatus: http.StatusOK, wantEvents: 1,
			check: func(t *testing.T, recorder *flushRecorder, events []string) {
				if events[0] != "{broken-json" || recorder.flushes != 2 {
					t.Fatalf("malformed stream = %q, flushes = %d", events, recorder.flushes)
				}
			},
		},
		{
			mode: ModeEOFBeforeDone, wantStatus: http.StatusOK, wantEvents: 6,
			check: func(t *testing.T, recorder *flushRecorder, events []string) {
				if strings.Contains(strings.Join(events, "\n"), "[DONE]") || recorder.flushes != 7 {
					t.Fatalf("EOF stream = %q, flushes = %d", events, recorder.flushes)
				}
			},
		},
		{
			mode: ModeDataAfterDone, wantStatus: http.StatusOK, wantEvents: 8,
			check: func(t *testing.T, recorder *flushRecorder, events []string) {
				if events[6] != "[DONE]" || events[7] != `{"choices":[]}` || recorder.flushes != 9 {
					t.Fatalf("data-after-DONE stream = %q, flushes = %d", events, recorder.flushes)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(string(test.mode), func(t *testing.T) {
			config := zeroDelayConfig()
			config.Mode = test.mode
			recorder := newFlushRecorder()
			mustHandler(t, config).ServeHTTP(recorder, validRequest(t, "private prompt"))
			if recorder.status != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %q", recorder.status, test.wantStatus, recorder.body.String())
			}
			var events []string
			if test.wantStatus == http.StatusOK {
				events = parseDataEvents(t, recorder.body.String())
				if len(events) != test.wantEvents {
					t.Fatalf("events = %d, want %d: %q", len(events), test.wantEvents, events)
				}
			}
			test.check(t, recorder, events)
		})
	}
}

func TestHandlerRequestValidation(t *testing.T) {
	validBody := validRequestJSON("private prompt")
	tests := []struct {
		name        string
		method      string
		path        string
		contentType string
		body        string
		wantStatus  int
		wantError   string
	}{
		{name: "wrong path", method: http.MethodPost, path: "/other", contentType: "application/json", body: validBody, wantStatus: http.StatusNotFound},
		{name: "wrong method", method: http.MethodGet, path: "/v1/chat/completions", contentType: "application/json", body: validBody, wantStatus: http.StatusMethodNotAllowed, wantError: "method must be POST"},
		{name: "missing content type", method: http.MethodPost, path: "/v1/chat/completions", body: validBody, wantStatus: http.StatusBadRequest, wantError: "Content-Type"},
		{name: "wrong content type", method: http.MethodPost, path: "/v1/chat/completions", contentType: "text/plain", body: validBody, wantStatus: http.StatusBadRequest, wantError: "Content-Type"},
		{name: "malformed JSON", method: http.MethodPost, path: "/v1/chat/completions", contentType: "application/json", body: "{broken", wantStatus: http.StatusBadRequest, wantError: "valid JSON"},
		{name: "trailing JSON", method: http.MethodPost, path: "/v1/chat/completions", contentType: "application/json", body: validBody + `{}`, wantStatus: http.StatusBadRequest, wantError: "exactly one"},
		{name: "missing model", method: http.MethodPost, path: "/v1/chat/completions", contentType: "application/json", body: `{"messages":[{}],"stream":true,"stream_options":{"include_usage":true}}`, wantStatus: http.StatusBadRequest, wantError: "model is required"},
		{name: "missing messages", method: http.MethodPost, path: "/v1/chat/completions", contentType: "application/json", body: `{"model":"m","stream":true,"stream_options":{"include_usage":true}}`, wantStatus: http.StatusBadRequest, wantError: "message"},
		{name: "stream false", method: http.MethodPost, path: "/v1/chat/completions", contentType: "application/json", body: `{"model":"m","messages":[{}],"stream":false,"stream_options":{"include_usage":true}}`, wantStatus: http.StatusBadRequest, wantError: "stream must"},
		{name: "usage false", method: http.MethodPost, path: "/v1/chat/completions", contentType: "application/json", body: `{"model":"m","messages":[{}],"stream":true,"stream_options":{"include_usage":false}}`, wantStatus: http.StatusBadRequest, wantError: "include_usage"},
		{name: "oversized body", method: http.MethodPost, path: "/v1/chat/completions", contentType: "application/json", body: `{"model":"` + strings.Repeat("x", int(maxRequestBodyBytes)) + `"}`, wantStatus: http.StatusBadRequest, wantError: "1 MiB"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(test.method, test.path, strings.NewReader(test.body))
			if test.contentType != "" {
				request.Header.Set("Content-Type", test.contentType)
			}
			request.Header.Set("Authorization", "Bearer private-api-secret")
			recorder := newFlushRecorder()
			mustHandler(t, zeroDelayConfig()).ServeHTTP(recorder, request)
			if recorder.status != test.wantStatus {
				t.Fatalf("status = %d, want %d, body = %q", recorder.status, test.wantStatus, recorder.body.String())
			}
			if test.wantError != "" && !strings.Contains(recorder.body.String(), test.wantError) {
				t.Fatalf("body = %q, want substring %q", recorder.body.String(), test.wantError)
			}
			for _, forbidden := range []string{"private prompt", "private-api-secret", "Authorization"} {
				if strings.Contains(recorder.body.String(), forbidden) {
					t.Fatalf("error response contains forbidden value %q", forbidden)
				}
			}
		})
	}
}

func TestHandlerFirstContentDelayRespectsCancellation(t *testing.T) {
	config := zeroDelayConfig()
	config.FirstContentDelay = 10 * time.Second
	handler := mustHandler(t, config)
	ctx, cancel := context.WithCancel(context.Background())
	request := validRequest(t, "private prompt").WithContext(ctx)
	recorder := newNotifyingFlushRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-recorder.firstFlush:
	case <-time.After(time.Second):
		t.Fatal("headers were not flushed")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("handler did not return promptly after cancellation")
	}
	if events := parseDataEvents(t, recorder.body.String()); len(events) != 0 {
		t.Fatalf("events after cancellation = %q", events)
	}
}

func zeroDelayConfig() Config {
	config := DefaultConfig()
	config.HeaderDelay = 0
	config.FirstContentDelay = 0
	config.ChunkInterval = 0
	config.UsageDelay = 0
	config.DoneDelay = 0
	return config
}

func mustHandler(t *testing.T, config Config) http.Handler {
	t.Helper()
	handler, err := NewHandler(config)
	if err != nil {
		t.Fatalf("NewHandler: %v", err)
	}
	return handler
}

func validRequest(t *testing.T, prompt string) *http.Request {
	t.Helper()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(validRequestJSON(prompt)))
	request.Header.Set("Content-Type", "application/json; charset=utf-8")
	return request
}

func validRequestJSON(prompt string) string {
	payload := map[string]any{
		"model":          "fake-model",
		"messages":       []map[string]string{{"role": "user", "content": prompt}},
		"stream":         true,
		"stream_options": map[string]bool{"include_usage": true},
		"unknown_option": "accepted",
	}
	encoded, _ := json.Marshal(payload)
	return string(encoded)
}

func parseDataEvents(t *testing.T, body string) []string {
	t.Helper()
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return nil
	}
	frames := strings.Split(trimmed, "\n\n")
	events := make([]string, 0, len(frames))
	for _, frame := range frames {
		if !strings.HasPrefix(frame, "data: ") {
			t.Fatalf("invalid SSE frame %q", frame)
		}
		events = append(events, strings.TrimPrefix(frame, "data: "))
	}
	return events
}

func decodeEvent(t *testing.T, data string, target any) {
	t.Helper()
	if err := json.Unmarshal([]byte(data), target); err != nil {
		t.Fatalf("decode event %q: %v", data, err)
	}
}

type flushRecorder struct {
	header  http.Header
	status  int
	body    bytes.Buffer
	flushes int
}

func newFlushRecorder() *flushRecorder {
	return &flushRecorder{header: make(http.Header)}
}

func (r *flushRecorder) Header() http.Header {
	return r.header
}

func (r *flushRecorder) WriteHeader(status int) {
	if r.status == 0 {
		r.status = status
	}
}

func (r *flushRecorder) Write(data []byte) (int, error) {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	return r.body.Write(data)
}

func (r *flushRecorder) Flush() {
	if r.status == 0 {
		r.WriteHeader(http.StatusOK)
	}
	r.flushes++
}

type notifyingFlushRecorder struct {
	*flushRecorder
	firstFlush chan struct{}
	once       sync.Once
}

func newNotifyingFlushRecorder() *notifyingFlushRecorder {
	return &notifyingFlushRecorder{
		flushRecorder: newFlushRecorder(),
		firstFlush:    make(chan struct{}),
	}
}

func (r *notifyingFlushRecorder) Flush() {
	r.flushRecorder.Flush()
	r.once.Do(func() { close(r.firstFlush) })
}
