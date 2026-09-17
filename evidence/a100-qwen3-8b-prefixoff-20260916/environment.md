# Controlled environment

The archived commissioning contract, runtime validation, vLLM startup lines,
Prometheus configuration metadata, and benchmark run records agree on the
following environment.

| Component | Qualified value |
| --- | --- |
| GPU | 1 × NVIDIA A100-SXM4-40GB |
| GPU UUID | `GPU-c757cde4-7971-d84f-7ba0-b6f48a1bda1c` |
| GPU memory | 40,960 MiB |
| GPU power limit | 400 W |
| NVIDIA driver | 580.105.08 |
| vLLM | 0.26.0, V1 engine |
| Model | `Qwen/Qwen3-8B` |
| Model snapshot | `b968826d9c46dd6066d109eabc6255188de91218` |
| Weight dtype | bfloat16 |
| KV cache dtype | auto |
| Tensor parallel / world size | 1 / 1 |
| Maximum model length | 32,768 tokens |
| Maximum sequences | 16 |
| GPU memory utilization | 0.90 |
| Generation configuration | vLLM |
| Thinking | disabled |
| Prefix caching | disabled |
| Slentore measurement SHA | `4a573b104970d7300ebebd136548e0c679fd4f74` |
| Slentore version string | `v0.0.0-20260830170145-4a573b104970` |

The open-loop archive records the Hugging Face `main` ref and the sole cached
snapshot as the model snapshot shown above. Earlier phase archives do not each
contain a separate model-revision file. The startup record reports
`revision=None`, so the recovered cache evidence—not a user-pinned launch
argument—is the revision provenance.

Prefix caching was checked before and after commissioning and was `false` in
both cases. The archived vLLM startup record says
`enable_prefix_caching=False`, and Prometheus `vllm:cache_config_info` reports
`enable_prefix_caching="False"`. This is configuration evidence; the prefix
cache counters remaining zero are consistent with it but are not the primary
identity check.
