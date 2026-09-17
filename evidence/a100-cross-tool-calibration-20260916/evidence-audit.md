# Evidence audit

## Integrity and inventory

- Source: final `20260916T025430Z` recovery archive; the earlier `20260916T023059Z` partial package was not combined with it.
- Computed SHA-256: `4ef3347e6861bc7d985543202329f5034be64ed740a5c8f26d6aaeaaf2d33db4`; matches the preserved sidecar and expected recovery digest.
- Archive members: 1,976. No absolute paths, `..` traversal components, symbolic links, or hard links were present.
- No internal checksum manifest was present; this audit therefore makes no internal-manifest claim.
- Final cohort: three Slentore, three vLLM bench, three AIPerf, and three GuideLLM repetitions.

## Controlled workload checks

- All final comparable cohorts completed 100 requests with no recorded failures.
- Every Slentore request-level usage record, vLLM input/output length, AIPerf server-token-count result, and GuideLLM raw request record reports exactly 23 input and 32 output tokens.
- Configuration evidence records concurrency 1, temperature 0, thinking disabled, and requested maximum output 32.
- The repeated prompt and enabled prefix cache establish a steady-state warm-prefix-cache calibration. Prompt text is omitted from curated evidence.
- Run order varied by repetition. The same live vLLM deployment and configuration were retained.

## Cohorts and timing evidence

| Evidence | Rep 1 | Rep 2 | Rep 3 |
|---|---:|---:|---:|
| Slentore successful requests | 100 | 100 | 100 |
| Slentore true-ITL requests | 100 | 98 | 99 |
| Slentore true-ITL intervals | 3100 | 3038 | 3069 |
| vLLM bench completed requests | 100 | 100 | 100 |
| AIPerf profiling requests | 100 | 100 | 100 |
| GuideLLM raw/scheduler successful requests | 100 | 100 | 100 |
| GuideLLM native finalized requests | 98 | 98 | 98 |

GuideLLM raw and native cohorts remain separate. AIPerf `inter_chunk_latency` is excluded from token-ITL claims. Slentore true ITL is client-observed token-arrival timing backed by token identity where available, not GPU-kernel or server-internal timing.

## Incidents and exclusions

- An initial AIPerf recovery attempt returned HTTP 401 because the API key was not explicitly passed. It is retained in the archive but excluded from every summary row. Corrected final AIPerf repetitions report no errors.
- AIPerf required a 10-request non-zero warmup phase. This operational difference is not interpreted as a performance result.
- The recorded fixed GuideLLM development build was used. The package does not preserve enough evidence to independently establish the reported earlier stable-build failure or its cause.
- vLLM generation defaults were explicit through `--generation-config vllm`; final requests used temperature 0. The package does not independently establish the earlier configuration history.
- A successful `/health` check was not treated as authentication proof; authenticated `/v1` access was checked separately.
- vLLM bench dependencies were isolated, and the custom dataset used the recorded skip-chat-template path.

## Disclosure scan

The extracted package was searched for common credential forms: `Authorization:` headers, `Bearer` values, `VLLM_API_KEY` assignments/usages, Hugging Face `hf_` tokens, and OpenAI-style `sk-` keys. Preserved scripts contain environment-variable references and archived tool output contains redacted placeholders; no credential value is copied into curated files. This was a targeted pattern scan, not a universal secret audit.

Added files were separately checked to exclude the known prompt text, generated response content, authorization values, raw request bodies, and archives. Tool-version provenance is complete for the four clients; the package does not independently pin a Hugging Face model revision.
