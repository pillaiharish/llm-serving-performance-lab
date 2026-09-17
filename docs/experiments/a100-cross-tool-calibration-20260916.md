# A100 cross-tool client metric calibration

## Question and scope

When model, GPU, server, workload, concurrency, generation settings, and cache state are controlled, do Slentore's comparable client-visible latency and throughput metrics numerically agree with vLLM bench, NVIDIA AIPerf, and GuideLLM, and where do their definitions differ?

This is a steady-state repeated-prompt warm-prefix-cache client metric calibration, not another performance study or a benchmark-tool ranking. The raw recovery archive remains local; the curated evidence is under [`evidence/a100-cross-tool-calibration-20260916/`](../../evidence/a100-cross-tool-calibration-20260916/README.md).

## Controlled environment and workload

The three repetitions used one NVIDIA A100-SXM4-40GB (40960 MiB, driver 580.105.08), Qwen/Qwen3-8B, and one vLLM 0.26.0 deployment. It used bfloat16 weights, TP=1, max model length 32768, max sequences 16, GPU memory utilization 0.90, KV-cache dtype `auto`, vLLM generation defaults, thinking disabled, and prefix caching enabled.

Each tool ran closed-loop at concurrency 1 against a warm repeated prompt. Every final controlled request had exactly 23 server-reported input tokens and 32 output tokens; output maximum was 32 and temperature was 0. Each tool completed three 100-request repetitions. The prompt itself is intentionally not published.

This differs materially from the [prefix-cache-OFF controlled performance study](a100-qwen3-8b-prefixoff-20260916.md), including input length, output behavior, cache state, and purpose. Their absolute results must not be combined or interpreted as a cache-effect experiment.

## Tool identities

- Slentore `v0.0.0-20260830170145-4a573b104970`, revision `4a573b104970d7300ebebd136548e0c679fd4f74`
- vLLM server and vLLM bench 0.26.0
- NVIDIA AIPerf 0.12.0
- GuideLLM 0.8.0.dev14 at commit `d3a6da9d5055582cbafbf4e09bf05ba7937029b5`

## Metric-boundary mapping

Slentore TPOT, vLLM bench TPOT, AIPerf `inter_token_latency`, and GuideLLM raw-response `inter_token_latency_ms` share the relevant per-request decode cadence boundary and `(output tokens - 1)` denominator for this controlled case. They are the decode metrics compared below.

TTFT and E2E are shown as contextual client-visible metrics because each tool's exact request/window implementation can differ. AIPerf `inter_chunk_latency` is chunk timing, not true token ITL. GuideLLM native `time_per_output_token_ms` includes a different front/timing boundary and is not compared to Slentore TPOT. The detailed mapping is in [`metric-semantics.md`](../../evidence/a100-cross-tool-calibration-20260916/metric-semantics.md).

### Slentore true ITL

Slentore separately recorded client-observed token-arrival ITL backed by full token-identity coverage for each request where the metric is available. Coverage was 100/100 requests and 3100 intervals in repetition 1, 98/100 and 3038 in repetition 2, and 99/100 and 3069 in repetition 3. Unavailable requests were not imputed. This is neither GPU-kernel decode latency nor server-internal token time.

### GuideLLM cohorts

GuideLLM preserved 100 successful raw/scheduler request records per repetition, but its native finalized aggregate contains 98 requests per repetition. The repetition table keeps both representations separate. Comparable GuideLLM decode and throughput values come only from the raw 100-request cohort; its native TPOT is deliberately blank.

## Results

The [repetition table](../../evidence/a100-cross-tool-calibration-20260916/repetition-summary.csv) has 15 rows: four final tools across three repetitions, with GuideLLM's two cohorts separate. The [tool summary](../../evidence/a100-cross-tool-calibration-20260916/tool-summary.csv) contains mean-of-repetition summary values, not pooled request percentiles.

| Tool and comparable cohort | Mean decode p50 (ms/token) | Mean successful req/s | Mean output tok/s |
|---|---:|---:|---:|
| Slentore summary-100 | 13.546515 | 2.264383 | 72.460269 |
| vLLM bench result-100 | 13.544507 | 2.257443 | 72.238164 |
| AIPerf profiling-100 | 13.555402 | 2.251462 | 72.046778 |
| GuideLLM raw-request-100 | 13.538455 | 2.253775 | 72.120785 |

Relative to the other tool's mean decode p50, Slentore differed by +0.014825% from vLLM bench, -0.065562% from AIPerf, and +0.059532% from GuideLLM raw-100. Thus, for this one controlled A100/Qwen3-8B, C=1, 23-input/32-output, repeated-prompt warm-prefix-cache experiment, Slentore's comparable client-visible decode metric and throughput were closely aligned with the corresponding vLLM, AIPerf, and GuideLLM raw-response metrics.

## Reproducibility incidents

The archive supports several operational lessons: the final server explicitly used `--generation-config vllm`; `/health` alone was not authentication proof, so `/v1` access was checked separately; requested output was treated as a ceiling and actual output was verified; vLLM bench dependencies were isolated and its custom dataset used the appropriate skip-chat-template path; an initial AIPerf HTTP 401 attempt was excluded after API-key propagation was corrected; and AIPerf used a non-zero warmup. The final procedure records the fixed GuideLLM development build, but not enough evidence to independently establish the reported earlier stable-build failure or its cause. These are reproducibility notes, not evidence that one tool is better.

## Claim boundaries

This study does not prove universal correctness of Slentore, equivalence of every metric exposed by every tool, production-workload accuracy, cross-model or cross-GPU equivalence, networked-client equivalence, cache-OFF equivalence, distributed-serving equivalence, GPU-kernel timing equivalence, quality/evaluation equivalence, or tool superiority.

TTFT and E2E depend on exact client request/window semantics. TPOT-like values are compared only where timing boundaries and denominators match. Inter-chunk latency is not labeled token ITL. The result applies only to the recorded controlled case.

## Raw-evidence policy

No raw archive, prompt, generated response, credential, request body, server log, or large telemetry file is committed. The curated tables were independently derived from the immutable archive whose SHA-256 and audit are published in the evidence directory. Missing or non-comparable metrics remain blank rather than being guessed.
