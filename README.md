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
go build -o build/slentore-fake-server ./cmd/slentore-fake-server
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

## Deterministic local fake server

`slentore-fake-server` is a loopback-only-by-default OpenAI-compatible SSE
fixture server. It provides controlled HTTP and streaming delays so Slentore's
one-request measurement, failure handling, and artifacts can be validated
without a GPU, model, credential, cloud endpoint, or network access.

Start the default normal profile:

```bash
build/slentore-fake-server \
  --listen 127.0.0.1:18080 \
  --mode normal \
  --header-delay 50ms \
  --first-content-delay 100ms \
  --chunk-interval 20ms \
  --content-chunks 4 \
  --usage-delay 10ms \
  --done-delay 10ms \
  --prompt-tokens 16 \
  --completion-tokens 4
```

In another terminal, benchmark it exactly like any other OpenAI-compatible
endpoint:

```bash
build/slentore bench \
  --base-url http://127.0.0.1:18080/v1 \
  --model fake-model \
  --prompt "benchmark fixture" \
  --max-output-tokens 4 \
  --temperature 0 \
  --timeout 5s \
  --output-dir runs \
  --api-key-env ""
```

The normal timeline is:

```text
request accepted
  └─ header_delay (50ms) ─ flush response headers
       └─ first_content_delay (100ms) ─ content #1
            ├─ chunk_interval (20ms) ─ content #2
            ├─ chunk_interval (20ms) ─ content #3
            └─ chunk_interval (20ms) ─ content #4
                 └─ finish event
                      └─ usage_delay (10ms) ─ usage event
                           └─ done_delay (10ms) ─ [DONE] and EOF
```

`header_delay` begins after request validation and ends when headers are
explicitly flushed. `first_content_delay` starts after that flush.
`chunk_interval` applies only between consecutive content-bearing events.
`usage_delay` starts after the finish event, and `done_delay` starts after the
usage event. Every SSE event is flushed independently. With four content
events and four reported completion tokens, the controlled decode window is
about 60ms, so TPOT and the three inter-chunk gaps should be about 20ms.

Available modes are:

- `normal`: content, finish, usage, and `[DONE]`.
- `no-content`: finish, usage, and `[DONE]` without generated content.
- `http-error`: a fixed `503 Service Unavailable` response.
- `malformed-json`: valid SSE framing containing invalid JSON.
- `eof-before-done`: content, finish, and usage followed by EOF without
  `[DONE]`.
- `data-after-done`: a normal stream followed by an additional data event.

All delays may be zero. Sleeps stop when the request is canceled, and SIGINT
or SIGTERM initiates graceful HTTP shutdown. The server validates the streaming
request shape but never requires authentication and never logs or persists a
prompt, request body, Authorization header, or generated content.

Real timers and OS scheduling make observed durations approximate: tests check
ordering, lower bounds, and protocol flushing rather than nanosecond equality.
The server is a measurement-system fixture, not an LLM performance simulator.
It does not emulate model tokenization, GPU execution, prefill/decode kernels,
KV cache, continuous batching, or vLLM scheduling.

## Real-cloud validation

The first live GPU-backed validation used vLLM 0.26.0, Qwen/Qwen3.5-4B, and
one NVIDIA RTX A5000 on Vast.ai. See the
[deployment and reproduction guide](docs/deployments/vast-ai-a5000-vllm.md)
and the [Slentore smoke report](reports/vast_a5000_slentore_smoke.md) for the
sanitized remote-client and server-local C=1 evidence.

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

Each optional wall timestamp is paired with a `*_after_ns` field containing
the nanosecond offset from `request_started_at`, calculated from the same
`time.Time` sample. Wall timestamps support external correlation. The offsets
preserve monotonic-derived elapsed evidence because JSON cannot retain Go's
process-local monotonic clock component. Metric recomputation prefers offsets
and falls back to wall-time subtraction only for older observations where the
relevant optional offsets are absent.

Every valid `data:` event has a sequence number, receipt timestamp, canonical
non-optional `received_after_ns` offset, content flag, and UTF-8 content-byte
count. Usage-only, role-only, empty-delta, finish, and `[DONE]` events are
retained as zero-content events. Raw event JSON and generated strings are not
retained.

The client continues reading through EOF after `[DONE]`. EOF without `[DONE]`,
malformed JSON, data after `[DONE]`, or an oversized SSE frame is a stream
failure. The timeout bounds the entire request through `context.Context`.

## Metric definitions

Metrics are derived only after the measured request completes. Every scalar is
stored as `{available, value, unit, reason}`; unavailable observations never
become estimated values, NaN, or infinity. The formulas below use timestamp
names for readability; persisted recomputation uses their request-relative
nanosecond offsets when present.

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
- `observation.json` contains `(run_id, request_id)`, wall timestamps,
  request-relative nanosecond offsets, event evidence, server usage when
  supplied, HTTP status, finish reason, byte counts, and error state.
- `metrics.json` contains `(run_id, request_id)` and values derived from that
  observation.

All three files remain artifact schema version 1; the observation and metrics
can be associated by `(run_id, request_id)` without relying on their directory.

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
gofmt -l cmd internal
go test ./...
go vet ./...
go build ./cmd/slentore
go build ./cmd/slentore-fake-server
go test -race ./...
```

## Current scope

This version performs exactly one request. It contains no concurrency option,
worker pool, jobs/results channel, QPS scheduler, warmup lifecycle, percentile
aggregation, tokenizer workload generator, database, GPU discovery, deployment
automation, or observability integration. Future orchestration can reuse one
shared `http.Client` and call the same `Runner.RunRequest` primitive without
changing how a request is observed or how its metrics are calculated. The fake
server validates this primitive but does not add orchestration to it.
