#!/usr/bin/env python3
"""Offline V1 acceptance artifact audit using only the Python standard library."""

from __future__ import annotations

import argparse
import csv
import datetime as dt
import json
import math
import os
from pathlib import Path
import re
import sys
from typing import Any

RUN_SCHEMA = 7
EXPERIMENT_SCHEMA = 1
VERIFICATION_SCHEMA = 1
ITL_SOURCE = "vllm_return_token_ids_client_receive"
PROMPT_SENTINEL = "Slentore V1 acceptance sentinel."
TOKEN_SENTINELS = ("987654300", "987654301", "987654321", "987654322")
RAW_KEYS = {"messages", "choices", "delta", "prompt_token_ids", "token_ids"}
SAFE_PATH = re.compile(r"^[A-Za-z0-9._/-]+$")


class Audit:
    def __init__(self, root: Path, api_key_env: str) -> None:
        self.root = root
        self.api_key_env = api_key_env
        self.errors: list[str] = []
        self.benchmark_failures: list[str] = []
        self.scenarios: dict[str, dict[str, Any]] = {}

    def error(self, message: str) -> None:
        self.errors.append(message)

    def benchmark_failure(self, message: str) -> None:
        self.benchmark_failures.append(message)

    def run(self) -> dict[str, Any]:
        acceptance = load_json(self.root / "acceptance.json", self.errors)
        if acceptance.get("acceptance_schema_version") != 1:
            self.error("acceptance.json has unsupported schema")

        basic_root = self.root / "basic"
        basic_dirs = sorted(path for path in basic_root.iterdir() if path.is_dir()) if basic_root.is_dir() else []
        if len(basic_dirs) != 1:
            self.error(f"basic scenario has {len(basic_dirs)} run directories, want 1")
        else:
            self.scenarios["basic"] = self.verify_run(basic_dirs[0], require_timing=True, require_slo=True)

        for name, relative, token_length in (
            ("closed_loop", "closed-loop", False),
            ("open_loop", "open-loop", False),
            ("token_length", "token-length", True),
        ):
            experiment_dirs = sorted((self.root / relative / "experiments").glob("*")) if (self.root / relative / "experiments").is_dir() else []
            if len(experiment_dirs) != 1:
                self.error(f"{name} scenario has {len(experiment_dirs)} experiment directories, want 1")
                continue
            self.scenarios[name] = self.verify_experiment(experiment_dirs[0], token_length=token_length)

        self.scan_redaction()
        artifacts_valid = not self.errors
        acceptance_passed = artifacts_valid and not self.benchmark_failures
        report = {
            "verification_schema_version": VERIFICATION_SCHEMA,
            "verified_at": dt.datetime.now(dt.timezone.utc).isoformat(),
            "artifacts_valid": artifacts_valid,
            "acceptance_passed": acceptance_passed,
            "errors": self.errors,
            "benchmark_failures": self.benchmark_failures,
            "scenarios": self.scenarios,
        }
        write_json_atomic(self.root / "verification.json", report)
        return report

    def verify_run(self, run_dir: Path, *, require_timing: bool = False, require_slo: bool = False, token_target: int | None = None) -> dict[str, Any]:
        run = load_json(run_dir / "run.json", self.errors)
        summary = load_json(run_dir / "summary.json", self.errors)
        check_csv_rows(run_dir / "summary.csv", 2, self.errors)
        run_id = run.get("run_id")
        if run.get("schema_version") != RUN_SCHEMA or summary.get("schema_version") != RUN_SCHEMA:
            self.error(f"{run_dir}: child schema is not {RUN_SCHEMA}")
        if not isinstance(run_id, str) or run_id != run_dir.name or summary.get("run_id") != run_id:
            self.error(f"{run_dir}: incoherent run identity")
        if run.get("run_status") != summary.get("run_status"):
            self.error(f"{run_dir}: run and summary statuses differ")
        if run.get("run_status") != "completed" or not summary.get("complete"):
            self.benchmark_failure(f"{run_dir}: benchmark run did not complete successfully")

        if token_target is not None:
            run_input = ((run.get("workload") or {}).get("input") or {})
            summary_workload = summary.get("workload") or {}
            values = (run_input.get("target_tokens"), run_input.get("resolved_tokens"), summary_workload.get("input_target_tokens"), summary_workload.get("input_resolved_tokens"))
            if any(value != token_target for value in values):
                self.error(f"{run_dir}: token target/resolved evidence {values} does not equal point target {token_target}")

        if require_timing:
            self.verify_itl(run_dir, run, summary)
        if require_slo:
            self.verify_slo(run_dir, summary)
        return {"run_id": run_id, "run_status": run.get("run_status"), "path": str(run_dir.relative_to(self.root))}

    def verify_experiment(self, experiment_dir: Path, *, token_length: bool) -> dict[str, Any]:
        manifest = load_json(experiment_dir / "experiment.json", self.errors)
        experiment_id = manifest.get("experiment_id")
        points = manifest.get("points") if isinstance(manifest.get("points"), list) else []
        planned = manifest.get("planned_points")
        if manifest.get("experiment_schema_version") != EXPERIMENT_SCHEMA:
            self.error(f"{experiment_dir}: unsupported experiment schema")
        if experiment_id != experiment_dir.name:
            self.error(f"{experiment_dir}: incoherent experiment identity")
        if not isinstance(planned, int) or planned != len(points) or planned <= 0:
            self.error(f"{experiment_dir}: point count is inconsistent")
            planned = len(points)
        check_csv_rows(experiment_dir / "summary.csv", planned + 1, self.errors)

        child_count = 0
        for offset, point in enumerate(points, 1):
            if point.get("point_index") != offset or point.get("point_id") != f"point-{offset:06d}":
                self.error(f"{experiment_dir}: point {offset} identity is inconsistent")
            if point.get("point_status") != "executed":
                self.error(f"{experiment_dir}: point {offset} was not executed ({point.get('point_status')})")
                continue
            run_id = point.get("run_id")
            run_path = point.get("run_path")
            if not safe_child_path(run_path, run_id):
                self.error(f"{experiment_dir}: point {offset} has unsafe child identity/path")
                continue
            target = None
            if token_length:
                parameters = point.get("parameters") or {}
                target = parameters.get("input_tokens")
                if not isinstance(target, int) or target <= 0:
                    self.error(f"{experiment_dir}: point {offset} has invalid token target")
                    continue
            self.verify_run(experiment_dir / run_path, token_target=target)
            child_count += 1
        return {
            "experiment_id": experiment_id,
            "status": manifest.get("status"),
            "planned_points": planned,
            "verified_children": child_count,
            "path": str(experiment_dir.relative_to(self.root)),
        }

    def verify_itl(self, run_dir: Path, run: dict[str, Any], summary: dict[str, Any]) -> None:
        timing = run.get("token_timing") or {}
        if timing.get("mode") != "vllm" or timing.get("source") != ITL_SOURCE:
            self.error(f"{run_dir}: vLLM token timing metadata is missing")
        available_counts = 0
        available_requests = 0
        metrics_files = sorted((run_dir / "measured" / "requests").glob("*/metrics.json"))
        if not metrics_files:
            self.error(f"{run_dir}: timing run has no measured request metrics")
        for metrics_path in metrics_files:
            metrics = load_json(metrics_path, self.errors)
            reason = validate_itl(metrics.get("itl") or {}, metrics.get("token_usage") or {})
            if reason:
                self.error(f"{metrics_path}: {reason}")
            elif (metrics.get("itl") or {}).get("available"):
                available_requests += 1
                available_counts += int((metrics.get("itl") or {}).get("count", 0))
        aggregate_itl = summary.get("itl") or {}
        if aggregate_itl.get("available_requests") != available_requests:
            self.error(f"{run_dir}: aggregate ITL available request count is inconsistent")
        if available_requests:
            if aggregate_itl.get("available_sources") != [ITL_SOURCE]:
                self.error(f"{run_dir}: aggregate ITL source is invalid")
            intervals = aggregate_itl.get("intervals_ms") or {}
            if intervals.get("sample_count") != available_counts:
                self.error(f"{run_dir}: aggregate ITL sample count is inconsistent")

    def verify_slo(self, run_dir: Path, summary: dict[str, Any]) -> None:
        slo = summary.get("slo") or {}
        if not slo.get("configured") or not all(slo.get(name) for name in ("ttft", "tpot", "e2e")):
            self.error(f"{run_dir}: SLO/goodput path was not configured")
            return
        successful = slo.get("successful_requests", 0)
        evaluable = slo.get("evaluable_requests", 0)
        good = slo.get("good_requests", 0)
        bad = slo.get("bad_requests", 0)
        unevaluable = slo.get("unevaluable_requests", 0)
        if evaluable != good + bad or successful != evaluable + unevaluable:
            self.error(f"{run_dir}: SLO counts are inconsistent")
        if evaluable and bad:
            self.benchmark_failure(f"{run_dir}: permissive acceptance SLO had {bad} bad requests")

    def scan_redaction(self) -> None:
        forbidden_values = [PROMPT_SENTINEL, *TOKEN_SENTINELS]
        if self.api_key_env:
            secret = os.environ.get(self.api_key_env, "")
            if secret:
                forbidden_values.append(secret)
        for path in self.root.rglob("*"):
            if not path.is_file() or path.name == "verification.json" or path.suffix.lower() not in {".json", ".jsonl", ".csv", ".log", ".txt"}:
                continue
            try:
                text = path.read_text(encoding="utf-8")
            except UnicodeDecodeError:
                continue
            for value in forbidden_values:
                if value and value in text:
                    self.error(f"{path}: contains forbidden redaction sentinel")
            if re.search(r"(?i)(authorization[\"']?\s*:|authorization\s+header|bearer\s+)", text):
                self.error(f"{path}: contains Authorization material")
            if path.suffix.lower() in {".json", ".jsonl"}:
                for line_number, value in enumerate_json_values(text, path):
                    bad_keys = find_raw_keys(value)
                    if bad_keys:
                        self.error(f"{path}:{line_number}: contains raw payload keys {sorted(bad_keys)}")


