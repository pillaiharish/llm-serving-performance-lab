# Disposable GPU commissioning and evidence gates

This workflow starts **after** a GPU host and an OpenAI-compatible vLLM server
already exist. It answers whether the observed host, server, client, and token
contract are trustworthy enough to begin an experiment, and packages the
evidence needed before the host disappears.

It does not provision a provider, install a machine, download a model, launch
vLLM, qualify capacity, compare clients, or destroy a host. It does not change
Slentore measurement semantics.

## Choose the validation level

**Level 1 — every new GPU instance.** Run `scripts/gpu_commission.sh`. It gates
runtime GPU identity, exact Slentore identity, server configuration,
authentication, model/token/cache contracts, external NVIDIA telemetry, and a
short Slentore reference run.

**Level 2 — a replacement host continuing the same experiment.** Run Level 1
with the same predeclared contract, then compare the new reference result and
host identity with the previous host under a predeclared host-delta policy.
Record the review decision. Never silently combine different host identities.
The commissioning script intentionally has no universal latency or throughput
threshold.

**Level 3 — cross-tool metric validation.** Use a separately reviewed
cross-tool procedure only when client measurement semantics, tool versions, or
material methodology changed. Level 3 is not implemented by this script.

These levels are distinct from other gates:

- Commissioning is not capacity qualification or saturation testing.
- Commissioning is not release acceptance. When qualifying a Slentore release,
  run the existing [V1 acceptance workflow](../v1-release-checklist.md) as an
  additional explicit gate; this workflow does not reimplement it.
- Commissioning is not cross-tool metric validation.

## Three different kinds of warmup

Keep these terms separate in notes and reports:

1. **Engine/model startup warmup** happens while the server loads and performs
   its startup work. The operator must establish inference readiness before
   commissioning.
2. **Discarded benchmark-request warmup** is controlled by
   `--warmup-requests`; Slentore records these requests separately from the
   measured cohort.
3. **GPU clock/power/thermal stabilization** is a hardware-state policy. The
   commissioning script records telemetry but does not claim thermal
   equilibrium and does not implement a stabilization threshold.

## Prepare an explicit contract

Use a clean Slentore checkout at the exact revision being tested. Build outside
that checkout so the source remains clean:

```bash
export SLENTORE_REPO=/path/to/clean/slentore
export EXPECTED_SLENTORE_SHA=$(git -C "$SLENTORE_REPO" rev-parse HEAD)
export BUILD_ROOT=/path/outside/slentore/build
mkdir -p "$BUILD_ROOT"

GOTOOLCHAIN=go1.25.5 go -C "$SLENTORE_REPO" build \
  -trimpath \
  -ldflags="-X main.revision=$EXPECTED_SLENTORE_SHA" \
  -o "$BUILD_ROOT/slentore" \
  ./cmd/slentore

test -z "$(git -C "$SLENTORE_REPO" status --porcelain)"
"$BUILD_ROOT/slentore" version
```

Create an operator-captured server configuration file from the actual launch
configuration and startup evidence. It is evidence, not a marketplace claim:

```json
{
  "model": "Qwen/Qwen3-8B",
  "vllm_version": "0.26.0",
  "dtype": "bfloat16",
  "tensor_parallel_size": 1,
  "world_size": 1,
  "kv_cache_dtype": "auto",
  "max_model_len": 32768,
  "max_num_seqs": 16,
  "gpu_memory_utilization": 0.9,
  "generation_config": "vllm",
  "thinking": false,
  "prefix_caching": false
}
```

Every field is required and no additional fields are allowed. The CLI
expectations must match this file. The original operator file is never copied
into evidence; the script persists a newly generated canonical object
containing only these twelve validated fields. Credential-shaped input is
rejected before any operator-derived value is written under the evidence root.
Prefix-cache state is also checked independently from the runtime vLLM
Prometheus `vllm:cache_config_info` sample before and after the smoke run. A
launch command or configuration file alone is not accepted as cache evidence.

Set the API key by environment-variable name. Do not place the value in a
command, file, shell history, or runbook:

