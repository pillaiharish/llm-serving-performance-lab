# Evidence audit

## Outer archives

All five local archives matched their external SHA-256 sidecars. Exact
digests are in `archive-checksums.txt`. The archives were read and extracted
outside the repository; the source archives were not modified.

Every member name was checked before extraction. No archive contained an
absolute path, `..` traversal, symbolic link, or hard link. Member counts were
73 (commissioning), 19,664 (concurrency), 33,753 (shapes), 20,166 (fixed-32
prefill), and 37,678 (open-loop).

## Internal manifests

| Evidence set | Entries | Matching | Missing | Digest mismatch |
| --- | ---: | ---: | ---: | ---: |
| Commissioning | 54 | 54 | 0 | 0 |
| Concurrency | 13,070 | 13,069 | 0 | 1 |
| Workload shapes | 22,425 | 22,425 | 0 | 0 |
| Fixed-32 prefill | 13,404 | 13,404 | 0 | 0 |
| Open-loop | 25,075 | 25,075 | 0 | 0 |

The original concurrency evidence package contains a known
manifest-construction defect: its generated SHA-256 manifest included an entry
for itself. An immutable file cannot contain its own valid final digest under
that construction. All 13,069 other manifested files verify successfully, and
the immutable archive matches its external SHA-256 sidecar. The original
archive was preserved unchanged and the benchmark was not rerun.

## Experiment identity and completeness

Across 97 archived `run.json` files, every record used schema 7, model and
tokenizer model `Qwen/Qwen3-8B`, and Slentore version
`v0.0.0-20260830170145-4a573b104970`. The archived full revision is
`4a573b104970d7300ebebd136548e0c679fd4f74`.

The curated matrices contain 18 canonical concurrency runs, 30 canonical shape
runs, 18 fixed-32 prefill runs, and 24 open-loop runs. All closed-loop canonical
runs completed with 320/320 successful measured requests and zero failures.
All open-loop requests that started completed successfully. The six 14/16
req/s open-loop child runs have a nonzero run status because planned arrivals
were client-limited; this is expected experimental evidence, not a missing or
failed request execution.

True ITL is sourced from `vllm_return_token_ids_client_receive`. It is a
client-visible token-identity interval, not GPU kernel decode latency. Coverage
is complete in the shape, fixed-32, and open-loop matrices. In the concurrency
matrix it is 958/960 at C1, 944/960 at C32, and 960/960 at C2–C16; unavailable
requests remain explicitly counted rather than imputed.

## Credential and publication scan

A byte-level scan of the extracted archives looked for case-insensitive
`Authorization:` headers plus bearer-token, `hf_...`, and `sk-...` patterns.
Three shell scripts contain the literal template
`Authorization: Bearer $VLLM_API_KEY`; manual inspection confirmed that no
value is embedded. No bearer value, Hugging Face token, or `sk-` key matched.
This documents the specific pattern scan performed and is not a universal
proof that arbitrary sensitive content could not exist.

Raw request observations contain prompts and generated content. They are
intentionally excluded, as are server addresses, logs, raw metrics, and raw
telemetry. Only the compact files in this directory are intended for Git.

## Model revision provenance

The open-loop environment evidence records Hugging Face `main` and the sole
cached snapshot as `b968826d9c46dd6066d109eabc6255188de91218`. The earlier
phase archives do not independently repeat this file, and the launch used
`revision=None`; this provenance boundary is retained rather than implying the
revision was explicitly pinned on the vLLM command line.