def validate_itl(itl: dict[str, Any], usage: dict[str, Any]) -> str | None:
    if itl.get("available"):
        if itl.get("source") != ITL_SOURCE:
            return "available ITL has an invalid source"
        output_tokens = usage.get("output_tokens") if usage.get("available") else None
        count = itl.get("count")
        values = itl.get("values_ms")
        if not isinstance(output_tokens, int) or output_tokens < 1 or count != output_tokens - 1:
            return "available ITL count does not equal output_tokens - 1"
        if not isinstance(values, list) or len(values) != count or any(not isinstance(value, (int, float)) or not math.isfinite(value) or value < 0 for value in values):
            return "available ITL values are invalid"
    elif not isinstance(itl.get("reason"), str) or not itl.get("reason").strip():
        return "unavailable ITL lacks an explicit reason"
    return None


def safe_child_path(run_path: Any, run_id: Any) -> bool:
    if not isinstance(run_path, str) or not isinstance(run_id, str) or not SAFE_PATH.fullmatch(run_path):
        return False
    path = Path(run_path)
    return not path.is_absolute() and ".." not in path.parts and path.parts == ("runs", run_id) and Path(run_id).name == run_id


def find_raw_keys(value: Any) -> set[str]:
    found: set[str] = set()
    if isinstance(value, dict):
        for key, child in value.items():
            if key in RAW_KEYS:
                found.add(key)
            found.update(find_raw_keys(child))
    elif isinstance(value, list):
        for child in value:
            found.update(find_raw_keys(child))
    return found


