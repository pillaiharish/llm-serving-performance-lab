# Sample Corpus for LLM Serving Metrics

## TTFT

Time To First Token measures how long the client waits before receiving the first generated token. TTFT is strongly affected by prompt processing, queueing, model load, batching behavior, and server overhead.

## TPOT

Time Per Output Token measures the average time spent generating each output token after the first token has arrived. TPOT is closely related to decode speed.

## ITL

Inter Token Latency measures the delay between generated tokens. Stable ITL usually means smoother streaming behavior.

## E2E Latency

End-to-end latency measures the total time from request start to final response completion. It includes queueing, prefill, decoding, network overhead, and client-side measurement overhead.

## Throughput

Throughput measures how much work the system completes per unit time. For LLM serving, output tokens per second is often used. Throughput can improve with concurrency until the system becomes saturated.

## Concurrency

Concurrency is the number of requests being handled at the same time. Higher concurrency can increase throughput, but it can also increase TTFT and tail latency.

## Prompt Length

Prompt length affects the prefill phase. Longer prompts usually increase TTFT because the model must process more input tokens before generating output.

## Output Length

Output length affects the decode phase. Longer outputs usually increase end-to-end latency because the model must generate more tokens.

## Benchmark Caveats

Small local benchmarks are useful for learning and comparison, but they are not production capacity claims. Production serving depends on traffic shape, model size, hardware, batching, memory pressure, queueing, availability requirements, and workload diversity.