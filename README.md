# LLM Serving Performance Lab

This repository contains **Slentore**, a Go benchmark harness for studying LLM
serving performance from raw client-side timing evidence.

Slentore implements a production-quality streaming OpenAI-compatible Chat
Completions request primitive with conservative generic inter-chunk timing and
opt-in, evidence-backed vLLM token-arrival timing. It provides two explicit load
models: fixed-concurrency closed-loop work and wall-clock open-loop request-rate
scheduling. One invocation runs an optional request-count warmup followed by one measured
cohort and writes exact run-level latency, throughput, delivery, token, and
optional SLO/goodput summaries. Duration-based warmup, load sweeps, deployment,
and observability remain outside this version.

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
  drain_timeout: "120s"

capture:
  output_dir: "runs"

benchmark:
  mode: closed_loop
  concurrency: 1
  requests: 1
  warmup_requests: 0
  token_timing:
    mode: disabled
  slo:
    ttft: null
    tpot: null
    e2e: null
  safety:
    max_concurrency: 256
    max_requests: 10000
    max_request_rate: 10000
    max_in_flight: 256
    max_input_tokens: 131072
    max_output_tokens: 32768
```

`base_url` is the OpenAI API root. Slentore removes a trailing slash and
appends `/chat/completions`, so an API root ending in `/v1` produces a request
to `/v1/chat/completions`.

Configuration precedence is:

```text
built-in defaults < YAML < explicitly supplied CLI flags
```

The built-in defaults are 64 maximum output tokens, temperature 0, a 120
second per-request timeout, a separate 120 second drain timeout, the `runs`
output directory, closed-loop mode, concurrency 1, zero warmup requests, one
measured request, maximum concurrency 256, maximum requests 10,000, maximum
request rate 10,000/s, maximum in-flight 256, disabled token timing, maximum
target input tokens 131,072, and maximum requested output tokens 32,768. Base
URL and model must be supplied by YAML and/or flags; direct-prompt mode also
requires a prompt. No SLO is configured by default.

Requested concurrency and measured request count must be positive; warmup may
be zero. Warmup and measured counts are each checked independently against
`max_requests`, and concurrency is checked against `max_concurrency`. Slentore
rejects an over-limit run and never silently clamps it. Explicitly raising
`--max-concurrency` or `--max-requests` permits a larger intentional run.
Open-loop rate and in-flight admission are likewise checked against
`max_request_rate` and the safety `max_in_flight`; their CLI ceiling overrides
are `--max-request-rate-ceiling` and `--max-in-flight-ceiling`.
Admission records the client's CPU count, `GOMAXPROCS`, Go version, operating
system, and architecture as diagnostics, but does not infer a capacity limit
from them.

Unknown YAML fields, multiple YAML documents, unsupported versions, invalid
durations, unsafe URLs, and missing required values are rejected before
measurement begins. Explicit SLO thresholds use Go duration syntax and must be
greater than zero.

Input and output token ceilings are Slentore client guardrails, not claims
about a model's context window. Token-length mode additionally checks the
server-reported `max_model_len` before starting a lifecycle.

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
  --drain-timeout 120s \
  --output-dir runs \
  --api-key-env "" \
  --token-timing disabled \
  --mode closed-loop \
  --concurrency 4 \
  --warmup-requests 4 \
  --requests 16 \
  --slo-ttft 800ms \
  --slo-tpot 30ms \
  --slo-e2e 5s \
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

With the default `token_timing.mode: disabled`, the request is semantically the
generic payload above: the vLLM extension is omitted entirely, rather than sent
as `return_token_ids: false`. With `--token-timing vllm`, the same streaming
request additionally contains `"return_token_ids": true`.

Generated content is parsed for byte counts and timing but is not streamed to
the terminal or accumulated in memory. With the default zero warmup,
`concurrency: 1`, and `requests: 1`, behavior remains compatible with the
original single-request invocation and its detailed scalar terminal summary.

## Workload modes

### Direct prompt workload

`prompt` is the default workload mode. The user supplies `request.prompt`, and
Slentore sends it without configuring or contacting a tokenizer. This retains
the earlier CLI and YAML behavior. Prompt bytes and SHA-256 are persisted, but
prompt text is not.

### Token-length workload

Token-length mode builds one deterministic prompt and verifies its rendered
chat-input shape before benchmark timing:

```yaml
workload:
  mode: token_length
  input_tokens: 128
  tokenizer:
    adapter: vllm
    url: "http://127.0.0.1:18000/tokenize"

