#!/usr/bin/env python3
"""Validation helpers for gpu_commission.sh using only the standard library."""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import hashlib
import json
import os
from pathlib import Path
import re
import sys
from typing import Any


ITL_SOURCE = "vllm_return_token_ids_client_receive"
SECRET_PATTERNS = (
    re.compile(r"(?i)authorization\s*[:=]\s*bearer\s+\S+"),
    re.compile(r"\bhf_[A-Za-z0-9]{12,}\b"),
    re.compile(r"\bsk-[A-Za-z0-9_-]{12,}\b"),
)
TELEMETRY_HEADER = [
    "timestamp", "gpu_index", "gpu_uuid", "gpu_name", "memory_used_mib",
    "memory_total_mib", "utilization_gpu_percent", "power_draw_w",
    "power_limit_w", "temperature_c", "sm_clock_mhz",
]


class ValidationError(Exception):
    pass


def load_json(path: Path) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as exc:
        raise ValidationError(f"cannot read JSON {path}: {exc}") from exc
    if not isinstance(value, dict):
        raise ValidationError(f"{path} must contain a JSON object")
    return value


def write_json(path: Path, value: Any) -> None:
    path.parent.mkdir(parents=True, exist_ok=True)
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n", encoding="utf-8")
    temporary.replace(path)


def validate_models(value: dict[str, Any], model: str, max_model_len: int | None) -> dict[str, Any]:
    data = value.get("data")
    if not isinstance(data, list):
        raise ValidationError("/v1/models response has no data array")
    matches = [item for item in data if isinstance(item, dict) and item.get("id") == model]
    if not matches:
        raise ValidationError(f"expected model {model!r} is absent from /v1/models")
    observed = matches[0]
    length = observed.get("max_model_len", observed.get("max_model_length"))
    if max_model_len is not None:
        if isinstance(length, bool) or not isinstance(length, int):
            raise ValidationError("/v1/models does not expose max_model_len")
        if length != max_model_len:
            raise ValidationError(f"max_model_len is {length}, expected {max_model_len}")
    return {"model": model, "max_model_len": length, "authenticated": True}


def validate_tokenize(value: dict[str, Any], expected: int | None) -> dict[str, Any]:
    count = value.get("count")
    tokens = value.get("tokens")
    if isinstance(count, bool) or not isinstance(count, int):
        raise ValidationError("/tokenize response has no integer count")
    if not isinstance(tokens, list) or len(tokens) != count:
        raise ValidationError("/tokenize tokens do not match count")
    if expected is not None and count != expected:
        raise ValidationError(f"tokenizer resolved {count} input tokens, expected {expected}")
    return {"probe_expected_tokens": expected, "probe_resolved_tokens": count}


def validate_generation(
    value: dict[str, Any], model: str, expected_input: int | None, expected_output: int | None,
) -> dict[str, Any]:
    choices = value.get("choices")
    if not isinstance(choices, list) or not choices or not isinstance(choices[0], dict):
        raise ValidationError("minimal generation returned no choice")
    if value.get("model") not in (None, model):
        raise ValidationError(f"minimal generation model is {value.get('model')!r}, expected {model!r}")
    usage = value.get("usage")
    if not isinstance(usage, dict):
        raise ValidationError("minimal generation returned no usage object")
    input_tokens = usage.get("prompt_tokens")
    output_tokens = usage.get("completion_tokens")
    if any(isinstance(item, bool) or not isinstance(item, int) or item < 0 for item in (input_tokens, output_tokens)):
        raise ValidationError("minimal generation usage token counts are invalid")
    if expected_input is not None and input_tokens != expected_input:
        raise ValidationError(f"generation reported {input_tokens} input tokens, expected {expected_input}")
    if expected_output is not None and output_tokens != expected_output:
        raise ValidationError(f"generation reported {output_tokens} output tokens, expected {expected_output}")
    return {"model": model, "input_tokens": input_tokens, "actual_output_tokens": output_tokens}