```bash
export VLLM_API_KEY='set-this-securely'
test -n "${VLLM_API_KEY:-}"     # verifies presence without printing it
```

The script passes `VLLM_API_KEY` as a name to Slentore, supplies HTTP
authorization through curl standard input, and scans every evidence file for
the configured value and common credential shapes. It never persists an
Authorization header.

## Run Level 1 commissioning

Choose a brand-new evidence path. Existing paths are rejected so an earlier
attempt cannot be overwritten.

```bash
export EXPERIMENT_ROOT=/path/outside/source/gpu-commissioning-20260916
export BASE_URL=http://127.0.0.1:8000/v1
export MODEL=Qwen/Qwen3-8B
export SERVER_CONFIG=/path/outside/source/server-config.json

scripts/gpu_commission.sh \
  --evidence-root "$EXPERIMENT_ROOT" \
  --slentore-repo "$SLENTORE_REPO" \
  --expected-slentore-sha "$EXPECTED_SLENTORE_SHA" \
  --slentore-bin "$BUILD_ROOT/slentore" \
  --base-url "$BASE_URL" \
  --model "$MODEL" \
  --api-key-env VLLM_API_KEY \
  --server-config "$SERVER_CONFIG" \
  --expected-vllm-version 0.26.0 \
  --expected-dtype bfloat16 \
  --expected-tensor-parallel-size 1 \
  --expected-world-size 1 \
  --expected-kv-cache-dtype auto \
  --expected-max-model-len 32768 \
  --expected-max-num-seqs 16 \
  --expected-gpu-memory-utilization 0.90 \
  --expected-generation-config vllm \
  --expected-thinking false \
  --expected-prefix-caching false \
  --expected-gpu-count 1 \
  --expected-gpu-name 'NVIDIA H100 80GB HBM3' \
  --expected-gpu-memory-mib 81559 \
  --input-tokens 512 \
  --max-output-tokens 128 \
  --requests 3 \
  --warmup-requests 1 \
  --concurrency 1 \
  --token-timing vllm
```

Defaults derive `/health`, `/version`, `/tokenize`, and `/metrics` from the
server root. The authenticated live `/version` response must match
`--expected-vllm-version`; use `--version-url` when it differs. Use
`--tokenizer-url` or `--metrics-url` when those endpoints differ.

The three `--expected-gpu-*` options are optional exact runtime assertions.
When supplied, the observed `nvidia-smi` count, canonical device name, and
memory per device must match. When omitted, GPU identity is capture-only and
the validation artifact explicitly records that no hardware match was
asserted. Provider marketplace metadata is never used. Confirm the exact name
and MiB value reported by the target host before declaring them.

Use `--expected-actual-output-tokens N` only when the experiment contract
requires every successful smoke request to report exactly `N` output tokens.
The script always records these as separate quantities:

- target input tokens;
- resolved input tokens;
- requested maximum output tokens;
- actual server-reported output tokens for every measured request.

The requested maximum is never presented as actual output. The token-length
Slentore run must resolve the exact target and every measured request must have
matching server input usage. True ITL availability and its evidence source are
retained when `--token-timing vllm` is selected; unavailability remains an
explicit recorded outcome rather than being replaced with inter-chunk latency.

## Gates performed

The script fails if any of these checks fail:

1. The expected Slentore SHA is not the checkout HEAD, the checkout is dirty,
   the binary is absent/non-executable, or `slentore version` does not identify
   the expected SHA. The binary SHA-256 is recorded.
2. Runtime environment collection cannot observe NVIDIA GPU identity, or an
   explicitly configured GPU count/name/memory contract does not match. Evidence
   includes UTC time, OS, kernel, CPU, disk, GPU count/model/memory/UUID,
   driver, power limit, Python, and available PyTorch/CUDA/vLLM/tokenizer
   package versions.
3. The supplied server configuration disagrees with the explicit expectations.
4. `/health` fails. Health alone is insufficient: authenticated `/v1/models`
   must expose the expected model and `max_model_len`, authenticated `/version`
   must match the expected live vLLM version, `/tokenize` must work, and a
   minimal authenticated generation must return valid usage.