request:
  max_output_tokens: 32
  temperature: 0
```

See [`configs/token-length.example.yaml`](configs/token-length.example.yaml).
The equivalent CLI flags are `--workload-mode token-length`,
`--input-tokens`, `--tokenizer-adapter vllm`, and `--tokenizer-url`.
`--max-output-tokens` remains the one output-limit setting in both modes.
Slentore never infers a workload mode from these flags, and an explicitly
supplied raw prompt conflicts with token-length mode.

The vLLM adapter sends the same single-user chat shape as the measured request
to the configured `/tokenize` endpoint with `add_generation_prompt=true` and
no custom chat template or template arguments. Thus `input_tokens` means the
complete `rendered_chat_input`: user content plus roles, template markers,
special tokens, and the generation suffix selected by that server. It does
not mean raw content tokens. Token counts are valid only under the selected
tokenizer adapter contract and are not portable across models or endpoints.

Workload construction uses a versioned deterministic fixture, verifies the
final count exactly, and fails preflight if the exact target cannot be
constructed. The tokenizer is initialized once; one prepared prompt is reused
for every warmup and measured request in closed- and open-loop modes. No
tokenization occurs in workers or request goroutines.

`requested_max_tokens` is a generation maximum, not a promise that the model
will produce that many tokens. Server usage remains authoritative for actual
input and output token counts. Slentore persists local resolved input and
server usage separately, does not rewrite mismatches, and does not fail merely
because EOS produced fewer output tokens.

The vLLM adapter adds no Go dependency and requires no CGO, Rust, Python, GPU,
Hugging Face authentication, or model download. It does require the serving
endpoint during preflight. Identity evidence includes the model, adapter and
contract versions, source URL, server `max_model_len`, and a behavioral hash
of fixed safe tokenizer probes. Because `/tokenizer_info` is optional in vLLM,
this is not a vocabulary or model-revision hash; changing the endpoint may
change counts even when its URL and model name remain the same.

## Output token timing evidence

Output token timing is configured independently from the input workload:

```yaml
benchmark:
  token_timing:
    mode: vllm
