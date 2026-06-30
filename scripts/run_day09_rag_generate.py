from __future__ import annotations

import json
import os
import sys
import time
from pathlib import Path
from typing import Any

from openai import OpenAI

REPO_ROOT = Path(__file__).resolve().parents[1]
if str(REPO_ROOT) not in sys.path:
    sys.path.insert(0, str(REPO_ROOT))

from rag.simple_retriever import SimpleRetriever


MODEL_NAME = os.getenv("MODEL_NAME", "models/Qwen/Qwen2.5-0.5B-Instruct")
BASE_URL = os.getenv("BASE_URL", "http://localhost:8000/v1")

TOP_K = 3
MAX_TOKENS = 160

QUERIES = [
    "What is TTFT?",
    "How does concurrency affect throughput?",
    "Why are these benchmarks not production capacity claims?",
]


def build_context(results: list[dict[str, Any]]) -> str:
    context_blocks: list[str] = []

    for index, result in enumerate(results, start=1):
        context_blocks.append(
            "\n".join(
                [
                    f"[Context {index}]",
                    f"Title: {result['title']}",
                    f"Chunk ID: {result['chunk_id']}",
                    f"Text: {result['text']}",
                ]
            )
        )

    return "\n\n".join(context_blocks)


def build_prompt(query: str, context_text: str) -> str:
    return f"""Use only the context below to answer the question.

Rules:
- Keep the answer concise.
- Do not invent benchmark results.
- If the context is insufficient, say that the context does not contain enough information.

Context:
{context_text}

Question:
{query}
"""


def generate_streaming_answer(client: OpenAI, prompt: str) -> tuple[str, float | None, float]:
    generation_start = time.perf_counter()
    first_token_ms: float | None = None
    answer_parts: list[str] = []

    stream = client.chat.completions.create(
        model=MODEL_NAME,
        messages=[
            {
                "role": "system",
                "content": (
                    "You are a concise assistant for an LLM serving benchmark lab. "
                    "Answer only from the supplied context."
                ),
            },
            {
                "role": "user",
                "content": prompt,
            },
        ],
        temperature=0,
        max_tokens=MAX_TOKENS,
        stream=True,
    )

    for chunk in stream:
        if not chunk.choices:
            continue

        token = chunk.choices[0].delta.content or ""
        if not token:
            continue

        if first_token_ms is None:
            first_token_ms = (time.perf_counter() - generation_start) * 1000.0

        answer_parts.append(token)
        print(token, end="", flush=True)

    generation_e2e_ms = (time.perf_counter() - generation_start) * 1000.0
    answer = "".join(answer_parts).strip()

    return answer, first_token_ms, generation_e2e_ms


def main() -> None:
    corpus_path = REPO_ROOT / "rag" / "sample_corpus.md"
    output_path = REPO_ROOT / "results" / "day09" / "rag_generate_output.json"
    output_path.parent.mkdir(parents=True, exist_ok=True)

    client = OpenAI(
        base_url=BASE_URL,
        api_key="not-needed",
    )

    retriever = SimpleRetriever(corpus_path)

    run_start = time.perf_counter()
    query_records: list[dict[str, Any]] = []

    print("# Day 9 RAG Generation Smoke")
    print(f"Model: {MODEL_NAME}")
    print(f"Base URL: {BASE_URL}")
    print(f"Corpus: {corpus_path}")
    print(f"Chunks loaded: {len(retriever.chunks)}")

    for query in QUERIES:
        print("\n" + "=" * 80)
        print(f"Query: {query}")

        query_start = time.perf_counter()

        retrieval_start = time.perf_counter()
        retrieved = retriever.retrieve(query, top_k=TOP_K)
        retrieval_ms = (time.perf_counter() - retrieval_start) * 1000.0

        prompt_build_start = time.perf_counter()
        context_text = build_context(retrieved)
        prompt = build_prompt(query, context_text)
        prompt_build_ms = (time.perf_counter() - prompt_build_start) * 1000.0

        print(f"Retrieved titles: {[result['title'] for result in retrieved]}")
        print(f"Retrieval latency ms: {retrieval_ms:.3f}")
        print(f"Prompt build latency ms: {prompt_build_ms:.3f}")
        print("Answer:")

        answer, first_token_ms, generation_e2e_ms = generate_streaming_answer(
            client=client,
            prompt=prompt,
        )

        total_rag_e2e_ms = (time.perf_counter() - query_start) * 1000.0

        print("")
        print(f"Generation first-token latency ms: {first_token_ms}")
        print(f"Generation E2E latency ms: {generation_e2e_ms:.3f}")
        print(f"Total RAG E2E latency ms: {total_rag_e2e_ms:.3f}")

        query_records.append(
            {
                "query": query,
                "top_k": TOP_K,
                "retrieval_ms": round(retrieval_ms, 3),
                "prompt_build_ms": round(prompt_build_ms, 3),
                "generation_first_token_ms": (
                    round(first_token_ms, 3) if first_token_ms is not None else None
                ),
                "generation_e2e_ms": round(generation_e2e_ms, 3),
                "total_rag_e2e_ms": round(total_rag_e2e_ms, 3),
                "retrieved_chunks": [
                    {
                        "rank": rank,
                        "chunk_id": result["chunk_id"],
                        "title": result["title"],
                        "score": result["score"],
                        "matched_terms": result["matched_terms"],
                        "text": result["text"],
                    }
                    for rank, result in enumerate(retrieved, start=1)
                ],
                "answer": answer,
            }
        )

    total_run_ms = (time.perf_counter() - run_start) * 1000.0

    output = {
        "day": 9,
        "name": "basic_rag_generation_smoke",
        "model_name": MODEL_NAME,
        "base_url": BASE_URL,
        "corpus_path": str(corpus_path),
        "chunks_loaded": len(retriever.chunks),
        "total_run_ms": round(total_run_ms, 3),
        "queries": query_records,
        "notes": [
            "This is a single-request RAG generation smoke test.",
            "This is not a RAG benchmark under load.",
            "Retrieval, prompt construction, generation, and total RAG timing are separated.",
        ],
    }

    output_path.write_text(json.dumps(output, indent=2), encoding="utf-8")
    print("\n" + "=" * 80)
    print(f"Saved JSON output: {output_path}")
    print(f"Total run latency ms: {total_run_ms:.3f}")


if __name__ == "__main__":
    main()