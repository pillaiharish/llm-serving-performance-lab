# LLM Serving Metrics Glossary

## TTFT — Time to First Token

Time from request submission to the first generated token.

Functions:

```text
User's initial wait.
Highly affected by prompt length, queueing, prefill, and KV cache reuse.
```

Bad sign:

```text
TTFT grows sharply with concurrency or prompt length.
```

---

## ITL — Inter-Token Latency

Time between generated output tokens during streaming.

Functions:

```text
Controls how smooth streaming feels.
Decode-heavy workloads care about this.
```

---

## TPOT — Time Per Output Token

Average time spent per output token.

Functions:

```text
Useful for comparing generation speed across models/configs.
Often similar to ITL but reported differently depending on benchmark tool.
```

---

## E2E Latency — End-to-End Latency

Total request time from submission to final token.

Approximation:

```text
E2E ~= TTFT + output_tokens * ITL
```

---

## Throughput

Usually measured as:

```text
output tokens/sec
requests/sec
total tokens/sec
```

Brutal warning:

```text
High throughput can still be unusable if p95/p99 latency is bad.
```

---

## Goodput

Requests/sec that meet the defined latency SLO.

Example:

```text
p95 TTFT < 800 ms
p95 TPOT < 60 ms
p99 E2E < 8 sec
```

If a request misses the SLO, it does not count as good traffic.

---

## KV Cache

Key/value tensors stored during inference so the model does not recompute all previous tokens on every decode step.

Functions:

```text
More active tokens = more KV cache memory.
Long context and high concurrency can exhaust GPU memory.
```

---

## Prefill

The phase where the server processes the input prompt and builds KV cache.

Heavy when:

```text
prompts are long
RAG context is large
many requests arrive together
```

---

## Decode

The phase where the model generates output tokens one by one.

Heavy when:

```text
outputs are long
many users are streaming at once
```

---

## Saturation point

The point where adding concurrency no longer improves useful throughput and p95/p99 latency begins to explode.

The benchmark reports should identify this point.