```

The CLI equivalent is `--token-timing vllm`. Both direct-prompt and
token-length workloads support either `disabled` or `vllm`; Slentore never
infers output token timing from `workload.mode`, the input tokenizer adapter,
the model name, or endpoint URL. The `/tokenize` adapter controls input shape,
while this mode requests output evidence from the streaming Chat Completion
response.

In vLLM mode Slentore requests `return_token_ids=true`. vLLM returns prompt IDs
separately as top-level `prompt_token_ids` and generated delta IDs as
choice-zero `token_ids`. Slentore ignores prompt IDs, validates generated IDs,
immediately reduces them to safe presence/cardinality evidence, and never
prints or persists the numeric IDs.

An SSE event is not automatically a token event. True ITL is available only
when the request completes through `[DONE]`, server usage proves full output
coverage, and every token-bearing event contains exactly one generated ID. If
an event contains multiple generated IDs, their intra-event arrival times are
not observable; Slentore marks ITL unavailable rather than dividing or
interpolating the event gap. The successful request, ICL, and other supported
metrics remain usable. This path uses ordinary HTTP/SSE Chat Completions and
does not require gRPC.

## Benchmark lifecycle and load models

Each admitted invocation follows one centralized lifecycle:

```text
SETUP → WARMUP → MEASUREMENT → STOP ADMISSION → DRAIN → ARTIFACTS
```

Setup covers lifecycle initialization after configuration, admission, secret,
workload preparation, run-ID, shared-client, and OpenAI-client preflight.
These preflight operations are outside lifecycle timing; a preflight failure
creates no artifacts.

Warmup always has an explicit phase. With `warmup_requests: 0` it is recorded
as skipped. Otherwise, warmup uses the same request payload, endpoint,
per-request timeout, requested concurrency, shared HTTP client and transport,
stream parser, and `Runner.RunRequest` path as measurement. Its IDs are
`warmup-000001` through `warmup-N`. Every admitted warmup result drains before
measurement begins. Warmup failures are retained and make the overall run
fail, but do not suppress measurement unless the parent context is canceled.

Slentore samples a fresh measurement start immediately before measured workers
start, so measurement elapsed time excludes setup and warmup. Measured IDs
remain `req-000001` through `req-N`.

For `N` requested calls at concurrency `C`, Slentore starts exactly
`min(C, N)` long-lived workers. Each worker claims the next request ID, executes
it to completion or timeout, and immediately claims another while work remains.
There is no pacing or target QPS: offered load is closed-loop and depends on
request completion. IDs are assigned as `req-000001` through `req-N`; the
completion-order result list is independent of ID order.

Every claimed request gets its own child context with `runtime.timeout`. A
request failure is preserved in that request's observation and does not stop
later claims. The successful claim of measured request N is the exact stop-
admission boundary. No later request can be claimed; Slentore records the
boundary and starts the independent `runtime.drain_timeout` while active calls
finish normally.

If drain expires, outstanding request contexts are canceled with a distinct
drain-timeout cause, workers and result channels are fully drained, and partial
evidence is written. A parent cancellation during warmup skips measurement; a
parent cancellation during measurement stops new claims; during drain it
cancels active work immediately. Per-request timeout, parent cancellation, and
drain timeout remain distinct request outcomes. There is no run-wide timeout.

The phase worker counts and transport limit are:

```text
warmup_workers   = warmup_requests == 0 ? 0 : min(C, warmup_requests)
measured_workers = min(C, requests)
transport_limit  = max(warmup_workers, measured_workers)
```

One `http.Client` and one cloned standard transport are shared across both
phases. The transport's per-host connection and idle-connection limits use the
transport limit above while retaining standard dial and TLS behavior. Idle
connections are closed after the run. Workers perform no file I/O and never
send SSE or token events through coordinator channels; the coordinator
collects one completed result per attempted request in memory. The default
10,000-request ceiling bounds each cohort unless explicitly raised.

### Open-loop request rate

Open-loop mode offers arrivals from a wall-clock schedule independent of
request completion:

```yaml
benchmark:
  mode: open_loop
  warmup_requests: 4
  open_loop:
    request_rate: 20
    duration: "30s"
    max_in_flight: 256
  safety:
    max_concurrency: 256
    max_requests: 10000
    max_request_rate: 10000
    max_in_flight: 256
    max_input_tokens: 131072
    max_output_tokens: 32768
