from __future__ import annotations

import time
from pathlib import Path

from rag.simple_retriever import SimpleRetriever


QUERIES = [
    "What is TTFT?",
    "How does concurrency affect throughput?",
    "Why are these benchmarks not production capacity claims?",
]


def main() -> None:
    repo_root = Path(__file__).resolve().parents[1]
    corpus_path = repo_root / "rag" / "sample_corpus.md"
    output_path = repo_root / "results" / "day08" / "rag_smoke_output.txt"

    output_path.parent.mkdir(parents=True, exist_ok=True)

    smoke_start = time.perf_counter()
    retriever = SimpleRetriever(corpus_path)

    lines: list[str] = []
    lines.append("# Day 8 RAG Smoke Output")
    lines.append("")
    lines.append(f"Corpus: {corpus_path}")
    lines.append(f"Chunks loaded: {len(retriever.chunks)}")
    lines.append("")

    for query in QUERIES:
        retrieval_start = time.perf_counter()
        results = retriever.retrieve(query, top_k=3)
        retrieval_ms = (time.perf_counter() - retrieval_start) * 1000.0

        lines.append("=" * 80)
        lines.append(f"Query: {query}")
        lines.append(f"Retrieval latency ms: {retrieval_ms:.3f}")
        lines.append(f"Top-k results: {len(results)}")
        lines.append("")

        for rank, result in enumerate(results, start=1):
            lines.append(f"Rank: {rank}")
            lines.append(f"Chunk ID: {result['chunk_id']}")
            lines.append(f"Title: {result['title']}")
            lines.append(f"Score: {result['score']}")
            lines.append(f"Matched terms: {', '.join(result['matched_terms'])}")
            lines.append("Text:")
            lines.append(str(result["text"]))
            lines.append("")

    total_ms = (time.perf_counter() - smoke_start) * 1000.0
    lines.append("=" * 80)
    lines.append(f"Total smoke-test runtime ms: {total_ms:.3f}")
    lines.append("")

    output = "\n".join(lines)
    output_path.write_text(output, encoding="utf-8")

    print(output)


if __name__ == "__main__":
    main()