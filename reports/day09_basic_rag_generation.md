# Day 9 Basic RAG Generation Smoke

## Summary

Day 9 connects the local Day 8 retriever to local vLLM generation through the OpenAI-compatible API.

This is a single-request RAG generation smoke test. It is not a RAG benchmark under load.

## Goal

The goal is to separate retrieval, prompt construction, generation first-token latency, generation end-to-end latency, and total RAG latency before adding concurrency later.

## What was added

- `scripts/run_day09_rag_generate.py`
- `results/day09/rag_generate_output.json`
- `reports/day09_basic_rag_generation.md`

## What this test does

This test:

- loads the local markdown corpus
- retrieves top-k chunks using the Day 8 simple retriever
- builds a grounded prompt from retrieved chunks
- calls the local vLLM OpenAI-compatible endpoint
- streams the answer
- captures timing for retrieval, prompt construction, generation, and total RAG latency
- saves structured JSON output

## What this test does not do yet

This test does not include:

- concurrent RAG requests
- load testing
- embeddings
- vector databases
- rerankers
- LangChain
- LlamaIndex
- production API serving
- UI
- correctness evaluation

## Timing fields captured

For each query, the output captures:

- `retrieval_ms`
- `prompt_build_ms`
- `generation_first_token_ms`
- `generation_e2e_ms`
- `total_rag_e2e_ms`

## Smoke test

Run vLLM in one terminal:

```bash
source .venv/bin/activate
source configs/vllm_default.env

vllm serve "$MODEL_NAME" \
  --host "$HOST" \
  --port "$PORT" \
  --max-model-len "$MAX_MODEL_LEN" \
  --gpu-memory-utilization "$GPU_MEMORY_UTILIZATION"
```

## Run the Day 9 smoke test in another terminal:

```bash
source .venv/bin/activate
source configs/vllm_default.env

python -m py_compile rag/simple_retriever.py scripts/run_day09_rag_generate.py
python scripts/run_day09_rag_generate.py
python -m json.tool results/day09/rag_generate_output.json > /dev/null
```

## Observations

The test verifies that retrieval and generation can be connected in one local RAG path.

The generated answers are saved along with retrieved chunks and timing fields.

## Why this matters

RAG serving latency is not only model generation latency.

A RAG request includes:
```text
request
→ retrieval
→ prompt construction
→ generation
→ response
```

Day 9 makes these stages visible before load testing is added.

## Next steps

Future work can add:

- small RAG evaluation set
- retrieval hit/miss checks
- answer grounding checks
- repeated runs
- RAG under concurrency
- vector search
- reranking

# Run validation

Make sure vLLM is running in Terminal 1.

Then in Terminal 2:

```bash
cd llm-serving-performance-lab

source .venv/bin/activate
source configs/vllm_default.env

python -m py_compile rag/simple_retriever.py scripts/run_day09_rag_generate.py
python scripts/run_day09_rag_generate.py
python -m json.tool results/day09/rag_generate_output.json > /dev/null
```