import json
from pathlib import Path

import pandas as pd


RAW_DIR = Path("results/day02/raw")
OUT_DIR = Path("results/day03")
OUT_CSV = OUT_DIR / "day02_summary.csv"


def load_result(path: Path) -> dict:
    with path.open("r", encoding="utf-8") as f:
        data = json.load(f)

    name = path.stem

    # Expected names:
    # day02_c1_i128_o64
    # day02_c2_i128_o64
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
    for path in sorted(RAW_DIR.glob("day02_c*_i128_o64.json")):
        rows.append(load_result(path))

    if not rows:
        raise FileNotFoundError(f"No Day 2 JSON files found in {RAW_DIR}")

    df = pd.DataFrame(rows).sort_values("concurrency")
    df.to_csv(OUT_CSV, index=False)

    print(df.to_string(index=False))
    print(f"\nSaved: {OUT_CSV}")


if __name__ == "__main__":
    main()