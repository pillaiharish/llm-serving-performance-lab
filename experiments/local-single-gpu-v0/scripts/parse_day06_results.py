"""
Parse Day 6 vLLM benchmark JSON files into a Day 6 CSV summary.
"""
import json
from pathlib import Path

import pandas as pd


RAW_DIR = Path("results/day06/raw")
OUT_DIR = Path("results/day06")
OUT_CSV = OUT_DIR / "day06_summary.csv"


def load_result(path: Path) -> dict:
    with path.open("r", encoding="utf-8") as f:
        data = json.load(f)

    name = path.stem

    # Expected names:
    # day06_c1_i128_o64_n64.json
    # day06_c2_i128_o64_n64.json
    concurrency = int(name.split("_c")[1].split("_")[0])

    return {
        "file": str(path),
        "concurrency": concurrency,
        "completed": data.get("completed"),
        "failed": data.get("failed"),
        "duration_s": data.get("duration"),
        "request_throughput_req_s": data.get("request_throughput"),
        "output_throughput_tok_s": data.get("output_throughput"),
        "total_token_throughput_tok_s": data.get("total_token_throughput"),
        "total_input_tokens": data.get("total_input_tokens"),
        "total_output_tokens": data.get("total_output_tokens"),
        "ttft_p50_ms": data.get("p50_ttft_ms"),
        "ttft_p95_ms": data.get("p95_ttft_ms"),
        "tpot_p50_ms": data.get("p50_tpot_ms"),
        "itl_p50_ms": data.get("p50_itl_ms"),
        "e2el_p95_ms": data.get("p95_e2el_ms"),
    }


def main() -> None:
    OUT_DIR.mkdir(parents=True, exist_ok=True)

    rows = []
    for path in sorted(RAW_DIR.glob("day06_c*_i128_o64_n64.json")):
        rows.append(load_result(path))

    if not rows:
        raise FileNotFoundError(f"No Day 6 JSON files found in {RAW_DIR}")

    df = pd.DataFrame(rows).sort_values("concurrency")
    df.to_csv(OUT_CSV, index=False)

    print(df.to_string(index=False))
    print(f"\nSaved: {OUT_CSV}")


if __name__ == "__main__":
    main()