"""Turns bench/raw/*.csv into bench/results.md and chart PNGs.

Run with `make bench-report`. Every number in results.md is computed here from
the raw CSVs; nothing is typed in by hand. Hand-written commentary lives in
bench/findings.md and is spliced in verbatim.
"""
import csv
import os
import statistics
from collections import defaultdict

import matplotlib

matplotlib.use("Agg")
import matplotlib.pyplot as plt

HERE = os.path.dirname(os.path.abspath(__file__))
RAW = os.path.join(HERE, "raw")

# Categorical slots 1-4 of the reference palette (validated, light mode).
SERIES = [
    ("fl-64shards", "fl-cache, 64 shards", "#2a78d6", "o", "-"),
    ("fl-1shard", "fl-cache, 1 shard", "#eb6834", "s", "-"),
    ("redis", "Redis 7.0 (default)", "#1baf7a", "^", "-"),
    ("redis-io4", "Redis 7.0 (io-threads 4)", "#eda100", "D", "--"),
]
INK, INK2, GRID, SURFACE = "#0b0b0b", "#52514e", "#e4e3df", "#fcfcfb"


def read_csv(name):
    with open(os.path.join(RAW, name)) as f:
        return list(csv.DictReader(f))


def fmt_rps(v):
    return f"{v / 1000:.1f}k"


def cell(vals, fmt):
    return f"{fmt(statistics.median(vals))} ({fmt(min(vals))}–{fmt(max(vals))})"


def style(ax):
    ax.set_facecolor(SURFACE)
    for side in ("top", "right"):
        ax.spines[side].set_visible(False)
    for side in ("left", "bottom"):
        ax.spines[side].set_color(GRID)
    ax.tick_params(colors=INK2, labelsize=9, length=0)
    ax.grid(axis="y", color=GRID, linewidth=0.8)
    ax.set_axisbelow(True)


def network_section(rows):
    data = defaultdict(list)  # (cfg, clients, test, metric) -> [values per rep]
    for r in rows:
        for m in ("rps", "p99_ms"):
            data[(r["config"], int(r["clients"]), r["test"], m)].append(float(r[m]))
    clients = sorted({int(r["clients"]) for r in rows})
    cfgs = [s for s in SERIES if any(r["config"] == s[0] for r in rows)]
    reps = len({r["rep"] for r in rows})

    fig, axes = plt.subplots(2, 2, figsize=(10, 7), facecolor=SURFACE, sharex=True)
    for col, test in enumerate(("set", "get")):
        for row, (metric, ylabel) in enumerate((("rps", "Throughput (k requests/s)"), ("p99_ms", "p99 latency (ms)"))):
            ax = axes[row][col]
            style(ax)
            for key, label, color, marker, ls in cfgs:
                ys = [statistics.median(data[(key, c, test, metric)]) for c in clients]
                ys = [y / 1000 for y in ys] if metric == "rps" else ys
                ax.plot(clients, ys, color=color, marker=marker, linestyle=ls, linewidth=2,
                        markersize=7, markeredgecolor=SURFACE, markeredgewidth=1.5, label=label)
                ax.annotate(label.replace("fl-cache, ", "fl ").replace(" (default)", "").replace(" (io-threads 4)", " io4"),
                            (clients[-1], ys[-1]), xytext=(6, 0), textcoords="offset points",
                            fontsize=8, color=INK2, va="center")
            ax.set_xscale("log", base=2)
            ax.set_xticks(clients)
            ax.set_xticklabels([str(c) for c in clients])
            ax.set_xlim(clients[0] * 0.8, clients[-1] * 2.4)
            if metric == "p99_ms":
                ax.set_yscale("log")
                ticks = [0.2, 0.5, 1, 2, 5]
                ax.set_yticks(ticks)
                ax.set_yticklabels([f"{t:g}" for t in ticks])
                ax.minorticks_off()
            else:
                ax.set_ylim(bottom=0)
            if col == 0:
                ax.set_ylabel(ylabel, color=INK2, fontsize=9)
            if row == 0:
                ax.set_title(test.upper(), color=INK, fontsize=11, loc="left", fontweight="bold")
            if row == 1:
                ax.set_xlabel("concurrent clients", color=INK2, fontsize=9)
    handles, labels = axes[0][0].get_legend_handles_labels()
    fig.legend(handles, labels, loc="lower center", ncol=len(cfgs), frameon=False, fontsize=9,
               labelcolor=INK2, bbox_to_anchor=(0.5, -0.005))
    fig.suptitle(f"redis-benchmark, 64 B values, 100k keys — median of {reps} runs",
                 color=INK, fontsize=12, x=0.01, ha="left")
    fig.tight_layout(rect=(0, 0.04, 1, 0.96))
    fig.savefig(os.path.join(HERE, "chart-network.png"), dpi=150, facecolor=SURFACE)
    plt.close(fig)

    md = []
    for test in ("set", "get"):
        for metric, title, fmt in (("rps", "throughput, requests/s", fmt_rps), ("p99_ms", "p99 latency, ms", lambda v: f"{v:.2f}")):
            md.append(f"**{test.upper()} {title}** — median (min–max) over {reps} runs\n")
            md.append("| clients | " + " | ".join(c[1] for c in cfgs) + " |")
            md.append("|---:|" + "---:|" * len(cfgs))
            for c in clients:
                md.append(f"| {c} | " + " | ".join(cell(data[(k, c, test, metric)], fmt) for k, *_ in cfgs) + " |")
            md.append("")
    return "\n".join(md), data, clients