def validate_server_config(value: dict[str, Any], expected: dict[str, Any]) -> dict[str, Any]:
    required = {
        "model", "vllm_version", "dtype", "tensor_parallel_size", "world_size",
        "kv_cache_dtype", "max_model_len", "max_num_seqs", "gpu_memory_utilization",
        "generation_config", "thinking", "prefix_caching",
    }
    missing = sorted(required - value.keys())
    if missing:
        raise ValidationError(f"server configuration is missing fields: {', '.join(missing)}")
    unknown = sorted(value.keys() - required)
    if unknown:
        raise ValidationError(f"server configuration has unsupported fields: {', '.join(unknown)}")
    for key, wanted in expected.items():
        observed = value.get(key)
        if key == "gpu_memory_utilization":
            if isinstance(observed, bool) or not isinstance(observed, (int, float)) or abs(float(observed) - float(wanted)) > 1e-9:
                raise ValidationError(f"server configuration {key} does not match the expected value")
        elif observed != wanted:
            raise ValidationError(f"server configuration {key} does not match the expected value")
    return {key: value[key] for key in sorted(required)}


def validate_version(value: dict[str, Any], expected: str) -> dict[str, Any]:
    if set(value) != {"version"} or not isinstance(value.get("version"), str) or not value["version"]:
        raise ValidationError("/version response must contain only a nonempty version string")
    if value["version"] != expected:
        raise ValidationError(f"live vLLM version is {value['version']!r}, expected {expected!r}")
    return {"expected_vllm_version": expected, "observed_vllm_version": value["version"]}


def validate_gpu(
    path: Path, expected_count: int | None, expected_name: str | None, expected_memory_mib: int | None,
) -> dict[str, Any]:
    expected_header = ["gpu_index", "gpu_name", "memory_total_mib", "gpu_uuid", "driver_version", "power_limit_w"]
    try:
        with path.open(newline="", encoding="utf-8") as handle:
            rows = list(csv.reader(handle))
    except OSError as exc:
        raise ValidationError(f"cannot read GPU identity {path}: {exc}") from exc
    if not rows or [cell.strip() for cell in rows[0]] != expected_header:
        raise ValidationError("GPU identity header/schema is invalid")
    observed = []
    for number, row in enumerate(rows[1:], 2):
        if len(row) != len(expected_header):
            raise ValidationError(f"GPU identity row {number} has {len(row)} fields, expected {len(expected_header)}")
        row = [cell.strip() for cell in row]
        try:
            index = int(row[0]); memory = int(row[2]); power = float(row[5])
        except ValueError as exc:
            raise ValidationError(f"GPU identity row {number} has invalid numeric evidence") from exc
        if not row[1] or not row[3] or not row[4] or memory <= 0 or power <= 0:
            raise ValidationError(f"GPU identity row {number} is incomplete")
        observed.append({"gpu_index": index, "gpu_name": row[1], "memory_total_mib": memory,
                         "gpu_uuid": row[3], "driver_version": row[4], "power_limit_w": power})
    if not observed:
        raise ValidationError("GPU identity contains no devices")
    if expected_count is not None and len(observed) != expected_count:
        raise ValidationError(f"GPU count is {len(observed)}, expected {expected_count}")
    if expected_name is not None and any(item["gpu_name"] != expected_name for item in observed):
        raise ValidationError(f"GPU name does not exactly match {expected_name!r}")
    if expected_memory_mib is not None and any(item["memory_total_mib"] != expected_memory_mib for item in observed):
        raise ValidationError(f"GPU memory does not exactly match {expected_memory_mib} MiB")
    return {
        "expected": {"gpu_count": expected_count, "gpu_name": expected_name, "gpu_memory_mib": expected_memory_mib},
        "observed": {"gpu_count": len(observed), "gpus": observed},
        "identity_asserted": any(value is not None for value in (expected_count, expected_name, expected_memory_mib)),
    }


def prometheus_labels(line: str) -> dict[str, str]:
    match = re.search(r"vllm:cache_config_info\{([^}]*)\}", line)
    if not match:
        return {}
    return dict(re.findall(r'(\w+)="((?:[^"\\]|\\.)*)"', match.group(1)))


