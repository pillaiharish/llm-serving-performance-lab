# V1 readiness

This page records repository state; it does not announce a release or authorize a tag. See the [release checklist](../v1-release-checklist.md) for the mandatory sequence.

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

Historical real-vLLM qualification exists for Slentore measurement SHA `4a573b104970d7300ebebd136548e0c679fd4f74` and supports the published [A100 evidence](../experiments/README.md). It does not satisfy the exact-release-SHA gate for current `main`.

## Remaining before v1.0.0

- [ ] Freeze the final release SHA.
- [ ] Ensure `main` CI is green at that exact SHA.
- [ ] Build the acceptance binary from that exact clean checkout.
- [ ] Run real-vLLM acceptance with that binary.
- [ ] Append the redacted environment record to the acceptance evidence.
- [ ] Rerun the artifact verifier after adding the environment record.
- [ ] Confirm the release checkout remains clean and records the release SHA.
- [ ] Build reproducible release binaries for every target and publish checksums.
- [ ] Create `v1.0.0` only after every prior gate passes.

No current-main real-vLLM acceptance or v1.0.0 release is claimed here.
