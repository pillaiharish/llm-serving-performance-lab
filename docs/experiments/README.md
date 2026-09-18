# Slentore experiments and evidence

## Current controlled studies

### [A100 Qwen3-8B prefix-cache-OFF inference study](a100-qwen3-8b-prefixoff-20260916.md)

One A100-SXM4-40GB served Qwen3-8B with vLLM 0.26.0 and prefix caching disabled. The study covers closed-loop concurrency, exploratory workload shapes, a fixed-32-output controlled input-length comparison, and open-loop saturation. Measurements are tied to Slentore SHA `4a573b104970d7300ebebd136548e0c679fd4f74` and remain scoped to the recorded environment and workloads.

### [A100 cross-tool client metric calibration](a100-cross-tool-calibration-20260916.md)

The same A100/Qwen3-8B family was used at C=1 with 23 input tokens, 32 output tokens, and a repeated-prompt warm-prefix-cache workload. The calibration compares compatible client-visible boundaries across Slentore, vLLM bench, NVIDIA AIPerf, and GuideLLM; it validates metric alignment for this controlled case rather than ranking tools. Measurements are tied to Slentore SHA `4a573b104970d7300ebebd136548e0c679fd4f74`.

## Historical/local learning experiments

The [`local-single-gpu-v0`](../../experiments/local-single-gpu-v0/) series contains the repository's original learning exercises and local artifacts. It is historical material, not part of the controlled A100 evidence studies above.

## Evidence policy

Raw archives remain outside Git. The repository keeps compact sanitized evidence and archive checksums; published aggregates state whether values are pooled or means of repetition summaries. Every claim remains limited to its recorded hardware, software, workload, cache state, and measurement boundary.

For a provider-neutral workflow that validates an already-provisioned GPU/vLLM environment and preserves evidence before teardown, see the [disposable GPU commissioning runbook](../runbooks/disposable-gpu-commissioning.md). Slentore does not provision GPUs.