def validate_metrics(text: str, expected_prefix: bool) -> dict[str, Any]:
    candidates = [prometheus_labels(line) for line in text.splitlines() if "vllm:cache_config_info{" in line]
    candidates = [labels for labels in candidates if labels]
    if not candidates:
        raise ValidationError("Prometheus evidence has no vllm:cache_config_info sample")
    values = {labels.get("enable_prefix_caching", "").lower() for labels in candidates}
    if values == {"true"}:
        observed = True
    elif values == {"false"}:
        observed = False
    else:
        raise ValidationError(f"prefix-cache evidence is missing or inconsistent: {sorted(values)}")
    if observed != expected_prefix:
        raise ValidationError(f"prefix caching observed {str(observed).lower()}, expected {str(expected_prefix).lower()}")
    return {"expected_prefix_caching": expected_prefix, "observed_prefix_caching": observed}


def parse_timestamp(value: str) -> dt.datetime:
    for format_string in ("%Y/%m/%d %H:%M:%S.%f", "%Y/%m/%d %H:%M:%S", "%Y-%m-%dT%H:%M:%S%z"):
        try:
            parsed = dt.datetime.strptime(value.strip(), format_string)
            local_zone = dt.datetime.now().astimezone().tzinfo
            return parsed.replace(tzinfo=local_zone).astimezone(dt.timezone.utc) if parsed.tzinfo is None else parsed.astimezone(dt.timezone.utc)
        except ValueError:
            pass
    raise ValidationError(f"unparseable telemetry timestamp {value!r}")


def validate_telemetry(path: Path, started: float, ended: float, freshness_seconds: float = 300) -> dict[str, Any]:
    try:
        with path.open(newline="", encoding="utf-8") as handle:
            rows = list(csv.reader(handle))
    except OSError as exc:
        raise ValidationError(f"cannot read telemetry {path}: {exc}") from exc
    if not rows or [cell.strip() for cell in rows[0]] != TELEMETRY_HEADER:
        raise ValidationError("telemetry header/schema is invalid")
    if len(rows) < 2:
        raise ValidationError("telemetry contains only its header")
    timestamps = []
    for number, row in enumerate(rows[1:], 2):
        if len(row) != len(TELEMETRY_HEADER):
            raise ValidationError(f"telemetry row {number} has {len(row)} fields, expected {len(TELEMETRY_HEADER)}")
        timestamps.append(parse_timestamp(row[0]).timestamp())
        for offset in range(1, len(row)):
            if not row[offset].strip():
                raise ValidationError(f"telemetry row {number} has an empty field")
        for offset in (1, 4, 5, 6, 7, 8, 9, 10):
            try:
                float(row[offset])
            except ValueError as exc:
                raise ValidationError(f"telemetry row {number} field {TELEMETRY_HEADER[offset]} is not numeric") from exc
    if timestamps != sorted(timestamps):
        raise ValidationError("telemetry timestamps are not monotonic")
    if min(timestamps) < started - freshness_seconds or max(timestamps) > ended + freshness_seconds:
        raise ValidationError("telemetry timestamps are stale relative to the benchmark")
    samples_in_window = sum(started <= timestamp <= ended for timestamp in timestamps)
    if samples_in_window == 0:
        raise ValidationError("telemetry has no sample inside the benchmark window")
    return {"schema": TELEMETRY_HEADER, "sample_count": len(rows) - 1, "samples_in_benchmark_window": samples_in_window,
            "first_timestamp": rows[1][0], "last_timestamp": rows[-1][0]}


def request_metric_files(run_root: Path) -> list[Path]:
    return sorted((run_root / "measured" / "requests").glob("*/metrics.json"))


