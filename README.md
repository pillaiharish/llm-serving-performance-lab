# LLM Serving Performance Lab

This repository contains **Slentore**, a Go benchmark harness for studying LLM
serving performance from raw client-side timing evidence.

Slentore currently implements one production-quality primitive: one streaming
OpenAI-compatible Chat Completions request. Concurrency, request-rate
scheduling, warmup, run-level aggregation, deployment, and observability are
intentionally outside this version.

The original local single-GPU learning series remains unchanged under
[`experiments/local-single-gpu-v0/`](experiments/local-single-gpu-v0/).

## Build

Slentore requires Go 1.22 or newer.

```bash
go build -o build/slentore ./cmd/slentore
```

The only non-standard-library dependency is `go.yaml.in/yaml/v3`, used for
strict YAML configuration parsing.

## Configure

Start from [`configs/ollama.example.yaml`](configs/ollama.example.yaml):

```yaml
version: 1

endpoint:
  base_url: "https://ollama.com/v1"
  api_key_env: "OLLAMA_API_KEY"
  model: "<model-name>"

request:
  prompt: "Explain KV cache briefly."
  max_output_tokens: 64
  temperature: 0

runtime:
  timeout: "120s"

capture:
  output_dir: "runs"
```

`base_url` is the OpenAI API root. Slentore removes a trailing slash and
appends `/chat/completions`, so an API root ending in `/v1` produces a request
to `/v1/chat/completions`.

Configuration precedence is:

```text
built-in defaults < YAML < explicitly supplied CLI flags
```

The built-in defaults are 64 maximum output tokens, temperature 0, a 120
second timeout, and the `runs` output directory. Base URL, model, and prompt
must be supplied by YAML and/or flags.

Unknown YAML fields, multiple YAML documents, unsupported versions, invalid
durations, unsafe URLs, and missing required values are rejected before
measurement begins.

## Authentication

Slentore never accepts an API key value as a CLI option. Name the environment
variable containing the key instead:

```bash
export OLLAMA_API_KEY='...'
build/slentore bench \
  --config configs/ollama.example.yaml \
  --api-key-env OLLAMA_API_KEY
```

If `api_key_env` is omitted or explicitly cleared with `--api-key-env ""`, no
Authorization header is sent. When it is configured, the environment variable
must exist and be non-empty. The key is never printed or persisted.

URLs containing userinfo, query strings, or fragments are rejected so a secret
cannot accidentally be embedded in persisted endpoint metadata.

## Run one benchmark request

```bash
build/slentore bench --config configs/ollama.example.yaml
```

Every YAML setting can be overridden for a single invocation:

```bash
build/slentore bench \
  --config configs/ollama.example.yaml \
  --base-url http://127.0.0.1:8000/v1 \
  --model models/Qwen/Qwen2.5-0.5B-Instruct \
  --prompt "Explain KV cache briefly." \
  --max-output-tokens 64 \
  --temperature 0 \
  --timeout 120s \
  --output-dir runs \
  --api-key-env ""
```

Slentore sends:

```json
{
  "model": "...",
  "messages": [{"role": "user", "content": "..."}],
  "max_tokens": 64,
  "temperature": 0,
  "stream": true,
  "stream_options": {"include_usage": true}
}
```

Generated content is parsed for byte counts and timing but is not streamed to
the terminal or accumulated in memory.

## Timing evidence

Configuration, secret lookup, payload marshaling, and HTTP request construction
happen before the measured interval wherever practical. The following raw
timestamps are retained:

- `request_started_at`: immediately before `http.Client.Do`.
- `headers_received_at`: immediately after `Do` returns parsed response
  headers.
- `first_byte_at`: Go `httptrace.GotFirstResponseByte`, which observes the
  first byte of the HTTP response headers. If the transport cannot provide it,
  the value is `null` and TTFB is unavailable.
- `first_stream_event_at`: the first successfully decoded OpenAI SSE data
  event, including a usage or `[DONE]` event.
- `first_content_at`: the first event containing a non-empty choice-zero
  content delta.
- `last_content_at`: the last event containing a non-empty choice-zero content
  delta.
