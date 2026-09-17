# Metric semantics

Numerical proximity is assessed only after matching timing boundary and denominator. Similar names alone do not establish equivalence.

| Tool | Metric | Timing boundary and denominator | Cohort | Comparable to Slentore? | Notes |
|---|---|---|---|---|---|
| Slentore | TTFT | request start to first content token | 100 requests | Reference only | Client-visible. |
| vLLM bench | TTFT | client request start to first streamed token | 100 requests | Contextual | Small boundary/implementation differences can affect TTFT. |
| AIPerf | `time_to_first_token` | client request timing to first token | 100 profiling requests | Contextual | Stored output reports milliseconds. |
| GuideLLM | raw `time_to_first_token_ms` | raw request start to first token | 100 raw requests | Contextual | Native finalized aggregate covers 98 requests. |
| Slentore | TPOT | last-token minus first-token duration divided by output tokens minus one | 100 requests | Reference | Request-level decode cadence, ms/token. |
| vLLM bench | TPOT | decode duration divided by output tokens minus one | 100 requests | Yes | Same denominator and client-visible streaming boundary for this workload. |
| AIPerf | `inter_token_latency` | per-request decode duration divided by output tokens minus one | 100 profiling requests | Yes | Distinct from AIPerf `inter_chunk_latency`. |
| GuideLLM | raw `inter_token_latency_ms` | raw-response last-token minus first-token duration divided by output tokens minus one | 100 raw requests | Yes | Nearest-rank request-level percentiles were recomputed. |
| GuideLLM | native `time_per_output_token_ms` (TPOT) | includes a different front/timing boundary in the numerator | 98 finalized requests | No | Deliberately blank in the comparable-decode columns. |
| All tools | E2E/request latency | client-visible request start to completion, with tool-specific window details | stated cohort | Contextual | Not treated as perfectly interchangeable. |
| Slentore | true ITL | client-observed token-arrival gaps backed by returned token identities | 100/98/99 requests | Unique evidence here | Source: `vllm_return_token_ids_client_receive`; unavailable requests are not imputed. |
| AIPerf | `inter_chunk_latency` | gaps between received stream chunks | 100 profiling requests | No | Chunk timing is not true token ITL. |
| vLLM bench | ITL output | client stream timing without preserved token-identity proof in this package | 100 requests | No true-ITL claim | Not used for token-identity comparison. |

Throughput uses each tool's completed-request measurement window. Because concurrency is one and every comparable raw cohort has 100 successful requests with exactly 32 output tokens, request/s and output-token/s are useful controlled cross-checks, not universal formula equivalence.