def validate_smoke(
    smoke_root: Path, model: str, requests: int, warmup: int, concurrency: int, input_tokens: int,
    max_output: int, expected_output: int | None, token_timing: bool,
) -> dict[str, Any]:
    runs = sorted(path for path in smoke_root.iterdir() if path.is_dir()) if smoke_root.is_dir() else []
    if len(runs) != 1:
        raise ValidationError(f"smoke output has {len(runs)} run directories, expected 1")
    run_root = runs[0]
    run = load_json(run_root / "run.json")
    summary = load_json(run_root / "summary.json")
    if not (run_root / "summary.csv").is_file():
        raise ValidationError("smoke summary.csv is missing")
    if run.get("schema_version") != 7 or summary.get("schema_version") != 7:
        raise ValidationError("smoke run/summary schema must be 7")
    if run.get("run_status") != "completed" or summary.get("run_status") != "completed" or summary.get("complete") is not True:
        raise ValidationError("smoke run is not completed with complete summary")
    if run.get("model") != model or run.get("temperature") != 0:
        raise ValidationError("smoke model/temperature contract does not match")
    counts = summary.get("counts") or {}
    wanted_counts = {"requested_or_planned": requests, "started": requests, "completed": requests, "successful": requests, "failed": 0}
    for key, wanted in wanted_counts.items():
        if counts.get(key) != wanted:
            raise ValidationError(f"smoke counts.{key} is {counts.get(key)!r}, expected {wanted}")
    warmup_value = run.get("warmup") or {}
    expected_warmup = {"requested": warmup, "attempted": warmup, "completed": warmup, "successful": warmup, "failed": 0}
    if any(warmup_value.get(key) != wanted for key, wanted in expected_warmup.items()):
        raise ValidationError("smoke discarded benchmark-request warmup contract is incomplete")
    if warmup_value.get("status") != ("completed" if warmup else "skipped"):
        raise ValidationError("smoke discarded benchmark-request warmup status does not match")
    measurement = run.get("measurement") or {}
    expected_measurement = {"requested": requests, "attempted": requests, "completed": requests, "successful": requests, "failed": 0}
    if any(measurement.get(key) != wanted for key, wanted in expected_measurement.items()):
        raise ValidationError("smoke measured request contract is incomplete")
    workload = summary.get("workload") or {}
    if workload.get("input_target_tokens") != input_tokens or workload.get("input_resolved_tokens") != input_tokens:
        raise ValidationError("smoke target/resolved input token contract does not match")
    if workload.get("requested_output_max_tokens") != max_output:
        raise ValidationError("smoke requested output maximum does not match")
    closed = (summary.get("load") or {}).get("closed_loop") or {}
    if (summary.get("load") or {}).get("mode") != "closed_loop" or closed.get("requested_concurrency") != concurrency:
        raise ValidationError("smoke concurrency/load contract does not match")
    metrics_files = request_metric_files(run_root)
    if len(metrics_files) != requests:
        raise ValidationError(f"smoke has {len(metrics_files)} measured metrics files, expected {requests}")
    actual_outputs: list[int] = []
    itl_available = 0
    itl_unavailable = 0
    for path in metrics_files:
        metric = load_json(path)
        usage = metric.get("token_usage") or {}
        if usage.get("available") is not True:
            raise ValidationError(f"{path}: server token usage is unavailable")
        if usage.get("input_tokens") != input_tokens:
            raise ValidationError(f"{path}: server input tokens do not match expected {input_tokens}")
        output = usage.get("output_tokens")
        if isinstance(output, bool) or not isinstance(output, int) or output < 0:
            raise ValidationError(f"{path}: actual output token count is invalid")
        if expected_output is not None and output != expected_output:
            raise ValidationError(f"{path}: actual output tokens {output}, expected {expected_output}")
        actual_outputs.append(output)
        itl = metric.get("itl") or {}
        if itl.get("available") is True:
            if itl.get("source") != ITL_SOURCE:
                raise ValidationError(f"{path}: available ITL has unsupported source")
            itl_available += 1
        else:
            if token_timing and not isinstance(itl.get("reason"), str):
                raise ValidationError(f"{path}: unavailable ITL has no reason")
            itl_unavailable += 1
    if token_timing:
        timing = run.get("token_timing") or {}
        if timing.get("mode") != "vllm" or timing.get("source") != ITL_SOURCE:
            raise ValidationError("smoke token-timing contract is missing")
    return {
        "run_id": summary.get("run_id"), "completed": True, "requests": requests,
        "discarded_benchmark_request_warmup": warmup, "concurrency": concurrency,
        "requested_input_tokens": input_tokens, "resolved_input_tokens": input_tokens,
        "requested_max_output_tokens": max_output, "actual_output_tokens": actual_outputs,
        "true_itl": {"requested": token_timing, "available_requests": itl_available, "unavailable_requests": itl_unavailable},
    }


