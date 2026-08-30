# Slentore V1 H100 Qualification

This directory contains a curated, redacted subset of the real Slentore V1
qualification evidence produced by an acceptance run on an NVIDIA H100 80GB HBM3
GPU served by vLLM.

## Qualified Slentore SHA

```
c877982609e6aa620ae196a380b54bd6d50f29ac
```

Qualification date:

```
2026-08-30 UTC
```

## Hardware

```
NVIDIA H100 80GB HBM3
81559 MiB reported
compute capability 9.0
driver 590.48.01
```

## Server

```
vLLM 0.26.0
Qwen/Qwen3.8-27B
tensor parallelism 1
max model len 32768
KV cache FP8
max-num-seqs 16
gpu-memory-utilization 0.90
```

Only configuration fields actually evidenced by the files in this directory
(and its `reports/release-evidence-manifest.txt`) are listed above.

## Qualification result

```
PASS
```

Client calibration:

```
closed-loop 1,4,8: PASS
open-loop 2,5,10 req/s: PASS
```

Real acceptance:

```
basic: PASS
closed-loop: PASS
open-loop: PASS
token-length: PASS
```

Verifier (`acceptance/verification.json`):

```
artifacts_valid = true
acceptance_passed = true
errors = []
benchmark_failures = []
```

## True ITL

The inter-token-latency (ITL) evidence for the BASIC measured requests is
release-critical: it proves the real vLLM token-evidence path worked for the
tested requests.

```
available successful BASIC requests: 5
unavailable requests: 0

source:
  vllm_return_token_ids_client_receive

violation:
  none
```

Each of the 5 BASIC measured requests produced 32 output tokens with an ITL
sample count of 31 (output_tokens - 1), sourced from
`vllm_return_token_ids_client_receive` (the client-received token-ids path
returned by vLLM). This demonstrates that the real vLLM token-evidence path
worked for the tested requests.

These acceptance loads are deliberately modest. This evidence does not make
any claim about H100 maximum capacity, safe max concurrency, production
capacity, or maximum sustainable QPS.

## What is committed here

This directory is a curated subset of the qualification evidence:

```
release manifest
acceptance/verifier contracts
aggregate run/experiment summaries
true-ITL metrics for BASIC
environment provenance
client calibration summaries
```

The exact run IDs from the qualification run are preserved in the directory
structure so the evidence retains its identity and can be cross-referenced
against the full raw qualification archive.

## What is intentionally not committed

```
raw archive
large server logs
raw per-request observations
generated text
prompts
token IDs
credentials
```

The full raw qualification archive (including the complete raw request trees,
observation files, vLLM server logs, and per-request payloads) remains external
immutable evidence and is intentionally not committed to this repository.

## Qualified code vs evidence commit

The H100 run qualified code SHA
`c877982609e6aa620ae196a380b54bd6d50f29ac`.

This evidence PR is a later documentation-only commit and therefore must not be
mistaken for the tested executable SHA. The evidence documentation commit is
newer than the qualified code SHA; the H100 qualification was executed against
the SHA above.