def store_section(rows):
    data = defaultdict(list)
    for r in rows:
        data[(int(r["shards"]), int(r["threads"]))].append(float(r["ops_per_sec"]))
    threads = sorted({t for _, t in data})
    reps = len(data[(1, threads[0])])

    fig, ax = plt.subplots(figsize=(7, 4), facecolor=SURFACE)
    style(ax)
    for shards, label, color, marker in ((64, "64 shards", "#2a78d6", "o"), (1, "1 shard (one big lock)", "#eb6834", "s")):
        ys = [statistics.median(data[(shards, t)]) / 1e6 for t in threads]
        ax.plot(threads, ys, color=color, marker=marker, linewidth=2, markersize=7,
                markeredgecolor=SURFACE, markeredgewidth=1.5, label=label)
        ax.annotate(label.split(" (")[0], (threads[-1], ys[-1]), xytext=(6, 0), textcoords="offset points",
                    fontsize=8, color=INK2, va="center")
    ax.set_xscale("log", base=2)
    ax.set_xticks(threads)
    ax.set_xticklabels([str(t) for t in threads])
    ax.set_xlim(threads[0] * 0.8, threads[-1] * 1.8)
    ax.set_ylim(bottom=0)
    ax.set_xlabel("threads hammering one Store", color=INK2, fontsize=9)
    ax.set_ylabel("M operations/s", color=INK2, fontsize=9)
    ax.set_title(f"In-process Store, 50% SET / 50% GET — median of {reps} runs", color=INK, fontsize=11, loc="left")
    ax.legend(frameon=False, fontsize=9, labelcolor=INK2, loc="upper left")
    fig.tight_layout()
    fig.savefig(os.path.join(HERE, "chart-store.png"), dpi=150, facecolor=SURFACE)
    plt.close(fig)

    md = ["| threads | 1 shard | 64 shards | 64 ÷ 1 |", "|---:|---:|---:|---:|"]
    for t in threads:
        a, b = data[(1, t)], data[(64, t)]
        ratio = statistics.median(b) / statistics.median(a)
        md.append(f"| {t} | {cell(a, lambda v: f'{v / 1e6:.2f}M')} | {cell(b, lambda v: f'{v / 1e6:.2f}M')} | {ratio:.2f}× |")
    return "\n".join(md), reps


def main():
    env = open(os.path.join(RAW, "env.txt")).read().strip()
    net_md, _, _ = network_section(read_csv("results.csv"))
    store_md, store_reps = store_section(read_csv("store.csv"))
    findings_path = os.path.join(HERE, "findings.md")
    findings = open(findings_path).read().strip() if os.path.exists(findings_path) else "_(findings not written yet)_"

    doc = f"""# fl-cache benchmark results

Every number below is generated by `bench/report.py` from the raw CSVs in `bench/raw/`.
Nothing here is typed in by hand except the *Findings* section.

## Findings

{findings}

## Environment

```
{env}
```

fl-cache runs with `--threads 4`; Redis runs single-threaded unless noted. All servers are pinned
to the same 4 vCPUs; the load generator is pinned to a disjoint set. Both run in one Linux container
inside Docker Desktop's VM, over loopback — see *Caveats*.

## 1. Network benchmark: fl-cache vs Redis

![Throughput and p99 latency vs concurrent clients](chart-network.png)

Reproduce: `make bench` (≈25 min), then `make bench-report`.
Each run is `redis-benchmark -p PORT -c <clients> -n <requests> -d 64 -r 100000 -t <set|get> --threads <min(clients,8)> --csv`;
keys are preloaded (`bench/preload.py`) so every GET hits. Each server is restarted and preloaded
for every repetition, and the four configurations are interleaved within each repetition so slow
drift (thermal, noisy neighbours) is spread across them rather than favouring one.
Request counts: 50k (1 client), 300k (8 clients), 500k (32 and 128 clients).

{net_md}

## 2. In-process Store microbenchmark

Same `Store` class the server uses, driven directly by N threads (no sockets, no parsing),
100k preloaded keys, 64 B values, 50% SET / 50% GET, {store_reps} runs of 3 s per cell.
This isolates lock contention, which the network benchmark can hide behind syscall cost.

![Store throughput vs threads](chart-store.png)

Reproduce: `make bench-store`, then `make bench-report`. Binary: `cache/build/release/fl-store-bench`.

{store_md}

## Caveats

- **Docker Desktop on a Mac.** The kernel is a LinuxKit VM; loopback round trips are far slower than
  on bare-metal Linux. Absolute numbers are not representative of production hardware. The comparison
  between configurations (same machine, same conditions, interleaved) is the meaningful part.
- **Hyper-threads.** The 16 vCPUs are the host's logical CPUs; I could not verify which vCPUs share a
  physical core, so "disjoint CPUs" for server and load generator does not guarantee disjoint cores.
- **One machine, one workload.** 64-byte values, uniform random keys, 100% hit rate, no pipelining.
  Skewed keys (a hot key) would behave differently, especially for the sharded store.
- **fl-cache times every command** (two `steady_clock` reads) for its latency histogram. Redis does
  no equivalent work on this path, so this slightly disadvantages fl-cache. The overhead was not
  measured separately.
- **p99 comes from `redis-benchmark`'s client-side histogram**, so it includes the load generator's own
  scheduling delays, and has millisecond resolution to 3 decimals.
"""
    with open(os.path.join(HERE, "results.md"), "w") as f:
        f.write(doc)
    print("wrote results.md, chart-network.png, chart-store.png")


if __name__ == "__main__":
    main()
