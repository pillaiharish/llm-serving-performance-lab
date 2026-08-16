package metrics

import "github.com/pillaiharish/llm-serving-performance-lab/internal/benchmark"

const TrueITLReason = "OpenAI-compatible SSE chunks are not guaranteed to map 1:1 to tokenizer tokens"

type Scalar struct {
	Available bool    `json:"available"`
	Value     float64 `json:"value"`
	Unit      string  `json:"unit"`
	Reason    string  `json:"reason,omitempty"`
}

type InterChunkLatency struct {
	Available bool      `json:"available"`
	Count     int       `json:"count"`
	MeanMS    float64   `json:"mean_ms"`
	MinMS     float64   `json:"min_ms"`
	MaxMS     float64   `json:"max_ms"`
	ValuesMS  []float64 `json:"values_ms"`
	Reason    string    `json:"reason,omitempty"`
}

type ITLAvailability struct {
	Available bool   `json:"available"`
	Source    string `json:"source"`
	Reason    string `json:"reason"`
}

type RequestMetrics struct {
	RunID                 string               `json:"run_id"`
	RequestID             string               `json:"request_id"`
	TimeToHeaders         Scalar               `json:"time_to_headers"`
	TTFB                  Scalar               `json:"ttfb"`
	TTFT                  Scalar               `json:"ttft"`
	TTLT                  Scalar               `json:"ttlt"`
	E2E                   Scalar               `json:"e2e"`
	TPOT                  Scalar               `json:"tpot"`
	OutputTokensPerSecond Scalar               `json:"output_tokens_per_second"`
	DecodeTokensPerSecond Scalar               `json:"decode_tokens_per_second"`
	InterChunkLatency     InterChunkLatency    `json:"inter_chunk_latency"`
	ITL                   ITLAvailability      `json:"itl"`
	TokenUsage            benchmark.TokenUsage `json:"token_usage"`
	StreamEventCount      int                  `json:"stream_event_count"`
	ContentEventCount     int                  `json:"content_event_count"`
	ResponseBytes         int                  `json:"response_bytes"`
	ResponseBodyBytes     int64                `json:"response_body_bytes"`
}