```

The CLI spelling is `--mode open-loop`, with `--request-rate`, `--duration`,
and `--max-in-flight`. Closed-loop flags are rejected in open-loop mode and
open-loop flags are rejected in closed-loop mode; Slentore never infers a mode
from an otherwise ambiguous combination.

For one-based arrival `i`, rate `R`, and measurement epoch `T0`, the target is
`T0 + (i-1)/R`. Every target is derived from `T0`, so timer wake-up delays do
not accumulate into the next target. Targets strictly before `T0 + duration`
are planned, giving `ceil(R × duration_seconds)` arrivals. The first target is
at `T0`; fractional rates use nanosecond offsets rounded down from the direct
formula.

At a target, Slentore performs a non-blocking in-flight admission check. An
admitted arrival starts one request goroutine. If the configured
`max_in_flight` guard is full, the arrival is `client_limited` and is never
queued or started later. If the local scheduler cannot process a planned
arrival before the measurement deadline, it is `scheduler_limited` and is not
burst-started after the window. `max_in_flight` is a client protection bound,
not target concurrency.

Started arrivals retain their scheduled wall time, phase-relative offset,
actual `request_started_at`, and scheduler lag. Scheduler lag is the actual
request start minus the scheduled target, calculated while Go's monotonic time
evidence is available. Dropped arrivals have null request/start/lag fields and
no request artifact; resulting request-ID gaps preserve offered-arrival
identity.

Open-loop warmup offers exactly `warmup_requests` at the same request rate and
with the same in-flight guard. It drains completely before a fresh measurement
epoch. Measurement stops admission exactly at `T0 + duration`; already
admitted requests then drain normally. A normal measurement containing either
limited disposition writes its evidence and exits non-zero with
`load_delivery_error`.

Little's Law suggests that expected in-flight work is approximately arrival
rate multiplied by average request duration. This is an analytical
relationship, not a safety guarantee; choose
and explicitly admit a suitable `max_in_flight` ceiling.

The terminal summary keeps run status and load delivery visible alongside
measured success/failure counts, p50/p95/p99 TTFT, TPOT, E2E and true ITL,
request and output-token throughput, and optional SLO goodput. Open-loop runs
also show offered/start rates, delivery and limiting counts, and scheduler lag.
Detailed scalar output is retained only for one successfully collected
measured request.

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

Tests and local token-length smoke runs may add `--tokenizer-fixture`, which
enables a tiny `/tokenize` contract with eight fixed rendered tokens plus one
token per UTF-8 content byte. It is disabled by default and is not a Qwen or
production tokenizer simulation.

Token-evidence tests may independently add `--token-evidence singleton`,
`batched`, `missing`, or `mismatch`. The default is `disabled`, and fixtures
emit IDs only when the request actually contains `return_token_ids=true`.
These deterministic sentinel IDs exercise evidence validation and redaction;
they do not make the fake server a vLLM simulator.

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
Its optional byte-token endpoint exists only to exercise Slentore preflight;
it does not emulate model tokenization, GPU execution, prefill/decode kernels,
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
non-optional `received_after_ns` offset, content flag, UTF-8 content-byte count,
safe token-ID-field presence, and generated-token cardinality. Usage-only,
role-only, empty-delta, finish, and `[DONE]` events are retained as zero-content
events. Content-bearing and token-bearing event subsets are independent: a
special generated token may have no visible content. Raw event JSON, generated
strings, prompt token IDs, and generated token IDs are not retained.

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

### Inter-chunk latency and true ITL

An OpenAI-compatible SSE event is not guaranteed to contain exactly one
tokenizer token. Slentore therefore does not label generic content-chunk gaps
as inter-token latency. With token timing disabled, true ITL remains explicitly
unavailable:

```json
{
  "available": false,
  "source": "not_available",
  "reason": "OpenAI-compatible SSE chunks are not guaranteed to map 1:1 to tokenizer tokens"
}
```

With complete singleton vLLM evidence, generated tokens arriving at canonical
request-relative offsets `t1, t2, ..., tn` produce:

```text
ITL = [t2-t1, t3-t2, ..., tn-t(n-1)]
count = output_tokens - 1
```

`metrics.json` stores the individual millisecond gaps plus count, mean,
minimum, maximum, source, and availability/reason. At least two server-reported
output tokens are required. Missing usage, missing or excess ID coverage,
negative/decreasing offsets, invalid IDs, incomplete streams, or any multi-ID
event make true ITL unavailable for that request. Zero gaps are permitted at
client timestamp resolution.

Slentore ITL is client-observed inter-token arrival latency. It is not an
internal GPU decode-kernel duration, model compute time, or server-scheduler-only
decode measurement. TTFT remains request start to first non-empty visible
content, TPOT retains its content-window formula, and ICL remains the gap
distribution over visible content-bearing events.

`response_bytes` is the sum of UTF-8 bytes in non-empty generated-content
deltas. `response_body_bytes` is the number of HTTP body bytes read, including
SSE framing, usage events, and `[DONE]`.

## Run aggregation methodology

Aggregation is deterministic post-processing performed after warmup,
measurement, stop-admission, and drain. It consumes per-request metrics and
lifecycle/arrival evidence without changing them. The summary uses only
`measured` request and arrival records: warmup latency, outcomes, usage, ICL,
ITL, and scheduler lag never enter a measured aggregate.

Request counts distinguish requested or planned work, started/completed HTTP
requests, successful requests, and each terminal failure outcome (`request_error`,
`request_timeout`, `parent_cancelled`, and `drain_timeout`). Request latency and
per-request token-rate distributions use successful measured requests, with an
independent availability cohort for each metric. Thus a run can have different
TTFT, TPOT, and ITL sample counts; an unavailable value is never inserted as
zero. Failed requests remain in the outcome counts even when partial timings on
those requests are excluded from successful-request latency distributions.

All distribution percentiles are exact nearest-rank values. Given sorted
samples `x[0] ... x[n-1]`, percentile `p` uses
`x[max(ceil(p*n)-1, 0)]`, with no interpolation. For example,
`[10,20,30,40]` has p50 `20` and p90/p95/p99 `40`; a one-sample distribution
returns that sample for every percentile. An empty distribution is explicitly
unavailable with sample count zero and a reason. Slentore calculates p50, p90,
p95, and p99 plus mean, minimum, and maximum from temporary exact numeric
samples; it does not persist the sorted vectors or use an approximate
histogram.

ICL and true ITL each expose two deliberately different views:

- The request-mean distribution takes one mean from every successful measured
  request where that interval metric is available, so requests have equal
  weight.
- The pooled-interval distribution combines all proven gaps, so individual
  intervals have equal weight. Contributing-request and interval counts make
  the weighting explicit.

True ITL is available only from the
`vllm_return_token_ids_client_receive` source established by the per-request
evidence. Generic unavailable ITL is not approximated from ICL or TPOT, and an
aggregate containing an incompatible available source is rejected.

The measured completion window is `measurement.elapsed_ns / 1e9`, spanning
measurement start through completion/drain of admitted measured requests. It
is the denominator for:

```text
completed_request_throughput  = completed started requests / completion window
successful_request_throughput = successful requests        / completion window
input_token_throughput        = successful input tokens    / completion window
output_token_throughput       = successful output tokens   / completion window
total_token_throughput        = successful total tokens    / completion window
goodput                       = SLO-good requests           / completion window
```

Token totals and throughput are available only when valid server usage covers
every successful measured request. Partial coverage reports its covered and
successful request counts plus an unavailable reason; it never reports a
throughput from the covered subset.

Open-loop admission metrics instead use the configured admission duration, so
drain time cannot reduce the offered or start rates:

```text
planned_arrival_rate = planned arrivals / admission duration
actual_start_rate    = started arrivals / admission duration
delivery_ratio       = started / planned
client_limited_ratio = client_limited / planned
scheduler_limited_ratio = scheduler_limited / planned
cancellation_unprocessed_ratio = unprocessed_due_to_cancellation / planned
```

The separately persisted configured request rate is not overwritten by
`planned / duration`, which can differ at a fractional schedule boundary.
Scheduler-lag percentiles use every measured arrival that actually started and
has lag evidence, even when its request later failed; dropped or unprocessed
arrivals do not enter that distribution. Request success and failure rates use
started requests as their denominator, retain numerator and denominator, and
are ratios in `[0,1]`. They are unavailable when no request started.

Optional `benchmark.slo` thresholds, or `--slo-ttft`, `--slo-tpot`, and
`--slo-e2e`, configure TTFT, TPOT, and E2E maximums. A metric passes with
inclusive semantics (`value <= threshold`). A successful request is evaluable
only when every configured metric is available; it is good when all pass, bad
when at least one fails, and unevaluable when at least one is unavailable.
Per-metric available, passing, failing, and unavailable counts are retained.
Goodput is good requests divided by the completion window, not the pass ratio
among evaluable requests or successful throughput. With no threshold, goodput
is explicitly unavailable with reason `no SLO configured`.

Failed, cancelled, client-limited, scheduler-limited, and drain-timeout runs
still receive summaries from the evidence that exists. `complete: false`
marks cancellation or an incomplete requested/planned workload; the summary
preserves the original requested/planned count and never manufactures timings
for work that did not start. Run status and error class remain adjacent to
otherwise healthy latency numbers so a partial or load-delivery failure cannot
be mistaken for a clean benchmark.

## Artifacts and failure behavior

After drain, Slentore creates:

```text
runs/
└── 20260816T120501Z-a31f00ff/
    ├── run.json
    ├── summary.json
    ├── summary.csv
    ├── warmup/
    │   ├── arrivals.jsonl       # open-loop only
    │   └── requests/
    │       └── warmup-000001/
    │           ├── observation.json
    │           └── metrics.json
    └── measured/
        ├── arrivals.jsonl       # open-loop only
        └── requests/
            ├── req-000001/
            │   ├── observation.json
            │   └── metrics.json
            └── req-000002/
                ├── observation.json
                └── metrics.json
