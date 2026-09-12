#!/usr/bin/env python3
"""Audit and plot the H100 Slentore archive without extracting it."""
from __future__ import annotations

import argparse, csv, hashlib, io, json, math, os, re, shutil, subprocess, sys, tarfile, tempfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path, PurePosixPath
from statistics import mean, stdev

VERSION, EXPECTED = "4", {1, 2, 3}
EXPECTED_MODEL = "Qwen/Qwen3.8-27B"
EXPECTED_SCHEMA = 7
EXPECTED_SLENTORE = "v0.0.0-20260830170145-4a573b104970"
RUN_RE = re.compile(r"(.*/slentore/c1-512-128-rep(\d+)/[^/]+)/run\.json$")
ITL_SOURCE = "vllm_return_token_ids_client_receive"
EXPLORE = "EXPLORATORY — ONE AVAILABLE REPETITION — NOT A REPEATABILITY RESULT"
TOL = 1e-6

class ValidationError(RuntimeError): pass

@dataclass
class Run:
    rep: int; root: str; run: dict; summary: dict; requests: list[dict]; itl: list[float]; telemetry: list[dict]

@dataclass
class Audit:
    runs: list[Run]; checks: list[tuple[str, str]]; found: set[int]
    @property
    def complete(self): return self.found == EXPECTED
    @property
    def cross_valid(self): return self.complete and len(self.runs) == 3

class Archive:
    def __init__(self, path):
        self.tar = tarfile.open(path, "r:gz")
        files = [m for m in self.tar.getmembers() if m.isfile()]
        names = [m.name for m in files]
        if len(names) != len(set(names)): raise ValidationError("archive contains duplicate member names")
        if any(PurePosixPath(n).is_absolute() or ".." in PurePosixPath(n).parts for n in names): raise ValidationError("archive contains an unsafe member path")
        self.members = dict(zip(names, files))
    def close(self): self.tar.close()
    def bytes(self, name):
        try: handle = self.tar.extractfile(self.members[name])
        except KeyError as exc: raise ValidationError(f"missing artifact: {name}") from exc
        if handle is None: raise ValidationError(f"unreadable artifact: {name}")
        return handle.read()
    def json(self, name):
        try: value = json.loads(self.bytes(name), parse_constant=lambda x: (_ for _ in ()).throw(ValueError(x)))
        except (json.JSONDecodeError, UnicodeDecodeError, ValueError) as exc: raise ValidationError(f"invalid JSON: {name}: {exc}") from exc
        if not isinstance(value, dict): raise ValidationError(f"invalid JSON object: {name}")
        return value

