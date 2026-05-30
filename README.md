# llm-inference-prod

Production-grade LLM inference engineering lab for benchmarking, monitoring, optimizing, and operating local and cloud LLM workloads.

## Why this project exists

Most GenAI prototypes work in demos but fail in production because of high latency, unpredictable GPU cost, weak observability, poor model-serving decisions, and lack of benchmark-driven deployment planning.

`llm-inference-prod` is a hands-on engineering lab focused on the production side of LLM systems:

- How fast does a model respond?
- How much GPU memory does it consume?
- What is the real cost per 1M tokens?
- Which quantization method is usable without destroying quality?
- How do we monitor LLM inference like a production service?
- How do we compare vLLM, SGLang, TensorRT-LLM, and other serving stacks?
- How do we make startup GenAI systems cheaper, faster, and more reliable?

The goal is to build practical, reproducible experiments that help founders, AI teams, and infra engineers make better deployment decisions.

## Day 1 note

Day 1 documents a low-memory `vllm` serving profile for an RTX 5070 Ti 16 GB machine and validates a local OpenAI-compatible API server.

It also captures troubleshooting notes for:

- `torch.OutOfMemoryError` during `vllm` startup
- `curl -s` returning no output because the API server did not bind to the port
- background GPU consumers from `systemd`, Docker services, and other model servers
- FlashInfer sampler compatibility issues on RTX 50-series / SM 12.x GPUs

See:

- [Day 1 README](days/day01-vllm-setup-first-benchmark/README.md)
- [Day 1 Tasks](days/day01-vllm-setup-first-benchmark/TASKS.md)
- [Day 1 Learner Appendix](docs/day-one-vllm-openai-server.md)