```

- Both request roots are always created, including an empty `warmup/requests`
  directory when warmup is skipped.
- `run.json`, `summary.json`, and request artifacts are generated under schema
  version 7. `run.json` retains explicit load mode and
  mode-specific configuration plus one run-level workload object containing
  safe prompt length/hash, requested output maximum, and, for token-length
  runs, exact target/resolved input, builder, tokenizer contract, behavioral
  fingerprint, and context evidence. It also retains build, endpoint, safety,
  and client diagnostics; ordered
  lifecycle transitions; explicit stop-admission wall/relative evidence;
  separate phase status, timing, counts, concurrency, and mutually exclusive
  outcome totals; drain timing, timeout/cancellation state and affected IDs;
  token-timing mode/source, and the overall completed, failed, or canceled
  status and error.
- Open-loop `arrivals.jsonl` files retain safe scheduling/admission evidence
  and counts for started, client-limited, scheduler-limited, and cancellation-
  unprocessed arrivals. Closed-loop schema-7 runs omit arrival files.
- `summary.json` is the complete structured run summary. `summary.csv` uses a
  deterministic column order with one header and exactly one run data row so a
  later experiment layer can combine runs naturally. An unavailable scalar is
  an empty CSV field, never `0`, `NaN`, or `N/A`; JSON availability flags and
  reasons preserve the distinction.
- `observation.json` contains `(run_id, request_id)`, wall timestamps,
  request-relative nanosecond offsets, safe content/token-cardinality event
  evidence, server usage when
  supplied, HTTP status, finish reason, byte counts, and error state.
- `metrics.json` contains `(run_id, request_id)` and values derived from that
  observation.

The writer keeps warmup and measured cohorts separate, sorts a copy of each by
request sequence, validates phase-specific identities, ranges, duplicates,
counts and outcome totals, and recalculates the canonical summary from the raw
artifacts. It stages the complete tree, including both summary files, and
atomically renames it only after every file has been written. Metric derivation
and filesystem work happen after drain. Warmup observations are never mixed
into the measured cohort. Historical schema-1 through schema-6 evidence remain
unchanged.

Artifacts deliberately exclude the raw prompt, request body, generated text,
raw SSE JSON, response body, and API credentials.

Configuration, URL, secret, admission, and client-construction errors occur
before lifecycle timing, create no run directory, and exit with status 2. Once
the lifecycle starts, DNS/connect errors, per-request timeouts, non-2xx
responses, malformed streams, cancellation, drain timeout, and a clean stream
with no generated content preserve available phase-scoped observation and
metric artifacts before exiting with status 1. Tokenizer or workload-building
failures also exit 1 before lifecycle timing and create no artifacts. Request
failures do not stop later IDs in their phase, and warmup failures do not
suppress measurement.
Parent cancellation stops admission according to the lifecycle rules above.
Artifact and coordinator failures also exit 1. Missing usage alone is not a
request failure; usage-dependent metrics are simply unavailable. Exit status 0
requires every warmup and measured request to succeed, no cancellation or
drain timeout, and the complete artifact tree to be written.

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

This version runs one optional request-count warmup and one measured closed- or
open-loop cohort and summarizes that one run. It contains no warmup-duration
control, load or workload sweep, database, client-capacity calibration, GPU
discovery, deployment automation, or observability integration. The lifecycle
coordinator reuses the existing
`Runner.RunRequest` primitive
without changing how an individual request is observed or how its metrics are
calculated. The fake server provides controlled HTTP/SSE validation and an
opt-in byte-tokenizer fixture; it does not simulate GPU, model, real tokenizer,
or vLLM capacity behavior.
