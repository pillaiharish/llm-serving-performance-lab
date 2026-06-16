# Day 5 Length Sweep Report

## Summary

Date: 2026-06-16  
Model: models/Qwen/Qwen2.5-0.5B-Instruct  
GPU: NVIDIA GeForce RTX 5070 Ti, 16 GB  
Server: vLLM OpenAI-compatible API  
Concurrency: 4  
Prompts per run: 16  

## Goal

Measure how prompt length and output length affect latency and throughput.

## Experiment cases

| Case | Configured input length | Configured output length | Max concurrency |
|---|---:|---:|---:|
| small | 128 | 64 | 4 |
| medium | 512 | 128 | 4 |
| large | 2048 | 256 | 4 |

## Smoke test

```text
Base URL: http://localhost:8000/v1
Model: models/Qwen/Qwen2.5-0.5B-Instruct
Available models:
- models/Qwen/Qwen2.5-0.5B-Instruct
Response: SMOKE_TEST_OK
```

## Result files

results/day05/raw/
results/day05/logs/

## Results summary
| Case   | Input | Output | Failed | TTFT p95 | TPOT p50 | ITL p50 | E2E p95 | Output tok/s | Notes |
| ------ | ----: | -----: | -----: | -------: | -------: | ------: | ------: | -----------: | ----- |
| small  |   128 |     64 |   TODO |     TODO |     TODO |    TODO |    TODO |         TODO | TODO  |
| medium |   512 |    128 |   TODO |     TODO |     TODO |    TODO |    TODO |         TODO | TODO  |
| large  |  2048 |    256 |   TODO |     TODO |     TODO |    TODO |    TODO |         TODO | TODO  |

## Interpretation

TODO: Explain what changed as input/output length increased.

## Expected learning

- Longer input should mostly increase prefill cost, which can increase TTFT.
- Longer output should increase total generation time, which can increase E2E latency.
- TPOT and ITL show whether token generation speed changes as output length grows.

## Caveats

- This is still a small benchmark.
- Only one concurrency level is tested.
- No repeated runs yet.
- Random synthetic prompts are not real user traffic.

## What worked

## What failed / what is weak

## What I learned

## Next actions

- Day 6: wider concurrency sweep.