# Results Interpretation Guide

## Goal

This guide explains how to read benchmark results in this repo.

The goal is to avoid misleading conclusions from raw throughput numbers.

## Throughput vs latency

Throughput measures how much work the system completes per second.

Latency measures how long users wait.

A useful serving result must consider both.

High throughput is not useful if latency becomes unacceptable. Low latency is not enough if the server cannot handle enough concurrent work.

## TTFT

TTFT means Time to First Token.

It measures how long the user waits before the first generated token appears.

TTFT is affected by:

- request queueing
- prompt length
- prefill cost
- server load
- batching behavior
- KV cache behavior

TTFT matters most for interactive chat because users feel the initial wait directly.

## ITL

ITL means Inter-Token Latency.

It measures the delay between generated tokens during streaming.

Low ITL makes streaming feel smooth. High ITL makes the model feel slow even if the first token appears quickly.

## TPOT

TPOT means Time Per Output Token.

It summarizes average decode speed per generated output token.

TPOT and ITL are closely related, but they are not always reported in exactly the same way by every benchmark tool.

Use TPOT for high-level decode speed comparison. Use ITL when reasoning about streaming smoothness.

## E2E latency

E2E latency means end-to-end latency.

It measures the full request time from request submission to final response completion.

For streaming generation:

```text
E2E latency ≈ TTFT + total decode time
```

E2E latency matters because it represents the full user-visible completion time.

## p50, p95, p99

Percentiles describe distribution behavior.

- p50: median request
- p95: slower tail request
- p99: worst tail behavior among nearly all requests

Production systems usually care about p95 and p99 because users notice slow tail requests.

## Output token throughput

Output token throughput measures generated tokens per second.

It is useful for estimating serving capacity, but it must be interpreted with latency.

Higher output token throughput is good only if latency remains acceptable for the use case.

## Request throughput

Request throughput measures completed requests per second.

It can be misleading if request sizes vary.

For LLM serving, token throughput is often more meaningful than request throughput because different requests can have very different input and output lengths.

## Goodput

Goodput means useful throughput while meeting latency or reliability targets.

Example:

```text
Only count requests that finish with E2E p95 below a chosen SLO.
```

Goodput is more production-relevant than raw throughput.

## Saturation

Saturation happens when adding concurrency gives little throughput improvement but causes latency to rise sharply.

Signs of saturation:

- p95 or p99 latency grows quickly
- request failures appear
- throughput stops improving
- GPU memory pressure increases
- queueing delay dominates TTFT

The current benchmark does not find the saturation point yet because it only tested concurrency 1, 2, and 4.

## Current Day 2/3 interpretation

For the tiny Day 2 benchmark:

| Max concurrency | E2E p95 | Output tok/s |
|---:|---:|---:|
| 1 | 148.70 ms | 438.30 |
| 2 | 164.94 ms | 782.64 |
| 4 | 170.16 ms | 1526.10 |

Interpretation:

```text
Concurrency 4 gave the best throughput-latency tradeoff among the tested values.
```

But this does not prove concurrency 4 is globally optimal.

More data is needed:

- concurrency 8 and 16
- repeated runs
- different prompt lengths
- different output lengths
- longer contexts
- more realistic workloads