5. Unauthenticated inference is not rejected with HTTP 401 or 403.
6. Runtime Prometheus evidence does not contain one consistent
   `vllm:cache_config_info` state matching the explicit expected prefix-cache
   value.
7. The external `nvidia-smi` collector exits early, has the wrong schema, has
   only a header, contains malformed/stale/nonmonotonic rows, has no sample
   inside the benchmark window, or cannot be stopped cleanly.
8. The short Slentore closed-loop run fails, lacks a summary, has failures,
   violates the workload/concurrency/token contract, lacks server usage, or
   does not preserve the requested token-timing state.
9. The source checkout changes during the workflow or any artifact contains
   the configured API key, an Authorization/Bearer value, or common `hf_` or
   `sk-` credential shapes.

There is deliberately no latency, throughput, temperature, power, or GPU
utilization pass threshold. This is a commissioning reference, not a universal
performance policy.

## Evidence lifecycle

During execution, evidence is written to a unique attempt:

```text
<experiment-root>/
└── attempts/<UTC-and-PID>/
    ├── contract.json
    ├── environment/
    ├── server/
    ├── smoke/
    ├── telemetry/
    └── validation.json
```

If a gate fails, the attempt stays under `attempts/`, records the failed gate
and exit code, and is never renamed as success. Inspect it, correct the cause,
and rerun with a new `--evidence-root`; do not erase the failed evidence.

On success the script copies the validated attempt into a private sibling
staging directory, adds the final passed validation and manifest, scans the
candidate again, and creates and verifies the archive/checksum under partial
names. Only then is the staged candidate atomically promoted to
`<experiment-root>/commissioning/`. The original attempt remains available to
the failure trap until promotion and final renames have succeeded. A manifest,
archive, checksum, or promotion failure therefore cannot leave a canonical
directory claiming success.

`manifest.json` records each included file's relative path, size, and SHA-256.
The script creates:

```text
<experiment-root>.tar.gz
<experiment-root>.tar.gz.sha256
```

The archive is host-local raw operational evidence. The secret scan makes it
credential-safe under the defined checks, but it is not a sanitized public
report and may contain endpoint, model, host, and request-derived metadata.
Inspect and curate separately before publication.

SHA-256 generation and verification use Python's standard-library `hashlib`,
not a host-specific `shasum` executable. The sidecar uses conventional
`<hex><two spaces><filename>` formatting accepted by `sha256sum -c`.

## Copy-off and teardown gate

The script intentionally does not copy files, call a provider API, or destroy
the machine. From a trusted local machine, copy both files without embedding a
provider-specific address in repository automation:

```bash
scp '<user>@<host>:<remote-path>/<experiment>.tar.gz' .
scp '<user>@<host>:<remote-path>/<experiment>.tar.gz.sha256' .
sha256sum -c '<experiment>.tar.gz.sha256'
tar -tzf '<experiment>.tar.gz'
```

Do not destroy the disposable host until every item is true:

- [ ] Level 1 commissioning status is `passed`.
- [ ] Runtime GPU/environment identity is present.
- [ ] Exact Slentore revision, version, clean-checkout result, and binary hash are present.
- [ ] Server configuration and authenticated readiness evidence are present.
- [ ] Prefix-cache runtime evidence exists before and after the reference run.
- [ ] NVIDIA telemetry contains valid samples and passed validation.
- [ ] Slentore smoke artifacts, summary, token usage, and ITL availability are present.
- [ ] Redaction scan passed.
- [ ] Manifest exists and enumerates the evidence.
- [ ] Archive and archive SHA-256 exist on the disposable host.
- [ ] Archive and checksum were copied off the disposable host.
- [ ] Local archive SHA-256 matches the copied checksum.
- [ ] Local archive contents were listed and inspected.
- [ ] Any experiment-specific evidence beyond commissioning was separately captured and verified.

Only after all applicable boxes are checked may the GPU host be destroyed.
