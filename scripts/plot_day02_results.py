"""
Generate Day 3 latency and throughput plots from the Day 2 summary CSV.
"""
from pathlib import Path

import matplotlib.pyplot as plt
import pandas as pd


CSV_PATH = Path("results/day03/day02_summary.csv")
PLOT_DIR = Path("plots/day03")


def main() -> None:
    PLOT_DIR.mkdir(parents=True, exist_ok=True)

    df = pd.read_csv(CSV_PATH)

    # Chart 1: concurrency vs E2E p95
    plt.figure()
    plt.plot(df["concurrency"], df["e2el_p95_ms"], marker="o")
    plt.xlabel("Max concurrency")
    plt.ylabel("E2E p95 latency (ms)")
    plt.title("Day 2: Concurrency vs E2E p95 latency")
    plt.grid(True)
    plt.savefig(PLOT_DIR / "day02_concurrency_vs_e2e_p95.png", bbox_inches="tight", dpi=160)
    plt.close()

    # Chart 2: concurrency vs output throughput
    plt.figure()
    plt.plot(df["concurrency"], df["output_throughput_tok_s"], marker="o")
    plt.xlabel("Max concurrency")
    plt.ylabel("Output throughput (tokens/sec)")
    plt.title("Day 2: Concurrency vs output throughput")
    plt.grid(True)
    plt.savefig(PLOT_DIR / "day02_concurrency_vs_output_tps.png", bbox_inches="tight", dpi=160)
    plt.close()

    print(f"Saved plots to {PLOT_DIR}")


if __name__ == "__main__":
    main()