def scan_secrets(root: Path, api_key_env: str) -> dict[str, Any]:
    secret = os.environ.get(api_key_env, "") if api_key_env else ""
    errors: list[str] = []
    scanned = 0
    for path in sorted(item for item in root.rglob("*") if item.is_file() and not item.is_symlink()):
        try:
            data = path.read_bytes()
        except OSError as exc:
            errors.append(f"cannot read {path}: {exc}")
            continue
        scanned += 1
        text = data.decode("utf-8", errors="ignore")
        if secret and secret.encode() in data:
            errors.append(f"{path}: contains configured API-key value")
        if any(pattern.search(text) for pattern in SECRET_PATTERNS):
            errors.append(f"{path}: contains credential-shaped content")
    if errors:
        raise ValidationError("; ".join(errors))
    return {"files_scanned": scanned, "secret_values_found": 0, "credential_shapes_found": 0}


def make_manifest(root: Path, output: Path) -> dict[str, Any]:
    records = []
    for path in sorted(item for item in root.rglob("*") if item.is_file() and item != output):
        relative = path.relative_to(root).as_posix()
        digest = sha256_file(path)
        records.append({"path": relative, "sha256": digest, "size_bytes": path.stat().st_size})
    payload = {"manifest_schema_version": 1, "file_count": len(records), "files": records}
    write_json(output, payload)
    return payload


