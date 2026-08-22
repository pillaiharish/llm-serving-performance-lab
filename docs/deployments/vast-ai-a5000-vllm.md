# Vast.ai RTX A5000 vLLM deployment

This guide reproduces the environment used for Slentore's first live
GPU-backed validation. It describes one tested configuration, not a universally
optimal Vast.ai or vLLM deployment.

## Tested environment

| Component | Value |
| --- | --- |
| Provider | Vast.ai |
| GPU | 1x NVIDIA RTX A5000 |
| VRAM | 24564 MiB (approximately 24 GB) |
| NVIDIA driver | 590.48.01 |
| CUDA reported by `nvidia-smi` | 13.1 |
| vLLM | 0.26.0 |
| Model | `Qwen/Qwen3.5-4B` |
| dtype | `bfloat16` |
| Tensor parallel size | 1 |
| Maximum model length | 4096 |
| GPU memory utilization | 0.85 |
| Checkpoint size reported by vLLM | 8.68 GiB |
| Model loading memory reported by vLLM | 8.61 GiB |

The Vast template was named `slentore-vllm-qwen35-4b`. The instance used
approximately 50 GB of container storage, On-Demand pricing, and Secure Cloud.
Availability, pricing, driver versions, and suitable memory settings vary by
host and over time.

## Configure the vLLM internal port

The first supervised startup used:

```text
VLLM_MODEL=Qwen/Qwen3.5-4B
VLLM_ARGS=--max-model-len 4096 --gpu-memory-utilization 0.85
```

That left vLLM on its default port 8000. Vast's portal/Caddy process already
owned that port, so vLLM failed with:

```text
OSError: [Errno 98] Address already in use
```

The portal mapping expected the vLLM API to use internal port 18000 while the
portal exposed its own external port 8000:

```yaml
vLLM API:
  hostname: localhost
  external_port: 8000
  internal_port: 18000
  open_path: /docs
  name: vLLM API
```

Make the internal listener explicit in the reusable template:

```text
VLLM_MODEL=Qwen/Qwen3.5-4B
VLLM_ARGS=--host 127.0.0.1 --port 18000 --max-model-len 4096 --gpu-memory-utilization 0.85
```

This loopback listener is also the endpoint used by the SSH tunnel and by
server-local Slentore runs.

## Diagnose and start vLLM

Check the GPU, installed vLLM version, executable, and port before launching:

```bash
nvidia-smi
python -c "import vllm; print(vllm.__version__)"
which vllm
ss -lntp | grep 18000
```

The working manual launch used while diagnosing the template was:

```bash
vllm serve Qwen/Qwen3.5-4B \
  --host 127.0.0.1 \
  --port 18000 \
  --max-model-len 4096 \
  --gpu-memory-utilization 0.85
```

Qwen deployments may optionally disable thinking mode with:

```text
--default-chat-template-kwargs '{"enable_thinking": false}'
```

That flag changes Qwen's model-serving behavior. It is not required by
Slentore or by the generic OpenAI-compatible streaming protocol.

## Token-length workload preflight

Slentore token-length mode uses vLLM 0.26.0's root-level `/tokenize` endpoint,
not the OpenAI `/v1` API root. With this deployment the two URLs are:

```text
chat API root:  http://127.0.0.1:18000/v1
tokenizer URL:  http://127.0.0.1:18000/tokenize
```

The adapter sends a single user chat message with the generation prompt
enabled and no template override. vLLM therefore applies the tokenizer, chat
template, and default template kwargs configured for the served model. If the
deployment uses `--default-chat-template-kwargs`, including Qwen's optional
`enable_thinking` setting, `/tokenize` and chat completions see the same server
default.

`/tokenizer_info` is not required and need not be enabled. Slentore records a
behavioral fingerprint of safe tokenizer probes instead; this proves the
observed preflight behavior but is not a vocabulary or Hugging Face revision
hash. A changed deployment at the same URL can therefore change token counts.

Use [`configs/token-length.example.yaml`](../../configs/token-length.example.yaml)
as the generic starting point. The target is the full rendered chat input,
and the server-reported `max_model_len` is checked against target input plus
the requested output maximum before lifecycle timing starts.

After vLLM is ready, use an environment variable rather than embedding a
credential in a command or file:

```bash
curl -sS \
  -H "Authorization: Bearer ${VAST_VLLM_TOKEN}" \
  http://127.0.0.1:18000/v1/models
```

A minimal streaming smoke test is:

```bash
curl -N -sS \
  -H "Authorization: Bearer ${VAST_VLLM_TOKEN}" \
  -H "Content-Type: application/json" \
  --data '{
    "model": "Qwen/Qwen3.5-4B",
    "messages": [{"role": "user", "content": "Explain KV cache briefly."}],
    "max_tokens": 16,
    "temperature": 0,
    "stream": true,
    "stream_options": {"include_usage": true}
  }' \
  http://127.0.0.1:18000/v1/chat/completions
```

