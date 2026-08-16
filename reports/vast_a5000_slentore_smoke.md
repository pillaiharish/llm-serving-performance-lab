# Vast.ai A5000 Slentore smoke validation

This report records Slentore's first successful live-cloud validation against a
GPU-backed vLLM endpoint. It covers one OpenAI-compatible streaming request at
a time and two sequential five-run smoke profiles. It is not a concurrency,
throughput, or deployment-optimization study.

The reproducible setup is documented in the
[Vast.ai RTX A5000 vLLM deployment guide](../docs/deployments/vast-ai-a5000-vllm.md).
Sanitized machine-readable evidence is available in the
[session summary](../results/vast-a5000-smoke/summary.json).

## Environment and workload

The experiment used Vast.ai, one NVIDIA RTX A5000 with 24564 MiB VRAM, vLLM
0.26.0, and `Qwen/Qwen3.5-4B`. vLLM ran with `bfloat16`, tensor parallel size
1, maximum model length 4096, and GPU memory utilization 0.85. The server
listened on `127.0.0.1:18000` to avoid the Vast portal/Caddy process already
using port 8000.

The clean workload was sequential at concurrency one:

```text
Prompt semantic intent: Explain KV cache in two short sentences.
Requested maximum output: 64 tokens
Temperature: 0
Server-reported input: 21 tokens
Server-reported output: 42 tokens
Server-reported total: 63 tokens
Finish reason: stop
```

No generated response text, raw SSE payload, Authorization value, or request
body is included in this report or its machine-readable evidence.

## First successful remote request

The first clean remote-client request traveled from the Mac through an SSH
local forward to the Vast instance and vLLM. Slentore recorded:

| Evidence | Value |
| --- | ---: |
| HTTP status | 200 |
| Input tokens | 21 |
| Output tokens | 42 |
| Total tokens | 63 |
| Finish reason | `stop` |
| Response body bytes | 10173 |
| Time to headers | 469.052 ms |
| TTFB | 468.924 ms |
| TTFT | 643.485 ms |
| TTLT | 1676.751 ms |
| E2E | 1695.825 ms |
| TPOT | 25.202 ms/token |
| Inter-chunk latency mean | 25.832 ms |
| True ITL | unavailable |

The exact schema-1 artifacts for run
`20260816T110915Z-aa4dadf4` are preserved under
[`results/vast-a5000-smoke/artifacts/remote-client/`](../results/vast-a5000-smoke/artifacts/remote-client/20260816T110915Z-aa4dadf4/).

True inter-token latency remains deliberately unavailable. An
OpenAI-compatible SSE content event may contain part of a token, one token, or
multiple tokens, so its arrival gap cannot be labeled tokenizer ITL. Slentore
reports the observable gaps as inter-chunk latency instead.

## Connection-refused partial evidence

When the SSH tunnel disappeared, Slentore attempted the real endpoint and the
connection to `127.0.0.1:18000` was refused before an HTTP response arrived.
The cited run recorded:

```text
Status: unavailable
Input/output usage: unavailable
Headers: unavailable
TTFB: unavailable
TTFT: unavailable
TTLT: unavailable
E2E: 3.936 ms
Error category: connection refused
```

This validates useful measured-failure behavior: measurement had started,
response-dependent evidence remained unavailable rather than becoming a
fabricated zero or non-finite value, client-side E2E remained measurable, and
partial artifacts were written. The exact artifacts for run
`20260816T105853Z-c8cfb842` are included with the other
[remote-client artifacts](../results/vast-a5000-smoke/artifacts/remote-client/20260816T105853Z-c8cfb842/).

## Remote-client five-run baseline

Label: **remote-client / SSH-tunnel / sequential C=1 smoke baseline**

```text
Mac -> Internet -> SSH tunnel -> Vast -> vLLM -> Qwen -> A5000
```

Every run reported 21 input tokens, 42 output tokens, temperature 0, and
`finish_reason=stop`.

| Run | TTFB ms | TTFT ms | E2E ms | TPOT ms/token | ICL mean ms |
| --: | ------: | ------: | -----: | ------------: | ----------: |
| 1 | 546.140 | 567.745 | 1451.719 | 21.559 | 22.098 |
| 2 | 550.663 | 551.192 | 1292.981 | 18.091 | 18.544 |
| 3 | 475.839 | 597.510 | 1459.750 | 18.358 | 18.817 |
| 4 | 407.408 | 509.630 | 1343.548 | 20.338 | 20.847 |
| 5 | 407.906 | 525.396 | 1310.740 | 19.154 | 19.632 |
| **Median** | **475.839** | **551.192** | **1343.548** | **19.154** | **19.632** |

The committed run directories contain the full available precision and the
complete sanitized Slentore observation and metric schemas.

## Server-local five-run baseline

Slentore was cross-compiled for `linux/amd64`, copied to the Vast instance, and
run directly against `http://127.0.0.1:18000/v1`. The workload and sequential
C=1 execution were unchanged.

| Run | Headers ms | TTFB ms | TTFT ms | TTLT ms | E2E ms | TPOT ms/token | ICL mean ms |
| --: | ---------: | ------: | ------: | ------: | -----: | ------------: | ----------: |
| 1 | 4.735 | 4.681 | 85.386 | 885.447 | 907.522 | 19.514 | 20.002 |
| 2 | 4.908 | 4.828 | 77.145 | 868.932 | 897.338 | 19.312 | 19.795 |
| 3 | 3.903 | 3.769 | 75.321 | 868.099 | 901.907 | 19.336 | 19.819 |
| 4 | 4.659 | 4.530 | 73.571 | 855.887 | 888.608 | 19.081 | 19.558 |
| 5 | 4.867 | 4.657 | 75.524 | 863.025 | 894.488 | 19.207 | 19.688 |
| **Median** | **4.735** | **4.657** | **75.524** | **868.099** | **897.338** | **19.312** | **19.795** |

The Vast instance was terminated before the exact server-local `run.json`,
`observation.json`, and `metrics.json` directories were recovered. These table
values come from captured benchmark output. The session summary records that
provenance and does not fabricate run IDs, timestamps, offsets, stream events,
or replacement raw artifacts.

## Interpretation

The strongest comparison is the nearly unchanged median decode timing:

```text
Remote median TPOT: 19.154 ms/token
Local median TPOT:  19.312 ms/token
```

The reciprocal of the local median TPOT is approximately 51.8 output
tokens/second for this fixed sequential workload. That is a descriptive
conversion, not a concurrency throughput or GPU-capacity claim.

The front of the request changed much more:

```text
Remote median TTFB: 475.839 ms
Local median TTFB:    4.657 ms

Remote median TTFT: 551.192 ms
Local median TTFT:   75.524 ms
```

For these runs, the median differences were about 471 ms for TTFB and 476 ms
for TTFT. This suggests that roughly 470-480 ms of the remote front-of-request
latency arose in the particular Mac, Internet, and SSH path rather than in the
server-local serving path. It is not a universal network-latency estimate and
should not be generalized to another client, route, proxy, or Vast host.

Server-local measurements help isolate serving and inference behavior.
Remote-client measurements represent latency actually observed by that client,
including network and proxy layers. Both are valuable evidence, and Slentore
should preserve them independently rather than subtracting a presumed network
component internally.