def sha256_file(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def hash_file(path: Path, output: Path, name: str | None = None) -> dict[str, Any]:
    digest = sha256_file(path)
    output.write_text(f"{digest}  {name or path.name}\n", encoding="utf-8")
    return {"path": str(path), "sha256": digest, "size_bytes": path.stat().st_size}


def verify_hash(path: Path, checksum: Path) -> dict[str, Any]:
    try:
        fields = checksum.read_text(encoding="utf-8").strip().split()
    except OSError as exc:
        raise ValidationError(f"cannot read checksum {checksum}: {exc}") from exc
    if len(fields) != 2 or not re.fullmatch(r"[0-9a-f]{64}", fields[0]):
        raise ValidationError("checksum sidecar is malformed")
    observed = sha256_file(path)
    if observed != fields[0]:
        raise ValidationError("SHA-256 verification failed")
    return {"path": str(path), "sha256": observed, "verified": True}


def bool_arg(value: str) -> bool:
    if value == "true":
        return True
    if value == "false":
        return False
    raise argparse.ArgumentTypeError("must be true or false")


def main(argv: list[str] | None = None) -> int:
    parser = argparse.ArgumentParser()
    sub = parser.add_subparsers(dest="command", required=True)
    models = sub.add_parser("models")
    models.add_argument("--file", type=Path, required=True); models.add_argument("--model", required=True); models.add_argument("--max-model-len", type=int)
    tokenize = sub.add_parser("tokenize")
    tokenize.add_argument("--file", type=Path, required=True); tokenize.add_argument("--expected", type=int)
    generation = sub.add_parser("generation")
    generation.add_argument("--file", type=Path, required=True); generation.add_argument("--model", required=True)
    generation.add_argument("--expected-input", type=int); generation.add_argument("--expected-output", type=int)
    server = sub.add_parser("server-config")
    server.add_argument("--file", type=Path, required=True); server.add_argument("--model", required=True); server.add_argument("--vllm-version", required=True)
    server.add_argument("--dtype", required=True); server.add_argument("--tensor-parallel-size", type=int, required=True); server.add_argument("--world-size", type=int, required=True); server.add_argument("--kv-cache-dtype", required=True)
    server.add_argument("--max-model-len", type=int, required=True); server.add_argument("--max-num-seqs", type=int, required=True); server.add_argument("--gpu-memory-utilization", type=float, required=True)
    server.add_argument("--generation-config", required=True); server.add_argument("--thinking", choices=("true", "false", "not-applicable"), required=True)
    server.add_argument("--prefix-caching", type=bool_arg, required=True)
    server.add_argument("--output", type=Path, required=True); server.add_argument("--api-key-env", default="")
    version = sub.add_parser("version")
    version.add_argument("--file", type=Path, required=True); version.add_argument("--expected", required=True); version.add_argument("--output", type=Path, required=True)
    gpu = sub.add_parser("gpu")
    gpu.add_argument("--file", type=Path, required=True); gpu.add_argument("--expected-count", type=int); gpu.add_argument("--expected-name"); gpu.add_argument("--expected-memory-mib", type=int)
    metrics = sub.add_parser("metrics")
    metrics.add_argument("--file", type=Path, required=True); metrics.add_argument("--expected-prefix", type=bool_arg, required=True)
    telemetry = sub.add_parser("telemetry")
    telemetry.add_argument("--file", type=Path, required=True); telemetry.add_argument("--started", type=float, required=True); telemetry.add_argument("--ended", type=float, required=True)
    smoke = sub.add_parser("smoke")
    smoke.add_argument("--root", type=Path, required=True); smoke.add_argument("--model", required=True); smoke.add_argument("--requests", type=int, required=True); smoke.add_argument("--warmup", type=int, required=True)
    smoke.add_argument("--concurrency", type=int, required=True); smoke.add_argument("--input-tokens", type=int, required=True); smoke.add_argument("--max-output", type=int, required=True)
    smoke.add_argument("--expected-output", type=int); smoke.add_argument("--token-timing", type=bool_arg, required=True)
    redact = sub.add_parser("redact")
    redact.add_argument("--root", type=Path, required=True); redact.add_argument("--api-key-env", default="")
    manifest = sub.add_parser("manifest")
    manifest.add_argument("--root", type=Path, required=True); manifest.add_argument("--output", type=Path, required=True)
    hashing = sub.add_parser("hash")
    hashing.add_argument("--file", type=Path, required=True); hashing.add_argument("--output", type=Path, required=True); hashing.add_argument("--name")
    hash_verify = sub.add_parser("verify-hash")
    hash_verify.add_argument("--file", type=Path, required=True); hash_verify.add_argument("--checksum", type=Path, required=True)
    args = parser.parse_args(argv)
    try:
        if args.command == "models": result = validate_models(load_json(args.file), args.model, args.max_model_len)
        elif args.command == "tokenize": result = validate_tokenize(load_json(args.file), args.expected)
        elif args.command == "generation": result = validate_generation(load_json(args.file), args.model, args.expected_input, args.expected_output)
        elif args.command == "server-config":
            thinking: bool | str = args.thinking if args.thinking == "not-applicable" else args.thinking == "true"
            expected = {"model": args.model, "vllm_version": args.vllm_version, "dtype": args.dtype, "tensor_parallel_size": args.tensor_parallel_size,
                        "world_size": args.world_size, "kv_cache_dtype": args.kv_cache_dtype, "max_model_len": args.max_model_len,
                        "max_num_seqs": args.max_num_seqs, "gpu_memory_utilization": args.gpu_memory_utilization,
                        "generation_config": args.generation_config, "thinking": thinking, "prefix_caching": args.prefix_caching}
            source = load_json(args.file)
            secret = os.environ.get(args.api_key_env, "") if args.api_key_env else ""
            source_text = args.file.read_text(encoding="utf-8")
            if (secret and secret in source_text) or any(pattern.search(source_text) for pattern in SECRET_PATTERNS):
                raise ValidationError("server configuration contains credential material")
            result = validate_server_config(source, expected)
            write_json(args.output, result)
        elif args.command == "version":
            result = validate_version(load_json(args.file), args.expected)
            write_json(args.output, result)
        elif args.command == "gpu": result = validate_gpu(args.file, args.expected_count, args.expected_name, args.expected_memory_mib)
        elif args.command == "metrics": result = validate_metrics(args.file.read_text(encoding="utf-8"), args.expected_prefix)
        elif args.command == "telemetry": result = validate_telemetry(args.file, args.started, args.ended)
        elif args.command == "smoke": result = validate_smoke(args.root, args.model, args.requests, args.warmup, args.concurrency, args.input_tokens, args.max_output, args.expected_output, args.token_timing)
        elif args.command == "redact": result = scan_secrets(args.root, args.api_key_env)
        elif args.command == "manifest": result = make_manifest(args.root, args.output)
        elif args.command == "hash": result = hash_file(args.file, args.output, args.name)
        else: result = verify_hash(args.file, args.checksum)
    except (OSError, ValidationError) as exc:
        print(f"error: {exc}", file=sys.stderr)
        return 1
    print(json.dumps(result, sort_keys=True))
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