def enumerate_json_values(text: str, path: Path):
    if path.suffix.lower() == ".jsonl":
        for number, line in enumerate(text.splitlines(), 1):
            if line.strip():
                try:
                    yield number, json.loads(line)
                except json.JSONDecodeError:
                    yield number, {}
    else:
        try:
            yield 1, json.loads(text)
        except json.JSONDecodeError:
            yield 1, {}


def load_json(path: Path, errors: list[str]) -> dict[str, Any]:
    try:
        value = json.loads(path.read_text(encoding="utf-8"))
    except (OSError, json.JSONDecodeError) as error:
        errors.append(f"{path}: cannot read valid JSON: {error}")
        return {}
    if not isinstance(value, dict):
        errors.append(f"{path}: top-level JSON value is not an object")
        return {}
    return value


def check_csv_rows(path: Path, expected: int, errors: list[str]) -> None:
    try:
        with path.open(newline="", encoding="utf-8") as handle:
            rows = list(csv.reader(handle))
    except OSError as error:
        errors.append(f"{path}: cannot read CSV: {error}")
        return
    if len(rows) != expected or not rows or not rows[0]:
        errors.append(f"{path}: has {len(rows)} rows, want {expected}")


def write_json_atomic(path: Path, value: dict[str, Any]) -> None:
    temporary = path.with_name(f".{path.name}.tmp")
    temporary.write_text(json.dumps(value, indent=2) + "\n", encoding="utf-8")
    temporary.replace(path)


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser()
    parser.add_argument("--acceptance-dir", required=True, type=Path)
    parser.add_argument("--api-key-env", default="")
    return parser.parse_args()


def main() -> int:
    args = parse_args()
    audit = Audit(args.acceptance_dir.resolve(), args.api_key_env)
    report = audit.run()
    if report["artifacts_valid"] and report["acceptance_passed"]:
        print(f"V1 acceptance verification passed: {args.acceptance_dir}")
        return 0
    for error in report["errors"]:
        print(f"artifact error: {error}", file=sys.stderr)
    for failure in report["benchmark_failures"]:
        print(f"benchmark failure: {failure}", file=sys.stderr)
    return 1


if __name__ == "__main__":
    raise SystemExit(main())
