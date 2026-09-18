# V1 readiness

This page records the completed Slentore V1 release and its qualification
boundaries. See the [release checklist](../v1-release-checklist.md) for the
process and completion record.

- Release: `v1.0.0`
- Release source SHA: `e9787008633b1dcb5b0836a81db743242b4f41a1`
- GitHub Release: [Slentore v1.0.0](https://github.com/pillaiharish/llm-serving-performance-lab/releases/tag/v1.0.0)

## Completed

- benchmark, run, experiment, and calibration schemas with aggregation;
- fixed-concurrency closed-loop and wall-clock open-loop load;
- deterministic token-length workloads and experiment sweeps;
- evidence-backed client-observed true ITL mode;
- client-delivery calibration;
- acceptance and artifact-verifier tooling;
- provider-neutral commissioning and evidence lifecycle;
- a controlled real-A100 prefix-cache-OFF study; and
- a cross-tool client metric calibration.

Historical controlled A100 measurement evidence remains tied to Slentore
measurement SHA `4a573b104970d7300ebebd136548e0c679fd4f74` and supports the
published [A100 studies](../experiments/README.md). That historical
qualification is not the v1.0.0 release qualification SHA. The v1.0.0 release
was qualified separately at `e9787008633b1dcb5b0836a81db743242b4f41a1`.

## v1.0.0 release record

- [x] The exact release SHA was frozen.
- [x] `main` CI was green at that exact SHA.
- [x] The acceptance binary was built from the exact clean checkout.
- [x] Real-vLLM acceptance passed at that exact SHA.
- [x] The redacted environment record was added.
- [x] The acceptance verifier passed after the environment record was added.
- [x] The release checkout remained clean and recorded the release SHA.
- [x] Multi-platform release binaries were independently rebuilt and matched.
- [x] Package and binary checksums were produced.
- [x] The annotated `v1.0.0` tag was created at the qualified SHA.

The real-vLLM v1.0.0 acceptance used vLLM 0.26.0, Qwen/Qwen3-8B, and
`fixture=false`. It was a functional and integration release gate, not a
performance or capacity benchmark. Its raw acceptance evidence was
intentionally kept outside the public GitHub Release.
