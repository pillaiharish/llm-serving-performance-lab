# Day 2 Benchmark Report

## Summary

Date: 2026-06-13  
Model: models/Qwen/Qwen2.5-0.5B-Instruct  
GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB  
Server: vLLM OpenAI-compatible API  
Configured random input length: 128 tokens  
Observed input tokens per request: 157 tokens  
Output length: 64 tokens  
Total requests per run: 16  
Concurrency sweep: 1, 2, 4  

## Goal

Run the first small vLLM benchmark loop and save raw benchmark evidence.

This is not a production benchmark. This is a Day 2 baseline to verify that the serving + measurement loop works.

## Server command

```bash
source .venv/bin/activate
source configs/vllm_default.env

vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```

## Smoke test

Command:

```bash
python scripts/smoke_chat.py | tee results/day02/logs/pre_benchmark_smoke.txt
```

Result:

```text
Base URL: http://localhost:8000/v1
Model: models/Qwen/Qwen2.5-0.5B-Instruct
Available models:
- models/Qwen/Qwen2.5-0.5B-Instruct
Response: SMOKE_TEST_OK
```

## Benchmark command

```bash
./scripts/run_day02_bench.sh
```

The benchmark ran 16 total requests per concurrency level with:

```text
--dataset-name random
--random-input-len 128
--random-output-len 64
--request-rate inf
--max-concurrency 1, 2, 4
--percentile-metrics ttft,tpot,itl,e2el
--metric-percentiles 50,90,95,99
--save-result
--save-detailed
```

## Result files

```text
results/day02/raw/day02_c1_i128_o64.json
results/day02/raw/day02_c2_i128_o64.json
results/day02/raw/day02_c4_i128_o64.json

results/day02/logs/bench_c1_i128_o64.txt
results/day02/logs/bench_c2_i128_o64.txt
results/day02/logs/bench_c4_i128_o64.txt
```

## Results summary

| Concurrency | Successful | Failed | Request throughput | TTFT p50 | TTFT p95 | TPOT p50 | ITL p50 | E2E p95 | Output tok/s |
|---:|---:|---:|---:|---:|---:|---:|---:|---:|---:|
| 1 | 16 | 0 | 6.85 req/s | 7.35 ms | 11.08 ms | 2.19 ms | 2.19 ms | 148.70 ms | 438.30 |
| 2 | 16 | 0 | 12.23 req/s | 8.86 ms | 11.60 ms | 2.44 ms | 2.45 ms | 164.94 ms | 782.64 |
| 4 | 16 | 0 | 23.85 req/s | 8.99 ms | 12.99 ms | 2.49 ms | 2.50 ms | 170.16 ms | 1526.10 |

## Interpretation

Increasing max concurrency from 1 to 4 improved output token throughput from 438.30 tok/s to 1526.10 tok/s.

Latency increased, but not dramatically at this small load size:

- TTFT p95 increased from 11.08 ms to 12.99 ms.
- TPOT p50 increased from 2.19 ms to 2.49 ms.
- E2E p95 increased from 148.70 ms to 170.16 ms.

This suggests the small Qwen2.5-0.5B model on RTX 5070 Ti can handle this tiny benchmark without saturation at concurrency 4.

## Caveats

This is a small benchmark, not a production conclusion.

Limitations:

- only 16 requests per run
- no warmup requests configured
- one model only
- one input/output length only
- one GPU only
- no concurrency above 4 yet
- no charts yet
- no repeated runs to estimate variance

Also, the configured random input length was 128, but the saved JSON shows 157 input tokens per request. This likely comes from chat formatting / benchmark prompt construction, so future reports should separate configured prompt length from observed tokenized input length.

## What worked

- vLLM server was already validated with a smoke test.
- Benchmark ran successfully for concurrency 1, 2, and 4.
- No failed requests were reported.
- Raw JSON results were saved.
- Text logs were saved.
- Percentile metrics were captured for TTFT, TPOT, ITL, and E2EL.

## What failed / what is weak

- The first report version was incomplete.
- The benchmark used only 16 requests per run.
- No warmup requests were configured.
- The script did not set `--temperature 0`, and vLLM warned that greedy behavior is no longer the default.
- No chart or CSV exists yet.

## What I learned

- Throughput can improve with concurrency while latency also rises.
- TTFT measures startup responsiveness, while TPOT/ITL reflect generation behavior after the first token.
- A benchmark report must include both raw evidence and interpretation.
- Configured input length and observed tokenized input length may differ.

## Next actions

- Add `--temperature 0` to the Day 2 benchmark script for reproducibility.
- Parse the raw JSON into CSV on Day 3.
- Generate the first chart on Day 3.
- Plot concurrency versus E2E p95 and output tokens/sec.