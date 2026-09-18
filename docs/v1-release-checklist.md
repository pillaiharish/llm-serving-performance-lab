# Slentore V1 release checklist

Slentore v1.0.0 completed this checklist against
`e9787008633b1dcb5b0836a81db743242b4f41a1`.

Release: [Slentore v1.0.0](https://github.com/pillaiharish/llm-serving-performance-lab/releases/tag/v1.0.0)

These gates remain the documented V1 release process. Complete them in order;
merging documentation, evidence, or implementation work is not permission to
tag.

## A. Repository and CI readiness

- [x] `Quality (Go 1.22.x)` and `Quality (Go 1.25.5)` pass.
- [x] `Race`, `Client calibration smoke`, and `V1 acceptance fixture` pass.
- [x] Local Go 1.25.5 tests, vet, builds, race tests, module cleanliness, and
      formatting checks pass.
- [x] Separate-process clean closed/open calibration and deliberate
      client-limited calibration have been inspected.
- [x] Calibration and acceptance artifacts contain no credential, prompt,
      generated text, raw request/SSE JSON, or token IDs.
- [x] Calibration, release, acceptance, experiment, and release-note
      documentation is reviewed with no unresolved release blocker.

The artifact consistency gate is already anchored by focused regression tests:
artifact writer tests recalculate `RequestMetrics` from observations and the
canonical `RunSummary`/JSON/CSV; experiment writer tests validate plan-to-point,
point-to-child metadata/summary, and child-summary-to-experiment-row identity.
Calibration publication also reloads its referenced experiment and schema-7
child summaries to prove report-to-point, counts, load evidence, scheduler lag,
throughput, and delivery classification identity. The V1 acceptance verifier
proves the recorded typed scenario configuration against the exact experiment
axes and ordered child-run configuration. These checks extend the evidence
chain without duplicating the complete artifact and experiment writers.

The final release commit on `main` must pass the five CI checks above. Repository
settings are outside this documentation checklist.

## B. Real-vLLM acceptance gate

Run against an already-running endpoint. This process does not provision or
manage GPU infrastructure.

```bash
export SLENTORE_BASE_URL='http://127.0.0.1:18000/v1'
export SLENTORE_MODEL='<served-model>'
export SLENTORE_TOKENIZER_URL='http://127.0.0.1:18000/tokenize'

# Optional authentication by environment-variable name:
export SLENTORE_API_KEY_ENV=VLLM_API_KEY
export VLLM_API_KEY='...'

scripts/v1_acceptance.sh
```

`SLENTORE_ACCEPTANCE_DIR` selects a new, nonexistent evidence directory.
Conservative defaults can be changed with
`SLENTORE_BASIC_REQUESTS`, `SLENTORE_BASIC_WARMUP`,
`SLENTORE_BASIC_MAX_OUTPUT_TOKENS`,
`SLENTORE_CLOSED_CONCURRENCY_VALUES`, `SLENTORE_CLOSED_REQUESTS`,
`SLENTORE_CLOSED_WARMUP`, `SLENTORE_OPEN_RATE_VALUES`,
`SLENTORE_OPEN_DURATION`, `SLENTORE_OPEN_MAX_IN_FLIGHT`,
`SLENTORE_OPEN_WARMUP`, `SLENTORE_TOKEN_INPUT_VALUES`,
`SLENTORE_TOKEN_OUTPUT_VALUES`, `SLENTORE_TOKEN_REQUESTS`, and
`SLENTORE_TOKEN_WARMUP`. Overrides are explicit tested values, not capacity
recommendations.

The script prints its unique output root. It must pass its basic direct-prompt
run, closed-loop sweep, open-loop sweep, two-input token-length matrix,
evidence-backed vLLM token-timing audit, permissive SLO/goodput audit, schema
and identity checks, and redaction scan. Any failed default child is an
acceptance failure even when its valid schema-7 evidence is retained.

Record alongside the evidence, with secrets redacted:

- exact Slentore git SHA and `slentore version` output;
- vLLM version;
- model identifier and model revision when known;
- tokenizer endpoint;
- GPU model and count;
- driver and CUDA/runtime versions when known;
- complete server launch command and relevant vLLM flags;
- client OS/architecture;
- UTC timestamp.

Remote GPU facts are manual evidence; Slentore does not pretend to discover
them. After adding the environment record, rerun
`scripts/verify_v1_acceptance.py --acceptance-dir <path>` so the redaction
audit also covers that text.

Normal GitHub CI deliberately uses only the deterministic fake server. It
requires no GPU, hosted endpoint, Hugging Face credential, or other secret and
does not replace this gate.

Historical controlled A100 evidence uses Slentore measurement SHA
`4a573b104970d7300ebebd136548e0c679fd4f74` and supports the published A100
studies. Slentore v1.0.0 separately passed real-vLLM acceptance against a
binary built from the exact release SHA
`e9787008633b1dcb5b0836a81db743242b4f41a1`. This acceptance was a functional
and integration release gate, not a performance or capacity benchmark; its RTX
A5000 environment does not establish GPU capacity.

## C. Reproducible-build and tag gate

- [x] `main` CI is green at the exact release SHA.
- [x] Real-vLLM acceptance passed against a binary built from that exact SHA.
- [x] The artifact verifier passed after the environment record was added.
- [x] The checkout is clean and the release SHA is recorded.
- [x] Reproducible binaries were built and checksummed.
- [x] The tag `v1.0.0` was created after every prior item passed.
- [x] The GitHub Release was created from the qualified tag.

## Reproducible build

From the exact clean release checkout, choose the explicit target and inject
the same revision reported by Git:

```bash
revision=$(git rev-parse HEAD)
mkdir -p dist/linux-amd64

CGO_ENABLED=0 \
GOOS=linux \
GOARCH=amd64 \
GOTOOLCHAIN=go1.25.5 \
go build \
  -trimpath \
  -buildvcs=false \
  -ldflags="-s -w -X main.version=v1.0.0 -X main.revision=$revision" \
  -o dist/linux-amd64/slentore \
  ./cmd/slentore

CGO_ENABLED=0 \
GOOS=linux \
GOARCH=amd64 \
GOTOOLCHAIN=go1.25.5 \
go build \
  -trimpath \
  -buildvcs=false \
  -ldflags="-s -w" \
  -o dist/linux-amd64/slentore-fake-server \
  ./cmd/slentore-fake-server

dist/linux-amd64/slentore version
go version -m dist/linux-amd64/slentore
shasum -a 256 dist/linux-amd64/slentore dist/linux-amd64/slentore-fake-server
```

Repeat with explicit `GOOS`/`GOARCH` values for every distributed target. Do
not tag from a dirty checkout or replace the injected revision with a branch
name.
