# LLM Serving Performance Lab

This repository contains **Slentore**, a Go benchmark harness for studying LLM
serving performance from raw client-side timing evidence.

Slentore implements a production-quality streaming OpenAI-compatible Chat
Completions request primitive and a fixed-concurrency, closed-loop coordinator
around it. One invocation attempts a configured number of requests at one
configured concurrency level. Request-rate scheduling, warmup, concurrency
sweeps, percentile aggregation, deployment, and observability remain outside
this version.

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

benchmark:
  concurrency: 1
  requests: 1
  safety:
    max_concurrency: 256
    max_requests: 10000
```

`base_url` is the OpenAI API root. Slentore removes a trailing slash and
appends `/chat/completions`, so an API root ending in `/v1` produces a request
to `/v1/chat/completions`.

Configuration precedence is:

```text
built-in defaults < YAML < explicitly supplied CLI flags
```

The built-in defaults are 64 maximum output tokens, temperature 0, a 120
second per-request timeout, the `runs` output directory, concurrency 1, one
request, maximum concurrency 256, and maximum requests 10,000. Base URL,
model, and prompt must be supplied by YAML and/or flags.

Requested concurrency and request count must be positive and must not exceed
their configured safety ceilings. Slentore rejects an over-limit run; it never
silently clamps it. `--max-concurrency` and `--max-requests` can explicitly
raise the ceilings when a larger run is intentional. Admission records the
client's CPU count, `GOMAXPROCS`, Go version, operating system, and architecture
as diagnostics, but does not infer a capacity limit from them.

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

## Run a benchmark

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
  --api-key-env "" \
  --concurrency 4 \
  --requests 16 \
  --max-concurrency 256 \
  --max-requests 10000
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
the terminal or accumulated in memory. With the default `concurrency: 1` and
`requests: 1`, behavior remains compatible with the original single-request
invocation and its detailed scalar terminal summary.

## Closed-loop concurrency

For `N` requested calls at concurrency `C`, Slentore starts exactly
`min(C, N)` long-lived workers. Each worker claims the next request ID, executes
it to completion or timeout, and immediately claims another while work remains.
There is no pacing or target QPS: offered load is closed-loop and depends on
request completion. IDs are assigned as `req-000001` through `req-N`; the
completion-order result list is independent of ID order.

Every claimed request gets its own child context with the configured timeout.
A request failure is preserved in that request's observation and does not stop
later claims. SIGINT or SIGTERM stops new claims, cancels active requests,
waits for workers and their results to drain, then writes the collected subset.
There is no separate run-wide timeout.

One `http.Client` and one cloned standard transport are shared by all workers
for the run. The transport's per-host connection and idle-connection limits
are set to the effective worker count, retaining the standard dial and TLS
behavior. Idle connections are closed after the run. Workers perform no file
I/O and never send SSE or token events through coordinator channels; the
coordinator collects one completed result per attempted request in memory.
The default 10,000-request ceiling bounds that collection unless explicitly
raised.

The terminal summary always reports requested, attempted, completed,
successful, and failed counts, requested concurrency, effective workers,
maximum observed active calls, elapsed time, and the artifact path. Detailed
per-request scalar output is retained only when `N=1`.

## Deterministic local fake server

`slentore-fake-server` is a loopback-only-by-default OpenAI-compatible SSE
fixture server. It provides controlled HTTP and streaming delays so Slentore's
per-request measurement, failure handling, and artifacts can be validated
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
endpoint. This example exercises four overlapping requests while leaving the
fixture's per-request protocol unchanged:

```bash
build/slentore bench \
  --base-url http://127.0.0.1:18080/v1 \
  --model fake-model \
  --prompt "benchmark fixture" \
  --max-output-tokens 4 \
  --temperature 0 \
  --timeout 5s \
  --output-dir runs \
  --api-key-env "" \
  --concurrency 4 \
  --requests 16
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
failure. Each request's timeout bounds that entire request through
`context.Context`.

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
    └── requests/
        ├── req-000001/
        │   ├── observation.json
        │   └── metrics.json
        └── req-000002/
            ├── observation.json
            └── metrics.json
```

- `run.json` is artifact schema version 2. It contains the run ID, Slentore
  version, model, safe base URL, requested output limit, temperature,
  per-request timeout, prompt byte length and SHA-256, requested/attempted/
  completed/successful/failed counts, concurrency and worker evidence, safety
  ceilings, run wall times and monotonic-derived elapsed nanoseconds, run
  status/error, and portable client diagnostics. It has no singular request
  ID.
- `observation.json` contains `(run_id, request_id)`, wall timestamps,
  request-relative nanosecond offsets, event evidence, server usage when
  supplied, HTTP status, finish reason, byte counts, and error state.
- `metrics.json` contains `(run_id, request_id)` and values derived from that
  observation.

The writer sorts a copy of completed results by request sequence, validates
run/request identities and duplicates, stages the complete tree, and atomically
renames it into place only after every file has been written. Metric derivation
and filesystem work happen after the coordinator's measured interval.
Historical committed schema-1 validation artifacts remain unchanged.

Artifacts deliberately exclude the raw prompt, request body, generated text,
raw SSE JSON, response body, and API credentials.

Configuration, URL, secret, admission, and client-construction errors occur
before measurement, create no run directory, and exit with status 2. Once
measurement starts, DNS/connect errors, per-request timeouts, non-2xx
responses, malformed streams, cancellation, and a clean stream with no
generated content preserve partial observation and metric artifacts before
exiting with status 1. Except for parent cancellation, measured request
failures do not stop remaining IDs from being attempted. Artifact failures and
coordinator failures also exit 1. Missing usage alone is not a request failure;
usage-dependent metrics are simply unavailable. Exit status 0 requires all
`N` requests to succeed and the complete artifact tree to be written.

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

This version runs one fixed concurrency level with one finite request count. It
contains no QPS scheduler, warmup lifecycle, concurrency sweep, run-level
percentile or throughput aggregation, tokenizer workload generator, database,
GPU discovery, deployment automation, or observability integration. The
coordinator reuses the existing `Runner.RunRequest` primitive without changing
how an individual request is observed or how its metrics are calculated. The
fake server provides controlled HTTP/SSE validation; it does not simulate GPU,
model, tokenizer, or vLLM capacity behavior.