Treat the generated output as transient smoke-test output. Do not copy it into
the repository or terminal logs intended for publication.

## Request evidence-backed token timing

The documented vLLM 0.26.0 Chat Completions protocol accepts the
`return_token_ids` request extension. In a streaming response, top-level
`prompt_token_ids` belongs to the prompt, while each choice may contain a
`token_ids` list for the generated delta. Slentore requests that extension with:

```bash
go run ./cmd/slentore bench \
  --config configs/vast-a5000-qwen35.example.yaml \
  --token-timing vllm
```

This remains the normal HTTP/SSE `/v1/chat/completions` endpoint; no gRPC or
special streaming endpoint is required. Slentore does not require logprobs and
does not send a `stream_interval` field.

Token timing is validated per request rather than assumed from the server
version. Slentore requires clean completion through `[DONE]`, matching server
usage coverage, at least two output tokens, and exactly one generated token ID
on every token-bearing event. If speculative decoding, batching, a parser, or
another server behavior returns multiple generated IDs in one event, the
client can observe only the shared event receipt time. True ITL is therefore
unavailable for that request; Slentore does not divide the gap by token count.
ICL, TTFT, TPOT, and other normally supported request metrics remain separate
and usable where their own evidence is valid.

The resulting ITL is client-observed token-arrival latency across the complete
network path to Slentore. It is not an internal GPU decode-kernel or
server-scheduler duration. Artifacts retain only token-ID field presence,
generated-token cardinality, relative receive offsets, source, and validity;
prompt and generated numeric token IDs are discarded.

## Connect through an SSH local forward

The reliable remote-client path was an SSH tunnel with placeholders for every
ephemeral instance detail:

```bash
ssh -N -T \
  -o ExitOnForwardFailure=yes \
  -o ServerAliveInterval=30 \
  -o ServerAliveCountMax=3 \
  -p <VAST_SSH_PORT> \
  -i <SSH_KEY_PATH> \
  root@<VAST_HOST> \
  -L 18000:127.0.0.1:18000
```

The measured path was:

```text
Mac / Slentore
      |
      | SSH encrypted tunnel
      v
Vast instance
      |
      | localhost
      v
vLLM :18000
      |
      v
Qwen/Qwen3.5-4B
      |
      v
RTX A5000
```

With the tunnel running, Slentore uses the OpenAI API root
`http://127.0.0.1:18000/v1`:

```bash
export VAST_VLLM_TOKEN='...'
go run ./cmd/slentore bench \
  --config configs/vast-a5000-qwen35.example.yaml
```

The Vast public Caddy mapping returned `401 Unauthorized` and introduced its
own authentication layer in this particular instance. Its mapped HTTPS route
was therefore not used for the benchmark. Do not compensate by weakening TLS
certificate verification in Slentore; use a correctly configured HTTPS route
or the SSH forward.

## Run Slentore on the server

For the server-local comparison, cross-compile Slentore without committing the
binary:

```bash
GOOS=linux GOARCH=amd64 go build \
  -o build/slentore-linux-amd64 \
  ./cmd/slentore
```

Copy it using only instance-specific values supplied at invocation time:

```bash
scp \
  -P <VAST_SSH_PORT> \
  -i <SSH_KEY_PATH> \
  build/slentore-linux-amd64 \
  root@<VAST_HOST>:/workspace/slentore
```

On the Vast instance, run against the same loopback API root:

```bash
export VAST_VLLM_TOKEN='...'
/workspace/slentore bench \
  --config /workspace/vast-a5000-qwen35.example.yaml \
  --base-url http://127.0.0.1:18000/v1 \
  --output-dir /workspace/runs/vast-a5000-local
```

The remote-client run includes the Mac, Internet path, and SSH forwarding. The
server-local run removes that path and more closely observes the serving
process, but neither mode is a capacity or concurrency benchmark.

## macOS `LC_UUID` troubleshooting

The original Mac toolchain reported `go1.22.2 darwin/arm64`. Its linked binary
failed before Slentore started:

```text
dyld: missing LC_UUID load command
```

Useful diagnostics are:

```bash
go version
file build/slentore
otool -l build/slentore | grep -A3 LC_UUID
```

After installing Homebrew Go 1.26.6 and rebuilding, the executable contained
an `LC_UUID` load command and ran normally. This was a local linker/toolchain
issue, not a reason to raise Slentore's declared minimum Go version from 1.22.

## Security and cleanup

- Keep API keys in environment variables and SSH keys outside the repository.
- Do not save public instance addresses, mapped ports, portal passwords, or
  terminal output containing credentials.
- Do not persist prompts or generated text in benchmark artifacts. Slentore's
  standard artifacts retain only prompt length/hash and generated byte counts.
- Stop the tunnel and terminate the paid instance when validation is complete.
