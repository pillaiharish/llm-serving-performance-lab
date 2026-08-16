package benchmark

import "time"

const TokenUsageSourceUnavailable = "not_available"

// Request contains the transient workload needed to issue one request. The
// prompt is intentionally absent from all persisted result types.
type Request struct {
	RequestID       string
	Model           string
	Prompt          string
	MaxOutputTokens int
	Temperature     float64
}

// RequestObservation is the raw client-side evidence captured for one
// request. Optional timestamps are pointers so absence is explicit in JSON.
type RequestObservation struct {
	RequestID          string        `json:"request_id"`
	RequestStartedAt   *time.Time    `json:"request_started_at"`
	HeadersReceivedAt  *time.Time    `json:"headers_received_at"`
	FirstByteAt        *time.Time    `json:"first_byte_at"`
	FirstStreamEventAt *time.Time    `json:"first_stream_event_at"`
	FirstContentAt     *time.Time    `json:"first_content_at"`
	LastContentAt      *time.Time    `json:"last_content_at"`
	CompletedAt        *time.Time    `json:"completed_at"`
	StreamEvents       []StreamEvent `json:"stream_events"`
	Usage              TokenUsage    `json:"usage"`
	ResponseBodyBytes  int64         `json:"response_body_bytes"`
	StatusCode         int           `json:"status_code"`
	FinishReason       string        `json:"finish_reason,omitempty"`
	Error              string        `json:"error,omitempty"`
}

type StreamEvent struct {
	Sequence     int       `json:"sequence"`
	ReceivedAt   time.Time `json:"received_at"`
	HasContent   bool      `json:"has_content"`
	ContentBytes int       `json:"content_bytes"`
}

type TokenUsage struct {
	InputTokens  int    `json:"input_tokens"`
	OutputTokens int    `json:"output_tokens"`
	TotalTokens  int    `json:"total_tokens"`
	Source       string `json:"source"`
	Available    bool   `json:"available"`
}

// Result couples raw evidence with the runtime error returned to callers. Err
// is never persisted by the artifacts package; Observation.Error is the safe,
// serializable error representation.
type Result struct {
	Observation RequestObservation
	Err         error
}
