package fakeserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"
)

const maxRequestBodyBytes int64 = 1024 * 1024

type handler struct {
	config Config
}

func NewHandler(config Config) (http.Handler, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	return &handler{config: config}, nil
}

func (h *handler) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.URL.Path != "/v1/chat/completions" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAPIError(writer, http.StatusMethodNotAllowed, "method must be POST")
		return
	}
	if message := validateRequest(writer, request); message != "" {
		writeAPIError(writer, http.StatusBadRequest, message)
		return
	}
	if !wait(request.Context(), h.config.HeaderDelay) {
		return
	}
	if h.config.Mode == ModeHTTPError {
		writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(writer, "fake server configured HTTP error\n")
		return
	}

	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeAPIError(writer, http.StatusInternalServerError, "streaming is not supported by the response writer")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
	writer.WriteHeader(http.StatusOK)
	flusher.Flush()

	if !wait(request.Context(), h.config.FirstContentDelay) {
		return
	}
	if h.config.Mode == ModeMalformedJSON {
		_ = writeRawEvent(writer, flusher, "{broken-json")
		return
	}

	contentChunks := h.config.ContentChunks
	if h.config.Mode == ModeNoContent {
		contentChunks = 0
	}
	for index := 0; index < contentChunks; index++ {
		if index > 0 && !wait(request.Context(), h.config.ChunkInterval) {
			return
		}
		if err := writeJSONEvent(writer, flusher, contentEvent()); err != nil {
			return
		}
	}
	if err := writeJSONEvent(writer, flusher, finishEvent()); err != nil {
		return
	}
	if !wait(request.Context(), h.config.UsageDelay) {
		return
	}
	if err := writeJSONEvent(writer, flusher, usageEvent(h.config)); err != nil {
		return
	}
	if !wait(request.Context(), h.config.DoneDelay) {
		return
	}
	if h.config.Mode == ModeEOFBeforeDone {
		return
	}
	if err := writeRawEvent(writer, flusher, "[DONE]"); err != nil {
		return
	}
	if h.config.Mode == ModeDataAfterDone {
		_ = writeJSONEvent(writer, flusher, streamChunk{Choices: []streamChoice{}})
	}
}

func validateRequest(writer http.ResponseWriter, request *http.Request) string {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return "Content-Type must be application/json"
	}
	defer request.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes))
	var payload chatCompletionRequest
	if err := decoder.Decode(&payload); err != nil {
		var maximumBytesError *http.MaxBytesError
		if errors.As(err, &maximumBytesError) {
			return "request body exceeds 1 MiB"
		}
		return "request body must contain valid JSON"
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return "request body must contain exactly one JSON value"
	}
	if strings.TrimSpace(payload.Model) == "" {
		return "model is required"
	}
	if len(payload.Messages) == 0 {
		return "at least one message is required"
	}
	if !payload.Stream {
		return "stream must be true"
	}
	if !payload.StreamOptions.IncludeUsage {
		return "stream_options.include_usage must be true"
	}
	return ""
}

func writeAPIError(writer http.ResponseWriter, status int, message string) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(apiErrorEnvelope{Error: apiError{
		Message: message,
		Type:    "invalid_request_error",
	}})
}

func writeJSONEvent(writer io.Writer, flusher http.Flusher, event streamChunk) error {
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode stream event: %w", err)
	}
	return writeRawEvent(writer, flusher, string(payload))
}

func writeRawEvent(writer io.Writer, flusher http.Flusher, payload string) error {
	if _, err := fmt.Fprintf(writer, "data: %s\n\n", payload); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func contentEvent() streamChunk {
	return streamChunk{Choices: []streamChoice{{
		Index: 0,
		Delta: map[string]string{"content": "x"},
	}}}
}

func finishEvent() streamChunk {
	reason := "stop"
	return streamChunk{Choices: []streamChoice{{
		Index:        0,
		Delta:        map[string]string{},
		FinishReason: &reason,
	}}}
}

func usageEvent(config Config) streamChunk {
	return streamChunk{
		Choices: []streamChoice{},
		Usage: &streamUsage{
			PromptTokens:     config.PromptTokens,
			CompletionTokens: config.CompletionTokens,
			TotalTokens:      config.TotalTokens(),
		},
	}
}

func wait(ctx context.Context, duration time.Duration) bool {
	if duration == 0 {
		select {
		case <-ctx.Done():
			return false
		default:
			return true
		}
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
