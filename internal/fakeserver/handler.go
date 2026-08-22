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
	if request.URL.Path == "/tokenize" && h.config.TokenizerFixture {
		h.serveTokenize(writer, request)
		return
	}
	if request.URL.Path != "/v1/chat/completions" {
		http.NotFound(writer, request)
		return
	}
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAPIError(writer, http.StatusMethodNotAllowed, "method must be POST")
		return
	}
	payload, message := validateRequest(writer, request)
	if message != "" {
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
	wantsTokenIDs := payload.ReturnTokenIDs != nil && *payload.ReturnTokenIDs
	if wantsTokenIDs && h.config.TokenEvidence != TokenEvidenceDisabled {
		if err := writeJSONEvent(writer, flusher, promptTokenEvent()); err != nil {
			return
		}
	}

	contentChunks := h.config.ContentChunks
	if h.config.Mode == ModeNoContent {
		contentChunks = 0
	}
	for index := 0; index < contentChunks; index++ {
		if index > 0 && !wait(request.Context(), h.config.ChunkInterval) {
			return
		}
		if err := writeJSONEvent(writer, flusher, contentEvent(h.config, index, wantsTokenIDs)); err != nil {
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

func (h *handler) serveTokenize(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writer.Header().Set("Allow", http.MethodPost)
		writeAPIError(writer, http.StatusMethodNotAllowed, "method must be POST")
		return
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		writeAPIError(writer, http.StatusBadRequest, "Content-Type must be application/json")
		return
	}
	defer request.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes))
	var payload tokenizeRequest
	if err := decoder.Decode(&payload); err != nil {
		writeAPIError(writer, http.StatusBadRequest, "request body must contain valid JSON")
		return
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		writeAPIError(writer, http.StatusBadRequest, "request body must contain exactly one JSON value")
		return
	}
	if strings.TrimSpace(payload.Model) == "" || len(payload.Messages) != 1 || payload.Messages[0].Role != "user" || !payload.AddGenerationPrompt || payload.AddSpecialTokens || payload.ReturnTokenStrings {
		writeAPIError(writer, http.StatusBadRequest, "invalid tokenizer fixture request")
		return
	}
	// The fixture models eight rendered chat-template tokens plus one token per
	// UTF-8 content byte. It is intentionally a test contract, not a model tokenizer.
	count := 8 + len([]byte(payload.Messages[0].Content))
	tokens := make([]int, count)
	for index := range tokens {
		tokens[index] = index + 1
	}
	writer.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(writer).Encode(tokenizeResponse{Count: count, MaxModelLength: h.config.TokenizerMaxModelLength, Tokens: tokens})
}

func validateRequest(writer http.ResponseWriter, request *http.Request) (chatCompletionRequest, string) {
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return chatCompletionRequest{}, "Content-Type must be application/json"
	}
	defer request.Body.Close()
	decoder := json.NewDecoder(http.MaxBytesReader(writer, request.Body, maxRequestBodyBytes))
	var payload chatCompletionRequest
	if err := decoder.Decode(&payload); err != nil {
		var maximumBytesError *http.MaxBytesError
		if errors.As(err, &maximumBytesError) {
			return chatCompletionRequest{}, "request body exceeds 1 MiB"
		}
		return chatCompletionRequest{}, "request body must contain valid JSON"
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return chatCompletionRequest{}, "request body must contain exactly one JSON value"
	}
	if strings.TrimSpace(payload.Model) == "" {
		return chatCompletionRequest{}, "model is required"
	}
	if len(payload.Messages) == 0 {
		return chatCompletionRequest{}, "at least one message is required"
	}
	if !payload.Stream {
		return chatCompletionRequest{}, "stream must be true"
	}
	if !payload.StreamOptions.IncludeUsage {
		return chatCompletionRequest{}, "stream_options.include_usage must be true"
	}
	return payload, ""
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

const (
	promptTokenSentinel    = 987654300
	generatedTokenSentinel = 987654321
)

func promptTokenEvent() streamChunk {
	return streamChunk{
		Choices: []streamChoice{{
			Index: 0,
			Delta: map[string]string{"role": "assistant"},
		}},
		PromptTokenIDs: []int{promptTokenSentinel, promptTokenSentinel + 1},
	}
}

func contentEvent(config Config, index int, wantsTokenIDs bool) streamChunk {
	choice := streamChoice{
		Index: 0,
		Delta: map[string]string{"content": "x"},
	}
	if wantsTokenIDs {
		choice.TokenIDs = fixtureTokenIDs(config.TokenEvidence, index)
	}
	return streamChunk{Choices: []streamChoice{choice}}
}

func fixtureTokenIDs(mode TokenEvidenceMode, index int) *[]int {
	var values []int
	switch mode {
	case TokenEvidenceSingleton, TokenEvidenceMismatch:
		values = []int{generatedTokenSentinel + index}
	case TokenEvidenceBatched:
		switch index {
		case 0:
			values = []int{generatedTokenSentinel, generatedTokenSentinel + 1}
		case 1:
			values = []int{}
		default:
			values = []int{generatedTokenSentinel + index}
		}
	case TokenEvidenceMissing:
		if index == 0 {
			return nil
		}
		values = []int{generatedTokenSentinel + index}
	default:
		return nil
	}
	return &values
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
	completionTokens := config.CompletionTokens
	if config.TokenEvidence == TokenEvidenceMismatch {
		completionTokens = config.ContentChunks + 1
	}
	return streamChunk{
		Choices: []streamChoice{},
		Usage: &streamUsage{
			PromptTokens:     config.PromptTokens,
			CompletionTokens: completionTokens,
			TotalTokens:      config.PromptTokens + completionTokens,
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