def sha256(path):
    h = hashlib.sha256()
    with path.open("rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""): h.update(chunk)
    return h.hexdigest()

def checksum_from(path, archive):
    for line in path.read_text().splitlines():
        p = line.split()
        if len(p) >= 2 and p[-1].lstrip("*") == archive.name and re.fullmatch(r"[0-9a-fA-F]{64}", p[0]): return p[0].lower()
    raise ValidationError(f"checksum file has no SHA-256 entry for {archive.name}")

def verify_sha(path, expected):
    if not re.fullmatch(r"[0-9a-fA-F]{64}", expected): raise ValidationError("expected SHA-256 must be 64 hexadecimal characters")
    actual = sha256(path)
    if actual != expected.lower(): raise ValidationError(f"archive SHA-256 mismatch: expected {expected.lower()}, got {actual}")
    return actual

def require(ok, message, checks):
    checks.append(("PASS" if ok else "FAIL", message))
    if not ok: raise ValidationError(message)

def finite(value, label):
    if isinstance(value, bool) or not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0: raise ValidationError(f"{label} must be finite and nonnegative")
    return float(value)

def close(saved, raw, label):
    if not math.isclose(saved, raw, rel_tol=1e-9, abs_tol=TOL): raise ValidationError(f"{label} mismatch: persisted={saved}, recomputed={raw}")

def percentile(values, q):
    values = sorted(values)
    if not values: raise ValidationError("cannot compute percentile from zero samples")
    return values[max(0, math.ceil(q * len(values)) - 1)]

def iso(value, label):
    try: return datetime.fromisoformat(value.replace("Z", "+00:00"))
    except (AttributeError, ValueError) as exc: raise ValidationError(f"invalid timestamp: {label}") from exc

def gpu_csv(raw, rep):
    try:
        reader = csv.DictReader(io.StringIO(raw.decode()))
        keys = {"timestamp", " utilization.gpu [%]", " memory.used [MiB]", " power.draw [W]", " clocks.current.sm [MHz]"}
        if not reader.fieldnames or not keys <= set(reader.fieldnames): raise ValidationError(f"rep{rep}: malformed GPU telemetry header")
        rows = []
        for src in reader:
            def num(key):
                m = re.fullmatch(r"\s*(-?\d+(?:\.\d+)?)\s*(?:%|MiB|W|MHz)?\s*", src.get(key, ""))
                if not m: raise ValidationError(f"rep{rep}: malformed telemetry field {key!r}")
                return finite(float(m.group(1)), f"rep{rep}: telemetry {key.strip()}")
            rows.append({"timestamp": datetime.strptime(src["timestamp"].strip(), "%Y/%m/%d %H:%M:%S.%f"), "gpu_util_pct": num(" utilization.gpu [%]"), "memory_used_mib": num(" memory.used [MiB]"), "power_w": num(" power.draw [W]"), "sm_clock_mhz": num(" clocks.current.sm [MHz]")})
    except UnicodeDecodeError as exc: raise ValidationError(f"rep{rep}: telemetry is not UTF-8") from exc
    if not rows: raise ValidationError(f"rep{rep}: telemetry is empty")
    return rows

def pairs(a, root, phase, prefix, count, rep):
    base = f"{root}/{phase}/requests/"
    expected = {f"{prefix}-{i:06d}" for i in range(1, count + 1)}
    found = {"metrics.json": {}, "observation.json": {}}
    for name in a.members:
        if not name.startswith(base) or PurePosixPath(name).name not in found: continue
        relative = PurePosixPath(name.removeprefix(base))
        if len(relative.parts) != 2 or relative.parts[0] not in expected:
            raise ValidationError(f"rep{rep}: {phase} contains non-canonical request artifact: {name}")
        request_id, kind = relative.parts
        if request_id in found[kind]: raise ValidationError(f"rep{rep}: {phase} contains duplicate {kind} for {request_id}")
        found[kind][request_id] = name
    metrics, observations = found["metrics.json"], found["observation.json"]
    if set(metrics) != expected or set(observations) != expected: raise ValidationError(f"rep{rep}: {phase} artifacts are missing, duplicated, or unexpectedly named")
    return [(rid, metrics[rid], observations[rid]) for rid in sorted(expected)]

def metric_value(metric, key, unit, label):
    field = metric.get(key, {})
    if field.get("available") is not True or field.get("unit") != unit: raise ValidationError(f"{label}: {key} must be available in {unit}")
    return finite(field.get("value"), f"{label}: {key}")

def request(a, metric_name, observation_name, run_id, rid, rep, keep):
    label = f"rep{rep} {rid}"; metric, obs = a.json(metric_name), a.json(observation_name)
    if metric.get("run_id") != run_id or obs.get("run_id") != run_id: raise ValidationError(f"{label}: run-ID mismatch")
    if metric.get("request_id") != rid or obs.get("request_id") != rid: raise ValidationError(f"{label}: request-ID mismatch")
    if obs.get("status_code") != 200 or obs.get("finish_reason") != "length" or not obs.get("completed_at"): raise ValidationError(f"{label}: request did not complete normally with HTTP 200")
    if obs.get("timed_out") is True or obs.get("cancelled") is True or obs.get("error"): raise ValidationError(f"{label}: timeout, cancellation, or error recorded")
    timing = obs.get("token_timing", {})
    if timing != {"requested": True, "source": ITL_SOURCE, "completed_through_done": True}: raise ValidationError(f"{label}: observation token timing is invalid")
    for usage, where in ((metric.get("token_usage", {}), "metric"), (obs.get("usage", {}), "observation")):
        if usage.get("available") is not True or usage.get("source") != "server_usage" or [usage.get(k) for k in ("input_tokens", "output_tokens", "total_tokens")] != [512, 128, 640]: raise ValidationError(f"{label}: {where} usage is not exact 512/128/640 server usage")
    ttft, e2e = metric_value(metric, "ttft", "ms", label), metric_value(metric, "e2e", "ms", label)
    tpot, ttlt = metric_value(metric, "tpot", "ms/token", label), metric_value(metric, "ttlt", "ms", label)
    first, last, completed = [finite(obs.get(k), f"{label}: {k}") for k in ("first_content_after_ns", "last_content_after_ns", "completed_after_ns")]
    close(ttft, first / 1e6, f"{label}: TTFT"); close(e2e, completed / 1e6, f"{label}: E2E"); close(ttlt, last / 1e6, f"{label}: TTLT"); close(tpot, (last - first) / 127e6, f"{label}: TPOT")
    itl = metric.get("itl", {}); values = itl.get("values_ms")
    if itl.get("available") is not True or itl.get("source") != ITL_SOURCE or itl.get("count") != 127 or not isinstance(values, list) or len(values) != 127: raise ValidationError(f"{label}: persisted ITL evidence is invalid")
    values = [finite(v, f"{label}: ITL") for v in values]
    events = [e for e in obs.get("stream_events", []) if e.get("token_ids_present")]
    if len(events) != 128 or any(e.get("generated_token_count") != 1 for e in events): raise ValidationError(f"{label}: expected 128 one-token events with token-ID-presence evidence")
    times = [finite(e.get("received_after_ns"), f"{label}: token event") for e in events]
    if any(b <= a for a, b in zip(times, times[1:])): raise ValidationError(f"{label}: token events are not strictly ordered")
    raw = [(b - a) / 1e6 for a, b in zip(times, times[1:])]
    for i, (saved, derived) in enumerate(zip(values, raw), 1): close(saved, derived, f"{label}: ITL interval {i}")
    usage = obs["usage"]
    row = {"index": int(rid.rsplit("-", 1)[1]), "ttft_ms": ttft, "tpot_ms": tpot, "ttlt_ms": ttlt, "e2e_ms": e2e, "input_tokens": usage["input_tokens"], "output_tokens": usage["output_tokens"], "total_tokens": usage["total_tokens"]} if keep else None
    return row, values

def csv_number(row, key, label):
    try: value = float(row[key])
    except (KeyError, TypeError, ValueError) as exc: raise ValidationError(f"{label}: invalid summary.csv field {key}") from exc
    return finite(value, f"{label}: summary.csv {key}")

def validate_run(a, rep, root, checks):
    run, summary = a.json(f"{root}/run.json"), a.json(f"{root}/summary.json")
    try: rows = list(csv.DictReader(io.StringIO(a.bytes(f"{root}/summary.csv").decode())))
    except UnicodeDecodeError as exc: raise ValidationError(f"rep{rep}: summary.csv is not UTF-8") from exc
    require(len(rows) == 1, f"rep{rep}: summary.csv has one row", checks); scsv = rows[0]; rid = run.get("run_id")
    require(run.get("schema_version") == summary.get("schema_version") == EXPECTED_SCHEMA and scsv.get("schema_version") == str(EXPECTED_SCHEMA), f"rep{rep}: run.json, summary.json, and summary.csv use supported schema {EXPECTED_SCHEMA}", checks)
    w, load = run.get("workload", {}), run.get("load", {})
    tokenizer = w.get("tokenizer") if isinstance(w, dict) else None
    tokenizer_model = tokenizer.get("model") if isinstance(tokenizer, dict) else None
    require(run.get("model") == EXPECTED_MODEL and tokenizer_model == EXPECTED_MODEL and run.get("model") == tokenizer_model, f"rep{rep}: persisted model identities agree with {EXPECTED_MODEL}", checks)
    require(run.get("slentore_version") == EXPECTED_SLENTORE, f"rep{rep}: Slentore version={EXPECTED_SLENTORE}", checks)
    require(run.get("run_status") == summary.get("run_status") == scsv.get("run_status") == "completed", f"rep{rep}: all statuses completed", checks)
    require(summary.get("complete") is True and scsv.get("summary_complete", "").lower() == "true", f"rep{rep}: summaries complete", checks)
    require(rid and rid == summary.get("run_id") == scsv.get("run_id"), f"rep{rep}: run IDs agree", checks)
    require(w.get("mode") == "token_length", f"rep{rep}: workload mode=token_length", checks)
    require(w.get("input", {}).get("target_tokens") == w.get("input", {}).get("resolved_tokens") == 512, f"rep{rep}: input target/resolved=512/512", checks)
    require(w.get("output", {}).get("requested_max_tokens") == 128, f"rep{rep}: output max=128", checks)
    require(load.get("mode") == "closed_loop" and load.get("closed_loop", {}).get("requested_concurrency") == 1, f"rep{rep}: closed-loop C=1", checks)
    require(run.get("temperature") == 0 and run.get("token_timing") == {"mode": "vllm", "source": ITL_SOURCE}, f"rep{rep}: deterministic generation and vLLM token timing", checks)
    warm, measured = run.get("warmup", {}), run.get("measurement", {})
    require(all(warm.get(k) == 10 for k in ("requested", "attempted", "completed", "successful")) and warm.get("failed") == 0, f"rep{rep}: warmup 10/10, zero failures", checks)
    require(all(measured.get(k) == 100 for k in ("requested", "attempted", "completed", "successful")) and measured.get("failed") == 0, f"rep{rep}: measured 100/100, zero failures", checks)
    require(warm.get("effective_workers") == measured.get("effective_workers") == 1 and max(warm.get("max_observed_active_requests", 99), measured.get("max_observed_active_requests", 99)) <= 1, f"rep{rep}: workers/active requests consistent with C=1", checks)
    zeroes = ("request_error", "request_timeout", "parent_cancelled", "drain_timeout")
    require(all(warm.get("outcomes", {}).get(k) == measured.get("outcomes", {}).get(k) == 0 for k in zeroes), f"rep{rep}: no timeout/cancellation/error outcomes", checks)
    sw, sl, counts = summary.get("workload", {}), summary.get("load", {}), summary.get("counts", {})
    require(sw == {"mode": "token_length", "input_target_tokens": 512, "input_resolved_tokens": 512, "requested_output_max_tokens": 128}, f"rep{rep}: summary workload agrees", checks)
    cl = sl.get("closed_loop", {}); require(sl.get("mode") == "closed_loop" and cl.get("requested_concurrency") == cl.get("worker_count") == 1 and cl.get("max_observed_active") <= 1, f"rep{rep}: summary load agrees", checks)
    expected_counts = {"requested_or_planned": 100, "started": 100, "completed": 100, "successful": 100, "failed": 0, **{k: 0 for k in zeroes}}
    require(all(counts.get(k) == v for k, v in expected_counts.items()), f"rep{rep}: summary counts agree", checks)
    require(scsv.get("load_mode") == "closed_loop" and scsv.get("workload_mode") == "token_length" and scsv.get("requested_concurrency") == scsv.get("worker_count") == "1", f"rep{rep}: summary.csv workload/load agree", checks)
    for request_id, m, o in pairs(a, root, "warmup", "warmup", 10, rep): request(a, m, o, rid, request_id, rep, False)
    requests, intervals = [], []
    for request_id, m, o in pairs(a, root, "measured", "req", 100, rep):
        row, values = request(a, m, o, rid, request_id, rep, True); requests.append(row); intervals += values
    checks += [("PASS", f"rep{rep}: warmups validated separately"), ("PASS", f"rep{rep}: 100 unique measured requests validated"), ("PASS", f"rep{rep}: 12,700 ITL gaps recomputed from receive events")]
    for key, values in (("ttft", [r["ttft_ms"] for r in requests]), ("tpot", [r["tpot_ms"] for r in requests]), ("ttlt", [r["ttlt_ms"] for r in requests]), ("e2e", [r["e2e_ms"] for r in requests])):
        f = summary.get("latency", {}).get(key, {}); unit = "ms/token" if key == "tpot" else "ms"
        require(f.get("available") is True and f.get("unit") == unit and f.get("sample_count") == 100 and f.get("unavailable_count") == 0, f"rep{rep}: summary {key} units/count valid", checks)
        for q, name in ((.5, "p50"), (.95, "p95"), (.99, "p99")): close(finite(f.get(name), f"rep{rep}: {key} {name}"), percentile(values, q), f"rep{rep}: raw {key} {name}")
    f = summary.get("itl", {}).get("intervals_ms", {}); require(f.get("available") is True and f.get("unit") == "ms" and f.get("sample_count") == 12700, f"rep{rep}: ITL units/count valid", checks)
    for q, name in ((.5, "p50"), (.95, "p95"), (.99, "p99")): close(finite(f.get(name), f"rep{rep}: ITL {name}"), percentile(intervals, q), f"rep{rep}: raw ITL {name}")
    elapsed_ns = finite(measured.get("elapsed_ns"), f"rep{rep}: measurement.elapsed_ns")
    require(elapsed_ns > 0, f"rep{rep}: measurement.elapsed_ns is positive", checks)
    duration = elapsed_ns / 1e9
    timestamp_duration = (iso(measured.get("completed_at"), f"rep{rep}: measurement.completed_at") - iso(measured.get("started_at"), f"rep{rep}: measurement.started_at")).total_seconds()
    require(timestamp_duration > 0, f"rep{rep}: measurement timestamps are ordered", checks)
    close(timestamp_duration, duration, f"rep{rep}: measurement timestamp duration")
    duration_field = summary.get("durations", {}).get("completion_window_seconds", {})
    require(duration_field.get("available") is True and duration_field.get("unit") == "s", f"rep{rep}: completion window metadata valid", checks)
    close(finite(duration_field.get("value"), f"rep{rep}: summary duration"), duration, f"rep{rep}: summary.json completion window")
    close(csv_number(scsv, "completion_window_seconds", f"rep{rep}"), duration, f"rep{rep}: summary.csv completion window")
    totals = {"successful": len(requests), "input": sum(r["input_tokens"] for r in requests), "output": sum(r["output_tokens"] for r in requests), "total": sum(r["total_tokens"] for r in requests)}
    require(totals["total"] == totals["input"] + totals["output"], f"rep{rep}: raw total tokens equal input plus output", checks)
    tokens = summary.get("tokens", {})
    require([tokens.get(k) for k in ("successful_requests", "usage_covered_requests", "successful_input_tokens", "successful_output_tokens", "successful_total_tokens")] == [totals["successful"], totals["successful"], totals["input"], totals["output"], totals["total"]], f"rep{rep}: summary token totals agree with request observations", checks)
    for key, raw in (("successful", totals["successful"]), ("successful_input_tokens", totals["input"]), ("successful_output_tokens", totals["output"]), ("successful_total_tokens", totals["total"])): close(csv_number(scsv, key, f"rep{rep}"), raw, f"rep{rep}: summary.csv {key}")
    rate_fields = ((summary.get("request_rates", {}).get("successful_request_throughput", {}), "successful_requests_per_second", "requests/s", totals["successful"]), (summary.get("tokens", {}).get("throughput", {}).get("input_tokens", {}), "input_tokens_per_second", "tokens/s", totals["input"]), (summary.get("tokens", {}).get("throughput", {}).get("output_tokens", {}), "output_tokens_per_second", "tokens/s", totals["output"]), (summary.get("tokens", {}).get("throughput", {}).get("total_tokens", {}), "total_tokens_per_second", "tokens/s", totals["total"]))
    for field, csv_key, unit, numerator in rate_fields:
        require(field.get("available") is True and field.get("unit") == unit and field.get("numerator") == numerator, f"rep{rep}: throughput metadata/numerator valid for {csv_key}", checks)
        close(finite(field.get("denominator_seconds"), f"rep{rep}: throughput denominator"), duration, f"rep{rep}: throughput denominator")
        expected_rate = numerator / duration
        close(finite(field.get("value"), f"rep{rep}: throughput"), expected_rate, f"rep{rep}: summary.json {csv_key}")
        close(csv_number(scsv, csv_key, f"rep{rep}"), expected_rate, f"rep{rep}: summary.csv {csv_key}")
    checks.append(("PASS", f"rep{rep}: duration cross-checked from elapsed_ns, timestamps, summary.json, and summary.csv (absolute tolerance {TOL:g} s)"))
    name = next((n for n in a.members if n.endswith(f"/telemetry/c1-512-128-rep{rep}-gpu.csv")), None); require(name is not None, f"rep{rep}: GPU telemetry exists", checks)
    telemetry = gpu_csv(a.bytes(name), rep); checks.append(("PASS", f"rep{rep}: parsed {len(telemetry)} telemetry samples; timezone unresolved"))
    return Run(rep, root, run, summary, requests, intervals, telemetry)

def signature(run): return {k: run.run.get(k) for k in ("schema_version", "slentore_version", "model", "temperature", "workload", "load", "token_timing", "request_timeout")}

def audit(path):
    checks, runs, found = [], [], set(); a = Archive(path)
    try:
        roots = {}
        for name in a.members:
            m = RUN_RE.match(name)
            if m: roots.setdefault(int(m.group(2)), []).append(m.group(1))
        found = set(roots); checks.append(("PASS" if found == EXPECTED else "FAIL", f"discovered repetitions: {sorted(found)}; expected [1, 2, 3]"))
        require(bool(found) and found <= EXPECTED, f"only expected repetitions present: {sorted(found)}", checks)
        for rep in found: require(len(roots[rep]) == 1, f"rep{rep}: exactly one run directory", checks)
        starts = [n for n in a.members if n.endswith("/logs/vllm-startup.log")]; require(len(starts) == 1, "one vLLM startup log exists", checks)
        text = a.bytes(starts[0]).decode(errors="replace"); markers = ("V1 LLM engine (v0.26.0)", "tensor_parallel_size=1", "dtype=torch.bfloat16", "kv_cache_dtype=bfloat16", "max_seq_len=32768", "enable_prefix_caching=False", "max_num_seqs': 16", "gpu_memory_utilization': 0.9")
        require(all(m in text for m in markers), "startup proves requested vLLM configuration", checks)
        env = [n for n in a.members if n.endswith("/environment/nvidia-smi-q-final.txt")]; require(len(env) == 1 and "NVIDIA H100 80GB HBM3" in a.bytes(env[0]).decode(errors="replace"), "environment proves H100 80GB HBM3", checks)
        for rep in sorted(found):
            for suffix, label in ((f"/logs/c1-512-128-rep{rep}.txt", "runtime log"), (f"/telemetry/vllm-before-rep{rep}.prom", "pre-run Prometheus"), (f"/telemetry/vllm-after-rep{rep}.prom", "post-run Prometheus")): require(any(n.endswith(suffix) for n in a.members), f"rep{rep}: {label} exists", checks)
            runs.append(validate_run(a, rep, roots[rep][0], checks))
        for run in runs[1:]: require(signature(run) == signature(runs[0]), f"rep{run.rep}: comparison configuration matches rep1", checks)
        return Audit(runs, checks, found)
    except ValidationError as exc: exc.checks, exc.runs, exc.found = checks, runs, found; raise
    finally: a.close()

def validation(result, archive, digest, mode, verified, error):
    runs, found = (result.runs, result.found) if result else ([], set()); complete = bool(result and result.complete and not error); cross = bool(result and result.cross_valid and not error)
    scope = EXPLORE if mode == "exploratory" else "STRICT VALIDATION — PUBLICATION REQUIRES THREE VALID REPETITIONS"
    return {"schema_version": 1, "analysis_scope": scope, "archive": archive.name, "archive_sha256": digest, "checksum_verified": verified, "mode": mode, "validation_status": "valid" if complete else "incomplete" if result and (not error or "incomplete experiment" in error) else "invalid", "individual_runs": {str(r.rep): {"valid": True} for r in runs}, "requested_experiment": {"expected_repetitions": [1,2,3], "found_repetitions": sorted(found), "complete": complete}, "cross_repetition_comparison": {"valid": cross, "reason": None if cross else "requires three valid independent repetitions"}, "error": error, "checks": [{"status": s, "check": m} for s,m in (result.checks if result else [])]}

def write_validation(out, doc):
    (out/"validation.json").write_text(json.dumps(doc, indent=2, sort_keys=True)+"\n")
    lines = ["# Validation report", "", f"Archive SHA-256: `{doc['archive_sha256']}`", "", f"Mode: **{doc['mode']}**", "", f"Validation: **{doc['validation_status'].upper()}**", "", f"Individual valid runs: **{', '.join(doc['individual_runs']) or 'none'}**", "", f"Requested experiment complete: **{str(doc['requested_experiment']['complete']).lower()}**", "", f"Cross-repetition comparison valid: **{str(doc['cross_repetition_comparison']['valid']).lower()}**", ""]
    if doc["mode"] == "exploratory": lines += [f"> {EXPLORE}", ""]
    if doc["error"]: lines += [f"Error: `{doc['error']}`", ""]
    if "analyzer" in doc:
        p=doc["analyzer"];lines += ["## Analyzer provenance", "", f"Analyzer SHA-256: `{p['analyzer_sha256']}` (authoritative byte-level identity).", "", f"Git SHA: `{p['git_sha'] or 'unavailable'}`; tracked={str(p['git_tracked']).lower()}; dirty={str(p['git_dirty']).lower()}; complete={str(p['provenance_complete']).lower()}.", ""]
    lines += ["| Status | Check |", "|---|---|"] + [f"| {x['status']} | {x['check']} |" for x in doc["checks"]]
    (out/"validation.md").write_text("\n".join(lines)+"\n")

def plotting():
    try:
        import matplotlib; matplotlib.use("Agg"); import matplotlib.pyplot as plt
    except ImportError as exc: raise RuntimeError("install analysis/requirements.txt") from exc
    plt.rcParams.update({"savefig.dpi":220,"font.size":10,"axes.grid":True,"grid.alpha":.25,"axes.spines.top":False,"axes.spines.right":False,"svg.hashsalt":"h100-analysis-v2"}); return plt

def lim(values, floor=0):
    lo, hi=min(values),max(values); pad=max((hi-lo)*.07,abs(hi)*.002,.001); return max(floor,lo-pad),hi+pad
def heading(text, exploratory): return f"{EXPLORE}\n{text}" if exploratory else text
def save(plt, fig, out, name):
    fig.tight_layout(); fig.savefig(out/f"{name}.svg",bbox_inches="tight",metadata={"Date":None}); fig.savefig(out/f"{name}.png",bbox_inches="tight",metadata={"Software":f"H100 analysis {VERSION}"}); plt.close(fig)
def ecdf(plt,runs,out,field,label,name,exploratory):
    fig,ax=plt.subplots(figsize=(7.2,4.5)); allv=[]
    for run in runs:
        v=sorted(r[field] for r in run.requests); allv+=v; ax.step(v,[(i+1)/len(v) for i in range(len(v))],where="post",label=f"Repetition {run.rep}")
    ax.set(title=heading(f"{label} empirical cumulative distribution (C=1)",exploratory),xlabel=label,ylabel="Cumulative probability",xlim=lim(allv),ylim=(0,1.01)); ax.legend(); save(plt,fig,out,name)

def plots(runs,out,exploratory):
    plt=plotting(); out.mkdir(parents=True)
    ecdf(plt,runs,out,"ttft_ms","TTFT (ms)","a_ttft_ecdf",exploratory)
    data=[[x["ttft_ms"] for x in r.requests] for r in runs]; fig,ax=plt.subplots(figsize=(7.2,4.5)); ax.hist(data,bins="fd",histtype="step",linewidth=1.8,label=[f"Repetition {r.rep}" for r in runs]); ax.set(title=heading("TTFT distribution by repetition (C=1)",exploratory),xlabel="TTFT (ms)",ylabel="Measured requests",xlim=lim(sum(data,[])),ylim=(0,None)); ax.legend(); save(plt,fig,out,"b_ttft_distribution")
    ecdf(plt,runs,out,"tpot_ms","TPOT (ms/token)","c_tpot_ecdf",exploratory); ecdf(plt,runs,out,"e2e_ms","End-to-end latency (ms)","d_e2e_ecdf",exploratory)
    fig,axes=plt.subplots(1,2,figsize=(11,4.2)); allv=[]
    for r in runs:
        v=sorted(r.itl); allv+=v; axes[0].step(v,[(i+1)/len(v) for i in range(len(v))],where="post",label=f"Repetition {r.rep}")
    axes[1].hist([r.itl for r in runs],bins="fd",histtype="step",linewidth=1.5,label=[f"Repetition {r.rep}" for r in runs]); axes[0].set(title="ITL ECDF",xlabel="Client-observed ITL (ms)",ylabel="Cumulative probability",xlim=lim(allv),ylim=(0,1.01)); axes[1].set(title="ITL distribution",xlabel="Client-observed ITL (ms)",ylabel="Pooled token intervals",xlim=lim(allv),ylim=(0,None)); axes[0].legend();axes[1].legend();fig.suptitle(heading("Client-observed token-ID-presence timing evidence\n(pooled within-repetition token intervals, C=1)",exploratory));save(plt,fig,out,"e_true_itl_ecdf_distribution")
    for letter,field,label in (("f","ttft_ms","TTFT (ms)"),("g","tpot_ms","TPOT (ms/token)"),("h","e2e_ms","End-to-end latency (ms)")):
        fig,ax=plt.subplots(figsize=(8,4.4)); vals=[]
        for r in runs: v=[x[field] for x in r.requests]; vals+=v; ax.plot(range(1,101),v,marker=".",linewidth=.8,label=f"Repetition {r.rep}")
        ax.set(title=heading(f"Request order versus {label.split(' (')[0]} (C=1)",exploratory),xlabel="Measured request index",ylabel=label,xlim=(1,100),ylim=lim(vals));ax.legend();save(plt,fig,out,f"{letter}_request_index_{field.removesuffix('_ms')}")
    specs=(("i","gpu_util_pct","GPU utilization (%)","GPU utilization"),("j","memory_used_mib","GPU memory used (MiB)","GPU memory used"),("k","power_w","GPU power draw (W)","GPU power draw"),("l","sm_clock_mhz","GPU SM clock (MHz)","GPU SM clock"))
    for letter,field,ylabel,label in specs:
        fig,ax=plt.subplots(figsize=(8,4.4)); vals=[]
        for r in runs:
            start=iso(r.run["measurement"]["started_at"],"start").replace(tzinfo=None); end=iso(r.run["measurement"]["completed_at"],"end").replace(tzinfo=None); elapsed=[(x["timestamp"]-start).total_seconds() for x in r.telemetry]; y=[x[field] for x in r.telemetry];vals+=y;ax.plot(elapsed,y,linewidth=1,label=f"Repetition {r.rep}");ax.axvline(0,color="black",linewidth=.8,linestyle="--");ax.axvline((end-start).total_seconds(),color="black",linewidth=.8,linestyle=":")
        ax.set(title=heading(f"{label} (start --, end :, timezone unresolved)",exploratory),xlabel="Elapsed from assumed measurement start (s)",ylabel=ylabel,ylim=lim(vals));ax.legend();save(plt,fig,out,f"{letter}_{field}_vs_elapsed")
    metrics=(("TTFT",lambda r:[x["ttft_ms"] for x in r.requests],"ms"),("TPOT",lambda r:[x["tpot_ms"] for x in r.requests],"ms/token"),("ITL pooled intervals",lambda r:r.itl,"ms"),("E2E",lambda r:[x["e2e_ms"] for x in r.requests],"ms"));fig,axes=plt.subplots(2,2,figsize=(11,8))
    for ax,(label,get,unit) in zip(axes.flat,metrics):
        for offset,r in enumerate(runs): shift=(offset-(len(runs)-1)/2)*.24;ax.bar([i+shift for i in range(3)],[percentile(get(r),q) for q in (.5,.95,.99)],width=.24,label=f"Repetition {r.rep}")
        ax.set(title=label,ylabel=unit,xticks=range(3),xticklabels=["p50","p95","p99"],ylim=(0,None));
        if len(runs)>1:ax.legend()
    base="Cross-repetition latency percentile comparison" if len(runs)==3 else "Available-repetition latency percentiles";fig.suptitle(heading(base+" (raw samples, C=1)",exploratory));save(plt,fig,out,"m_cross_repetition_percentiles" if len(runs)==3 else "m_available_repetition_percentiles")
    fig,axes=plt.subplots(1,3,figsize=(12,4.5)); rates=(("Successful requests","requests/s",lambda r:r.summary["request_rates"]["successful_request_throughput"]["value"]),("Output tokens","tokens/s",lambda r:r.summary["tokens"]["throughput"]["output_tokens"]["value"]),("Total tokens","tokens/s",lambda r:r.summary["tokens"]["throughput"]["total_tokens"]["value"]))
    for ax,(label,unit,get) in zip(axes,rates):ax.bar([f"Rep {r.rep}" for r in runs],[get(r) for r in runs]);ax.set(title=label,ylabel=unit,ylim=(0,None))
    base="Cross-repetition throughput comparison" if len(runs)==3 else "Available-repetition throughput";fig.suptitle(heading(base+" (C=1)",exploratory));save(plt,fig,out,"n_cross_repetition_throughput" if len(runs)==3 else "n_available_repetition_throughput")

def tables(runs,out,exploratory):
    out.mkdir(parents=True);scope=EXPLORE if exploratory else "VALID THREE-REPETITION EXPERIMENT"
    with (out/"request_metrics.csv").open("w",newline="") as f:
        w=csv.writer(f);w.writerow(["analysis_scope","repetition","request_index","ttft_ms","tpot_ms_per_token","e2e_ms"])
        for r in runs:
            for x in r.requests:w.writerow([scope,r.rep,x["index"],x["ttft_ms"],x["tpot_ms"],x["e2e_ms"]])
    with (out/"percentiles.csv").open("w",newline="") as f:
        w=csv.writer(f);w.writerow(["analysis_scope","repetition","population","metric","unit","p50","p95","p99"])
        for r in runs:
            for pop,label,v,unit in (("per-request","TTFT",[x["ttft_ms"] for x in r.requests],"ms"),("per-request","TPOT",[x["tpot_ms"] for x in r.requests],"ms/token"),("pooled within-repetition token intervals","ITL",r.itl,"ms"),("per-request","E2E",[x["e2e_ms"] for x in r.requests],"ms")):w.writerow([scope,r.rep,pop,label,unit]+[f"{percentile(v,q):.9f}" for q in (.5,.95,.99)])
    with (out/"throughput.csv").open("w",newline="") as f:
        w=csv.writer(f);w.writerow(["analysis_scope","repetition","successful_requests_per_s","output_tokens_per_s","total_tokens_per_s"])
        for r in runs:w.writerow([scope,r.rep,r.summary["request_rates"]["successful_request_throughput"]["value"],r.summary["tokens"]["throughput"]["output_tokens"]["value"],r.summary["tokens"]["throughput"]["total_tokens"]["value"]])

def measured_gpu(r):
    start=iso(r.run["measurement"]["started_at"],"start").replace(tzinfo=None);end=iso(r.run["measurement"]["completed_at"],"end").replace(tzinfo=None);return [x for x in r.telemetry if start<=x["timestamp"]<=end]
def report(runs,out,digest,exploratory,provenance):
    config=runs[0].run; workload=config["workload"]; load=config["load"]
    lines=[f"# {EXPLORE}" if exploratory else f"# H100 {config['model']} C=1 three-repetition report","",f"Archive SHA-256: `{digest}`","","## MEASURED FACTS","",f"H100 80GB HBM3; vLLM 0.26.0; {config['model']}; TP=1; BF16 weights/cache; max model length 32,768; GPU memory utilization 0.90; max sequences 16; prefix caching disabled. {load['mode'].replace('_', '-').title()} C={load['closed_loop']['requested_concurrency']}, deterministic token-length {workload['input']['resolved_tokens']}/{workload['output']['requested_max_tokens']}, temperature {config['temperature']}, 10 warmups, 100 measured requests per repetition. Artifact schema {config['schema_version']}; Slentore {config['slentore_version']}.","","ITL is client-observed token-ID-presence timing evidence pooled within each repetition; actual token ID values are not persisted and this is not server-internal timing.","","| Rep | TTFT p50/p95/p99 ms | TPOT p50/p95/p99 ms/token | pooled ITL p50/p95/p99 ms | E2E p50/p95/p99 ms | req/s | output tok/s | total tok/s |","|---:|---:|---:|---:|---:|---:|---:|---:|"]; scalars=[]
    for r in runs:
        series=([x["ttft_ms"] for x in r.requests],[x["tpot_ms"] for x in r.requests],r.itl,[x["e2e_ms"] for x in r.requests]);fmt=["/".join(f"{percentile(v,q):.3f}" for q in (.5,.95,.99)) for v in series];rates=(r.summary["request_rates"]["successful_request_throughput"]["value"],r.summary["tokens"]["throughput"]["output_tokens"]["value"],r.summary["tokens"]["throughput"]["total_tokens"]["value"]);lines.append(f"| {r.rep} | {' | '.join(fmt)} | {rates[0]:.6f} | {rates[1]:.6f} | {rates[2]:.6f} |");scalars.append((series,rates))
    lines += ["","### Telemetry during the assumed measured window","","The nvidia-smi timestamps contain no timezone. Wall-clock alignment with Slentore UTC timestamps is unresolved. GPU telemetry is sampled independently and does not establish exact per-request hardware causality.","","| Rep | samples | GPU util mean/range % | HBM mean/range MiB | power mean/range W | SM clock mean/range MHz |","|---:|---:|---:|---:|---:|---:|"]
    for r in runs:
        g=measured_gpu(r);cells=[]
        for field in ("gpu_util_pct","memory_used_mib","power_w","sm_clock_mhz"):
            v=[x[field] for x in g];cells.append(f"{mean(v):.2f} ({min(v):.2f}–{max(v):.2f})")
        lines.append(f"| {r.rep} | {len(g)} | {' | '.join(cells)} |")
    lines += ["","### Tails and outliers",""]
    for r in runs:
        v=[x["ttft_ms"] for x in r.requests];cut=percentile(v,.75)+1.5*(percentile(v,.75)-percentile(v,.25));flags=[f"request {x['index']} ({x['ttft_ms']:.3f} ms)" for x in r.requests if x["ttft_ms"]>cut];lines.append(f"- Rep {r.rep} TTFT Tukey 1.5×IQR high flags: {', '.join(flags) or 'none'}.")
    if len(runs)==3:
        lines += ["","### Across-repetition scalar variation","","Statistics below are across like-for-like per-run scalars. A mean of per-run percentiles is not an aggregate latency percentile.","","| Scalar | min | max | mean | sample std | CV |","|---|---:|---:|---:|---:|---:|"]; specs=[]
        for i,label in enumerate(("TTFT","TPOT","pooled ITL","E2E")):
            for q,name in ((.5,"p50"),(.95,"p95"),(.99,"p99")):specs.append((f"mean of per-run {label} {name} values",[percentile(x[0][i],q) for x in scalars]))
        specs += [("request throughput",[x[1][0] for x in scalars]),("output-token throughput",[x[1][1] for x in scalars]),("total-token throughput",[x[1][2] for x in scalars])]
        for label,v in specs:
            avg,sd=mean(v),stdev(v);lines.append(f"| {label} | {min(v):.6f} | {max(v):.6f} | {avg:.6f} | {sd:.6f} | {sd/avg:.4%} |")
    lines += ["","## INTERPRETATIONS","",("Only one repetition is available, so repeatability is not assessed." if exploratory else "Three repetitions permit limited repeatability assessment for this exact setup.")+" Outlier flags and temporal coincidence are descriptive, not causal.","","## HYPOTHESES","","No causal hypothesis is tested. Causes for tails require controlled experiments and defensible timestamp alignment.","","## LIMITATIONS","","This evidence applies only to this H100, model, vLLM configuration, deterministic 512/128 workload, and C=1 steady state. It does not establish batching, scheduler/concurrency/saturation scaling, TP/DP/multi-GPU performance, other models/engines, FP8 benefit, general H100 performance, production capacity, or SLO capability.","","## Analyzer provenance","",f"Analyzer SHA-256: `{provenance['analyzer_sha256']}` (authoritative byte-level identity for this invocation).",f"Repository Git SHA: `{provenance['git_sha'] or 'unavailable'}`; tracked={str(provenance['git_tracked']).lower()}; dirty={str(provenance['git_dirty']).lower()}; complete={str(provenance['provenance_complete']).lower()}.",""]
    (out/("exploratory_report.md" if exploratory else "experiment_report.md")).write_text("\n".join(lines))

def analyzer_provenance(script=Path(__file__).resolve()):
    critical = [script, script.with_name("test_analyze.py"), script.parents[1]/"requirements.txt", script.parents[1]/"checksums.sha256"]
    hashes = {str(p): sha256(p) for p in critical[1:] if p.is_file()}
    try:
        head = subprocess.run(["git", "rev-parse", "HEAD"], check=False, text=True, capture_output=True)
        tracked_result = subprocess.run(["git", "ls-files", "--error-unmatch", str(script)], check=False, text=True, capture_output=True)
        dirty_result = subprocess.run(["git", "status", "--porcelain", "--", *map(str, critical)], check=False, text=True, capture_output=True)
        git_sha = head.stdout.strip() if head.returncode == 0 else None
        tracked = tracked_result.returncode == 0
        dirty = dirty_result.returncode != 0 or bool(dirty_result.stdout.strip())
    except OSError: git_sha, tracked, dirty = None, False, True
    return {"script_path": str(script), "analyzer_sha256": sha256(script), "git_sha": git_sha, "git_dirty": dirty, "git_tracked": tracked, "provenance_complete": tracked and not dirty, "authoritative_identity": "analyzer_sha256", "critical_input_sha256": hashes}

def publish(root,digest,mode,doc,result,archive,error=None):
    parent=root/digest;parent.mkdir(parents=True,exist_ok=True);rid=datetime.now(timezone.utc).strftime("%Y%m%dT%H%M%S.%fZ")+f"-{os.getpid()}";tmp=Path(tempfile.mkdtemp(prefix=".tmp-",dir=parent));final=parent/rid
    try:
        provenance=analyzer_provenance();doc["analyzer"]=provenance;write_validation(tmp,doc);exploratory=mode=="exploratory"
        if result and not error and (result.complete or exploratory):tables(result.runs,tmp/"tables",exploratory);plots(result.runs,tmp/"plots",exploratory);report(result.runs,tmp,digest,exploratory,provenance)
        files=sorted(str(x.relative_to(tmp)) for x in tmp.rglob("*") if x.is_file());manifest={"schema_version":1,"analysis_scope":doc["analysis_scope"],"archive_sha256":digest,"analysis_timestamp":datetime.now(timezone.utc).isoformat(),"analyzer_version":VERSION,"analyzer":provenance,"mode":mode,"validation_status":doc["validation_status"],"experiment_complete":doc["requested_experiment"]["complete"],"cross_repetition_comparison_valid":doc["cross_repetition_comparison"]["valid"],"input_archive":str(archive),"generated_files":files+["manifest.json"]};(tmp/"manifest.json").write_text(json.dumps(manifest,indent=2,sort_keys=True)+"\n");tmp.rename(final);return final
    except Exception:shutil.rmtree(tmp);raise
def execute(archive,root,mode,expected):
    digest=sha256(archive)
    try:verify_sha(archive,expected)
    except ValidationError as exc:
        doc=validation(None,archive,digest,mode,False,str(exc));return 2,publish(root,digest,mode,doc,None,archive,str(exc))
    try:result=audit(archive)
    except ValidationError as exc:
        partial=Audit(getattr(exc,"runs",[]),getattr(exc,"checks",[]),getattr(exc,"found",set()));doc=validation(partial,archive,digest,mode,True,str(exc));return 2,publish(root,digest,mode,doc,partial,archive,str(exc))
    if not result.complete and mode=="strict":
        error=f"incomplete experiment: found repetitions {sorted(result.found)}, expected [1, 2, 3]";doc=validation(result,archive,digest,mode,True,error);return 2,publish(root,digest,mode,doc,result,archive,error)
    if mode=="exploratory" and len(result.runs)!=1:
        error="exploratory mode requires exactly one valid repetition";doc=validation(result,archive,digest,mode,True,error);return 2,publish(root,digest,mode,doc,result,archive,error)
    doc=validation(result,archive,digest,mode,True,None);return 0,publish(root,digest,mode,doc,result,archive)
def main():
    p=argparse.ArgumentParser();p.add_argument("archive",type=Path);p.add_argument("--output-root",type=Path,default=Path(__file__).resolve().parents[1]/"generated");p.add_argument("--mode",choices=("strict","exploratory"),default="strict");group=p.add_mutually_exclusive_group(required=True);group.add_argument("--expected-sha256");group.add_argument("--checksum-file",type=Path);args=p.parse_args()
    try:expected=args.expected_sha256 or checksum_from(args.checksum_file,args.archive);code,out=execute(args.archive,args.output_root,args.mode,expected)
    except (OSError,tarfile.TarError,ValidationError,RuntimeError) as exc:print(f"ANALYSIS FAILED BEFORE PUBLICATION: {exc}",file=sys.stderr);return 2
    print(f"{'ANALYSIS COMPLETE' if code==0 else 'VALIDATION FAILED'}: {out}");return code
if __name__=="__main__":raise SystemExit(main())
