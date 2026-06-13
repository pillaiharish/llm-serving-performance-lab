# LLM Serving Metrics Glossary

## TTFT — Time to First Token

Time from request submission to the first generated token. The time it takes to generate the first token after sending a request. It reflects how fast the model can start responding.

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

## E2E Latency — End-to-End Latency

Total request time from submission to final token. The time from sending the request to receiving the final token on the user end. Total latency directly affects perceived responsiveness. A fast TTFT followed by slow token generation still leads to a poor experience.

Approximation for streaming generation:

```text
E2E ≈ TTFT + sum(inter-token gaps)
```
A rough simplified estimate is:

```text
E2E ≈ TTFT + generated_tokens * average_ITL
```

---

## ITL — Inter-Token Latency

Time between generated output tokens during streaming. The exact pause between two consecutive tokens. 
For a single request, the mean of all ITLs equals TPOT, which is why the two are sometimes used interchangeably:

Functions:

```text
Controls how smooth streaming feels.
Decode-heavy workloads care about this.
Lower ITL means tokens appear more quickly during generation.
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

## Acceptable latency

Acceptable latency depends on the use case and on which metric is being measured.

Examples:

- chat: low TTFT matters for perceived responsiveness
- code generation: steady ITL matters because users watch tokens stream
- batch reports: E2E latency can be much higher if the job is asynchronous

The engineering task is to define an SLO for the specific workload instead of using one latency target for every product.