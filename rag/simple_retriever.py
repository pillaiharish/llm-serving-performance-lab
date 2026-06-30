from __future__ import annotations

import math
import re
from collections import Counter, defaultdict
from dataclasses import dataclass
from pathlib import Path
from typing import Iterable


STOPWORDS = {
    "a", "an", "and", "are", "as", "at", "be", "by", "for", "from",
    "how", "in", "is", "it", "of", "on", "or", "the", "these", "this",
    "to", "what", "why", "with",
}


@dataclass(frozen=True)
class Chunk:
    chunk_id: str
    title: str
    text: str
    tokens: tuple[str, ...]


def tokenize(text: str) -> list[str]:
    """Tokenize text using only Python standard library."""
    tokens = re.findall(r"[a-z0-9]+", text.lower())
    return [token for token in tokens if token not in STOPWORDS]


def split_markdown_sections(markdown_text: str) -> list[tuple[str, str]]:
    """
    Split a small markdown document into heading-based sections.

    This intentionally keeps the logic simple, each heading is a single chunk.
    """
    sections: list[tuple[str, str]] = []
    current_title: str | None = None
    current_lines: list[str] = []

    for line in markdown_text.splitlines():
        stripped = line.strip()

        if stripped.startswith("## "):
            if current_title and current_lines:
                sections.append((current_title, "\n".join(current_lines).strip()))

            current_title = stripped.removeprefix("## ").strip()
            current_lines = []
            continue

        if stripped.startswith("# "):
            continue

        if current_title:
            current_lines.append(line)

    if current_title and current_lines:
        sections.append((current_title, "\n".join(current_lines).strip()))

    return [(title, body) for title, body in sections if body]


class SimpleRetriever:
    """
    Tiny local keyword/BM25-like retriever.

    This is not production retrieval.
    It exists to make retrieval latency measurable before adding embeddings,
    vector databases, rerankers, or generation calls.
    """

    def __init__(self, corpus_path: str | Path):
        self.corpus_path = Path(corpus_path)
        self.chunks = self._load_chunks(self.corpus_path)
        self.document_frequency = self._build_document_frequency(self.chunks)

    def _load_chunks(self, corpus_path: Path) -> list[Chunk]:
        markdown_text = corpus_path.read_text(encoding="utf-8")
        sections = split_markdown_sections(markdown_text)

        chunks: list[Chunk] = []
        for index, (title, text) in enumerate(sections, start=1):
            combined_text = f"{title}\n{text}"
            chunks.append(
                Chunk(
                    chunk_id=f"chunk-{index:03d}",
                    title=title,
                    text=text,
                    tokens=tuple(tokenize(combined_text)),
                )
            )

        if not chunks:
            raise ValueError(f"No chunks found in corpus: {corpus_path}")

        return chunks

    def _build_document_frequency(self, chunks: Iterable[Chunk]) -> dict[str, int]:
        document_frequency: dict[str, int] = defaultdict(int)

        for chunk in chunks:
            for token in set(chunk.tokens):
                document_frequency[token] += 1

        return dict(document_frequency)

    def _idf(self, token: str) -> float:
        total_docs = len(self.chunks)
        docs_with_token = self.document_frequency.get(token, 0)

        return math.log((1 + total_docs) / (1 + docs_with_token)) + 1.0

    def _score_chunk(self, query_tokens: list[str], chunk: Chunk) -> tuple[float, list[str]]:
        if not query_tokens:
            return 0.0, []

        chunk_token_counts = Counter(chunk.tokens)
        matched_terms: list[str] = []
        score = 0.0

        for token in query_tokens:
            term_frequency = chunk_token_counts.get(token, 0)
            if term_frequency <= 0:
                continue

            matched_terms.append(token)
            score += (1.0 + math.log(term_frequency)) * self._idf(token)

        length_normalizer = 1.0 + (len(chunk.tokens) / 100.0)
        score = score / length_normalizer

        title_tokens = set(tokenize(chunk.title))
        title_matches = set(query_tokens).intersection(title_tokens)
        if title_matches:
            score += 1.5 * len(title_matches)

        return score, sorted(set(matched_terms))

    def retrieve(self, query: str, top_k: int = 3) -> list[dict[str, object]]:
        query_tokens = tokenize(query)

        scored_results: list[dict[str, object]] = []
        for chunk in self.chunks:
            score, matched_terms = self._score_chunk(query_tokens, chunk)

            if score <= 0:
                continue

            scored_results.append(
                {
                    "chunk_id": chunk.chunk_id,
                    "title": chunk.title,
                    "score": round(score, 4),
                    "matched_terms": matched_terms,
                    "text": chunk.text,
                }
            )

        scored_results.sort(key=lambda item: item["score"], reverse=True)
        return scored_results[:top_k]


if __name__ == "__main__":
    repo_root = Path(__file__).resolve().parents[1]
    retriever = SimpleRetriever(repo_root / "rag" / "sample_corpus.md")

    for result in retriever.retrieve("What is TTFT?", top_k=3):
        print(result)