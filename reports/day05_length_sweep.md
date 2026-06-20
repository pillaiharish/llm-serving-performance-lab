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
| Case   | Input | Output | Failed | TTFT p95 (ms) | TPOT p50 (ms) | ITL p50 (ms) | E2E p95 (ms) | Output tok/s | Notes |
| ------ | ----: | -----: | -----: | -------: | -------: | ------: | ------: | -----------: | ----- |
| small | 157 | 64 | 0 | 84.80 | 2.08 | 2.08 | 215.67 | 1566.47 | Lowest E2E p95, but TTFT p95 had a high tail in this run |
| medium | 541 | 128 | 0 | 31.50 | 2.09 | 2.09 | 296.37 | 1742.67 | Best TTFT p95 and output throughput in this single run  |
| large | 2077 | 256 | 0 | 92.10 | 2.30 | 2.23 | 664.01 | 1568.42 | Highest E2E p95; longer input/output increased total latency  |

## Interpretation

The length sweep shows that larger workloads increased end-to-end latency.

E2E p95 increased from 215.67 ms in the small case to 296.37 ms in the medium case and 664.01 ms in the large case.

This is expected because output length increased from 64 to 128 to 256 tokens, so the decode phase had more work to do. The large case also had much longer observed input length, which increases prefill work.

TTFT did not increase monotonically in this single run. The medium case had lower TTFT p95 than the small case. This means I should not claim a strict relationship from one run. Repeated runs are needed to separate real behavior from noise, warmup effects, scheduling effects, or benchmark artifacts.

TPOT and ITL were stable between small and medium, around 2.08–2.09 ms. In the large case, TPOT p50 increased to 2.30 ms and ITL p50 increased to 2.23 ms, showing slight decode degradation.

The medium case had the highest output token throughput and the best TTFT p95 in this run, but it was not the fastest end-to-end. The small case had the lowest E2E p95 because it generated fewer output tokens.

Therefore, the safest conclusion is:

> For this single-run synthetic benchmark at concurrency 4, increasing output length clearly increased E2E latency. Medium had the best TTFT p95 and output throughput, but more repeated runs are needed before making strong claims about the best length configuration.

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

- The length sweep ran successfully for small, medium, and large cases.
- All three runs completed with 0 failed requests.
- Raw JSON and text logs were saved.
- Output token length matched the configured output length in all three cases.
- The experiment showed a clear E2E latency increase as workload size increased.


## What failed / what is weak

- The report initially had TODO values.
- There are no repeated runs yet.
- TTFT p95 was not monotonic, so a strong TTFT conclusion is not supported.
- Only one concurrency level was tested.
- Random synthetic prompts may not represent real traffic.
- No CSV parser or plots were added for Day 5 yet.

## What I learned

- Input length mainly affects the prefill phase, but observed input tokens include chat/benchmark overhead.
- Output length mainly affects the decode phase and total E2E latency.
- TPOT and ITL stayed stable for small and medium, then degraded slightly for large.
- Output throughput alone does not decide the best configuration.
- The best configuration depends on the metric: small had best E2E p95, medium had best TTFT p95 and output throughput, large showed higher total latency.

## Further observations

Actual input tokens can be higher than configured input length

| Case   | Configured input | Total input tokens | Actual input/request |
| ------ | ---------------: | -----------------: | -------------------: |
| small  |              128 |               2512 |                  157 |
| medium |              512 |               8656 |                  541 |
| large  |             2048 |              33232 |                 2077 |

The extra tokens likely come from chat formatting / prompt wrapper / benchmark construction. So write:

“Configured random input length is not always equal to final tokenized input length. The benchmark produced 157, 541, and 2077 observed input tokens per request for the 128, 512, and 2048 configured cases.”

Output tokens were exactly what was configured

| Case   | Configured output | Total generated tokens | Output/request |
| ------ | ----------------: | ---------------------: | -------------: |
| small  |                64 |                   1024 |             64 |
| medium |               128 |                   2048 |            128 |
| large  |               256 |                   4096 |            256 |

Longer input/output increased E2E latency

| Case   |   E2E p95 |
| ------ | --------: |
| small  | 215.67 ms |
| medium | 296.37 ms |
| large  | 664.01 ms |

This is a clear trend: E2E p95 increased as the workload got larger.

Longer input can increase TTFT because prefill cost grows.
| Case   | TTFT p95 |
| ------ | -------: |
| small  | 84.80 ms |
| medium | 31.50 ms |
| large  | 92.10 ms |

Longer input can increase TTFT because prefill work grows, but this single run did not show a clean monotonic TTFT trend. The medium case had lower TTFT p95 than the small case, so repeated runs are needed before drawing a strong TTFT conclusion.

TPOT and ITL stayed close for small and medium, then degraded slightly for large.

| Case   | TPOT p50 | ITL p50 |
| ------ | -------: | ------: |
| small  |  2.08 ms | 2.08 ms |
| medium |  2.09 ms | 2.09 ms |
| large  |  2.30 ms | 2.23 ms |

So decode speed stayed stable for small/medium, but large had slightly slower per-token generation.
Medium had the best TTFT p95 and output throughput in this single run, but it did not have the best E2E latency. Small was still faster end-to-end because it generated fewer output tokens. Therefore, I cannot say medium is best overall. I can only say medium had the best throughput/TTFT behavior among these three single-run cases.


## Next actions

- Day 6: wider concurrency sweep.


## Summary

This PR adds the Day 5 prompt/output length sweep for the LLM Serving Performance Lab.

It compares three synthetic workloads at fixed max concurrency 4:

| Case | Configured input | Configured output |
|---|---:|---:|
| small | 128 | 64 |
| medium | 512 | 128 |
| large | 2048 | 256 |

## Result summary


## Interpretation

E2E p95 increased as prompt/output size increased. This is expected because longer output means more decode work, and longer input means more prefill work.

TTFT did not increase monotonically in this single run, so this PR does not claim a strict TTFT relationship. Medium had the best TTFT p95 and output throughput in this run, while small had the best E2E p95.

## Caveat

This is one synthetic run per case. Repeated runs are needed before making strong claims.

## Next

Day 6 will expand concurrency to 1, 2, 4, 8, and 16.