- `completed_at`: after the response body reaches EOF or failure cleanup has
  completed from the client's perspective.

Go's monotonic clock component is retained in memory for all duration
calculations. JSON artifacts contain the corresponding RFC3339 wall-clock
timestamps because monotonic readings are intentionally process-local.

Every valid `data:` event has a sequence number, receipt timestamp, content
flag, and UTF-8 content-byte count. Usage-only, role-only, empty-delta, finish,
and `[DONE]` events are retained as zero-content events. Raw event JSON and
generated strings are not retained.

The client continues reading through EOF after `[DONE]`. EOF without `[DONE]`,
malformed JSON, data after `[DONE]`, or an oversized SSE frame is a stream
failure. The timeout bounds the entire request through `context.Context`.

## Metric definitions

Metrics are derived only after the measured request completes. Every scalar is
stored as `{available, value, unit, reason}`; unavailable observations never
become estimated values, NaN, or infinity.

```text
time_to_headers = headers_received_at - request_started_at
TTFB            = first_byte_at - request_started_at
TTFT            = first_content_at - request_started_at
TTLT            = last_content_at - request_started_at
E2E             = completed_at - request_started_at

TPOT = (last_content_at - first_content_at) / (output_tokens - 1)

output_tokens_per_second = output_tokens / E2E_seconds

decode_tokens_per_second =
  (output_tokens - 1) /
  (last_content_at - first_content_at).Seconds()
```

TPOT and decode token rate require server usage, more than one output token,
at least two content events, and a positive first-to-last content window.
Output token rate requires server usage and a positive E2E duration. Slentore
does not estimate missing token usage.

`inter_chunk_latency` contains every gap between consecutive content-bearing
SSE events plus count, mean, minimum, and maximum. Fewer than two content events
makes it unavailable.

### Inter-chunk latency is not ITL

An OpenAI-compatible SSE event is not guaranteed to contain exactly one
tokenizer token. Slentore therefore does not label chunk gaps as inter-token
latency. True ITL is represented explicitly as:

```json
{
  "available": false,
  "source": "not_available",
  "reason": "OpenAI-compatible SSE chunks are not guaranteed to map 1:1 to tokenizer tokens"
}
```

`response_bytes` is the sum of UTF-8 bytes in non-empty generated-content
deltas. `response_body_bytes` is the number of HTTP body bytes read, including
SSE framing, usage events, and `[DONE]`.

## Artifacts and failure behavior

After measurement, Slentore creates:

```text
runs/
└── 20260816T120501Z-a31f00ff/
    ├── run.json
    ├── observation.json
    └── metrics.json
```

- `run.json` contains IDs, Slentore version, model, safe base URL, requested
  output limit, temperature, timeout, prompt byte length, and prompt SHA-256.
- `observation.json` contains timestamps, event evidence, server usage when
  supplied, HTTP status, finish reason, byte counts, and error state.
- `metrics.json` contains values derived from that observation.

Artifacts deliberately exclude the raw prompt, request body, generated text,
raw SSE JSON, response body, and API credentials.

Configuration and secret errors occur before measurement, create no run
directory, and exit with status 2. Once measurement starts, DNS/connect errors,
timeouts, non-2xx responses, malformed streams, cancellation, and a clean
stream with no generated content preserve partial observation and metric
artifacts before exiting with status 1. Missing usage alone is not a request
failure; usage-dependent metrics are simply unavailable. A valid
content-bearing stream exits with status 0.

## Validate

The test suite uses artificial timestamps, SSE fixtures, immediate local HTTP
servers, and custom transports. It does not require endpoint credentials or
network access.

```bash
gofmt -l .
go test ./...
go vet ./...
go build ./cmd/slentore
```

## Current scope

This version performs exactly one request. It contains no concurrency option,
worker pool, jobs/results channel, QPS scheduler, warmup lifecycle, percentile
aggregation, tokenizer workload generator, database, GPU discovery, deployment
automation, or observability integration. Future orchestration can reuse one
shared `http.Client` and call the same `Runner.RunRequest` primitive without
changing how a request is observed or how its metrics are calculated.
