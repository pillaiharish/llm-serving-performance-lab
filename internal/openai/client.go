package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"
)

type Client struct {
	httpClient *http.Client
	endpoint   string
	apiKey     string
	now        func() time.Time
}

func NewClient(httpClient *http.Client, baseURL, apiKey string) (*Client, error) {
	if httpClient == nil {
		return nil, fmt.Errorf("HTTP client is required")
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse base URL: %w", err)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/") + "/chat/completions"
	parsed.RawPath = ""
	return &Client{
		httpClient: httpClient,
		endpoint:   parsed.String(),
		apiKey:     apiKey,
		now:        time.Now,
	}, nil
}

// Execute performs one streaming request. Payload construction is completed
// before RequestStartedAt so the measured interval begins immediately before
// the HTTP round trip.
func (c *Client) Execute(ctx context.Context, request benchmark.Request, observation *benchmark.RequestObservation) (returnErr error) {
	if observation == nil {
		return fmt.Errorf("request observation is required")
	}

	payload, err := json.Marshal(chatCompletionRequest{
		Model: request.Model,
		Messages: []message{{
			Role:    "user",
			Content: request.Prompt,
		}},
		MaxTokens:   request.MaxOutputTokens,
		Temperature: request.Temperature,
		Stream:      true,
		StreamOptions: streamOptions{
			IncludeUsage: true,
		},
	})
	if err != nil {
		return fmt.Errorf("encode chat completion request: %w", err)
	}

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create chat completion request: %w", err)
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Accept", "text/event-stream")
	if c.apiKey != "" {
		httpRequest.Header.Set("Authorization", "Bearer "+c.apiKey)
	}

	var timestampMu sync.Mutex
	trace := &httptrace.ClientTrace{
		GotFirstResponseByte: func() {
			timestampMu.Lock()
			defer timestampMu.Unlock()
			if observation.FirstByteAt == nil {
				observed := c.now()
				observation.FirstByteAt = &observed
			}
		},
	}
	httpRequest = httpRequest.WithContext(httptrace.WithClientTrace(httpRequest.Context(), trace))

	started := c.now()
	observation.RequestStartedAt = &started
	defer func() {
		completed := c.now()
		observation.CompletedAt = &completed
	}()

	response, err := c.httpClient.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("send chat completion request: %w", err)
	}
	headersReceived := c.now()
	observation.HeadersReceivedAt = &headersReceived
	observation.StatusCode = response.StatusCode

	counter := &countingReader{reader: response.Body}
	defer func() {
		if err := response.Body.Close(); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("close response body: %w", err)
		}
		observation.ResponseBodyBytes = counter.bytesRead
	}()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		if _, err := io.Copy(io.Discard, counter); err != nil {
			return fmt.Errorf("HTTP status %d; discard response body: %w", response.StatusCode, err)
		}
		return fmt.Errorf("unexpected HTTP status %d", response.StatusCode)
	}

	seenDone := false
	err = parseSSE(counter, func(data string) error {
		received := c.now()
		if seenDone {
			return fmt.Errorf("received SSE data after [DONE]")
		}

		if data == "[DONE]" {
			seenDone = true
			appendStreamEvent(observation, received, false, 0)
			return nil
		}

		var chunk streamChunk
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return fmt.Errorf("decode SSE JSON event %d: %w", len(observation.StreamEvents)+1, err)
		}

		contentBytes := 0
		hasContent := false
		for _, candidate := range chunk.Choices {
			if candidate.Index != 0 {
				continue
			}
			if candidate.Delta.Content != nil && *candidate.Delta.Content != "" {
				hasContent = true
				contentBytes = len([]byte(*candidate.Delta.Content))
			}
			if candidate.FinishReason != nil && *candidate.FinishReason != "" {
				observation.FinishReason = *candidate.FinishReason
			}
			break
		}
		if chunk.Usage != nil {
			if chunk.Usage.PromptTokens < 0 || chunk.Usage.CompletionTokens < 0 || chunk.Usage.TotalTokens < 0 {
				return fmt.Errorf("SSE usage event contains a negative token count")
			}
			observation.Usage = benchmark.TokenUsage{
				InputTokens:  chunk.Usage.PromptTokens,
				OutputTokens: chunk.Usage.CompletionTokens,
				TotalTokens:  chunk.Usage.TotalTokens,
				Source:       "server_usage",
				Available:    true,
			}
		}

		appendStreamEvent(observation, received, hasContent, contentBytes)
		return nil
	})
	if err != nil {
		return err
	}
	if !seenDone {
		return ErrUnexpectedEOF
	}
	return nil
}

func appendStreamEvent(observation *benchmark.RequestObservation, received time.Time, hasContent bool, contentBytes int) {
	if observation.FirstStreamEventAt == nil {
		first := received
		observation.FirstStreamEventAt = &first
	}
	if hasContent {
		if observation.FirstContentAt == nil {
			first := received
			observation.FirstContentAt = &first
		}
		last := received
		observation.LastContentAt = &last
	}
	observation.StreamEvents = append(observation.StreamEvents, benchmark.StreamEvent{
		Sequence:     len(observation.StreamEvents) + 1,
		ReceivedAt:   received,
		HasContent:   hasContent,
		ContentBytes: contentBytes,
	})
}

type countingReader struct {
	reader    io.Reader
	bytesRead int64
}

func (r *countingReader) Read(buffer []byte) (int, error) {
	count, err := r.reader.Read(buffer)
	r.bytesRead += int64(count)
	return count, err
}
