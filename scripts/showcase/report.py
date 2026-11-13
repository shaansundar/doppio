"""Collect usage, judge verdicts and images into showcase results.

    worker/.venv/bin/python scripts/showcase/report.py --root showcase

For every run it finds the agent's Claude Code transcript (by its brief path) and
writes claude_usage.json and agent_report.md next to the edit. Then it writes:

- showcase/results/results.json   every number, per case and in total
- showcase/results/summary.md     tables for the README
- showcase/results/gallery.html   local side-by-side gallery at full size
- docs/assets/showcase/<case>.jpg 2x2 comparison panels for the README
"""

from __future__ import annotations

import argparse
import html
import json
import sys
from pathlib import Path

from PIL import Image, ImageDraw, ImageFont

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent
sys.path.insert(0, str(HERE))
from claude_usage import summarize  # noqa: E402

CONDS = ("baseline", "doppio")
LABEL = {"baseline": "Without doppio", "doppio": "With doppio"}
TRANSCRIPTS = Path.home() / ".claude" / "projects" / ("-" + str(REPO).strip("/").replace("/", "-"))


def find_transcript(brief: Path) -> Path | None:
    """Latest subagent transcript whose first user message points at this brief."""
    hits = []
    for t in TRANSCRIPTS.glob("*/subagents/agent-*.jsonl"):
        with t.open() as f:
            for line in f:
                row = json.loads(line)
                if row.get("type") == "user":
                    content = row["message"]["content"]
                    text = content if isinstance(content, str) else " ".join(c.get("text", "") for c in content if isinstance(c, dict))
                    if str(brief) in text:
                        hits.append(t)
                    break
    return max(hits, key=lambda p: p.stat().st_mtime) if hits else None


def final_text(transcript: Path) -> str:
    last = ""
    for line in transcript.read_text().splitlines():
        row = json.loads(line)
        msg = row.get("message")
        if row.get("type") == "assistant" and isinstance(msg, dict):
            texts = [c["text"] for c in msg.get("content", []) if c.get("type") == "text" and c.get("text", "").strip()]
            if texts:
                last = "\n".join(texts)
    return last


def collect_run(run: Path) -> dict | None:
    gem_path = run / "gemini.json"
    if not gem_path.exists():
        return None
    t = find_transcript(run / "brief.md")
    claude = summarize(t) if t else None
    if claude:
        (run / "claude_usage.json").write_text(json.dumps(claude, indent=2))
        (run / "agent_report.md").write_text(final_text(t))
    gem = json.loads(gem_path.read_text())
    tok = {"input_text": 0, "input_image": 0}
    for a in gem["attempts"]:
        for d in a["usage"].get("promptTokensDetails", []):
            tok["input_" + d["modality"].lower()] = tok.get("input_" + d["modality"].lower(), 0) + d["tokenCount"]
    out = {
        "claude": claude and {
            "model": claude["model"], "api_requests": claude["api_requests"], "tool_calls": claude["tool_calls"],
            "wall_time_s": claude["wall_time_s"], **claude["usage"], "cost_usd": claude["cost"]["total_usd"],
        },
        "gemini": {
            "attempts": gem["totals"]["attempts"], "input_tokens": gem["totals"]["input_tokens"], **tok,
            "output_tokens": gem["totals"]["output_tokens"], "latency_s": gem["totals"]["latency_s"],
            "cost_usd": gem["totals"]["cost_usd"], "attached": [Path(i["path"]).name for i in gem["inputs"][1:]],
            "prompt_chars": gem["prompt_chars"],
        },
        "edited": str(run / "edited.png") if (run / "edited.png").exists() else None,
        "prompt": gem["prompt"],
    }
    pack = run / "pack"
    if (pack / "scene.json").exists():
        scene = json.loads((pack / "scene.json").read_text())
        manifest = json.loads((pack / "manifest.json").read_text())
        out["doppio"] = {
            "analysis_s": round(sum(s.get("elapsed_seconds", 0) for s in manifest["stages"]), 2),
            "fov_x_deg": round(scene["camera"]["fov_x_deg"], 1),
            "focal_35mm": round(scene["camera"]["focal_35mm_equiv"]),
            "depth_m": [round(scene["depth_stats"][k], 2) for k in ("near_m", "median_m", "far_m")],
            "warnings": scene["quality"]["warnings"],
        }
    out["total_cost_usd"] = round((claude["cost"]["total_usd"] if claude else 0) + gem["totals"]["cost_usd"], 6)
    return out


def panel(path: Path, label: str, width: int, font) -> Image.Image:
    im = Image.open(path).convert("RGB")
    im = im.resize((width, round(im.height * width / im.width)), Image.LANCZOS)
    bar = 34
    out = Image.new("RGB", (width, im.height + bar), (24, 24, 24))
    out.paste(im, (0, bar))
    ImageDraw.Draw(out).text((10, 5), label, fill=(240, 240, 240), font=font)
    return out


def comparison(case: str, root: Path, verdict: str | None, dest: Path) -> None:
    font = ImageFont.load_default(size=22)
    w = 640
    pick = {c: (" - judge's pick" if verdict == c else "") for c in CONDS}
    top = [panel(root / "cases" / case / "original.jpg", "Original photo", w, font),
           panel(root / "runs" / "doppio" / case / "pack" / "maps" / "depth_inv8.png", "doppio depth (brighter = closer)", w, font)]
    bottom = [panel(root / "runs" / c / case / "edited.png", LABEL[c] + pick[c], w, font) for c in CONDS]
    rows = [top, bottom]
    heights = [max(p.height for p in r) for r in rows]
    gap = 6
    sheet = Image.new("RGB", (2 * w + gap, sum(heights) + gap), "white")
    y = 0
    for r, h in zip(rows, heights):
        for k, p in enumerate(r):
            sheet.paste(p, (k * (w + gap), y))
        y += h + gap
    dest.parent.mkdir(parents=True, exist_ok=True)
    sheet.save(dest, "JPEG", quality=84, optimize=True, progressive=True)


def pct(new: float, old: float) -> str:
    return f"{(new - old) / old * 100:+.0f}%" if old else "n/a"


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", type=Path, default=REPO / "showcase")
    ap.add_argument("--assets", type=Path, default=REPO / "docs" / "assets" / "showcase")
    args = ap.parse_args()
    root = args.root.resolve()
    requests = json.loads((HERE / "requests.json").read_text())

    cases = []
    for case in sorted(requests):
        runs = {c: collect_run(root / "runs" / c / case) for c in CONDS}
        if not all(runs.values()):
            continue
        meta = json.loads((root / "cases" / case / "meta.json").read_text())
        jp = root / "judge" / f"{case}.json"
        judge = json.loads(jp.read_text()) if jp.exists() else None
        cases.append({"case": case, "request": requests[case], "source": meta, "runs": runs, "judge": judge})
        if all(runs[c]["edited"] for c in CONDS):
            comparison(case, root, judge and judge["winner"], args.assets / f"{case}.jpg")

    def tot(cond: str, path: str) -> float:
        a, b = path.split(".")
        return sum((c["runs"][cond][a] or {}).get(b, 0) for c in cases)

    keys = ["claude.total_input_tokens", "claude.output_tokens", "claude.cost_usd", "claude.wall_time_s", "claude.api_requests",
            "gemini.input_tokens", "gemini.output_tokens", "gemini.cost_usd"]
    totals = {c: {k: tot(c, k) for k in keys} for c in CONDS}
    for c in CONDS:
        totals[c]["total_cost_usd"] = sum(x["runs"][c]["total_cost_usd"] for x in cases)
    judged = [c for c in cases if c["judge"]]
    wins = {w: sum(1 for c in judged if c["judge"]["winner"] == w) for w in ("doppio", "baseline", "tie", "split")}
    criteria = list(judged[0]["judge"]["mean_scores"]["doppio"]) if judged else []
    mean = {c: {k: sum(j["judge"]["mean_scores"][c][k] for j in judged) / len(judged) for k in criteria} for c in CONDS} if judged else {}
    judge_cost = sum(c["judge"]["cost_usd"] for c in judged)
    results = {"cases": cases, "totals": totals, "judge": {"wins": wins, "mean_scores": mean, "cost_usd": judge_cost, "n": len(judged)}}
    out = root / "results"
    out.mkdir(exist_ok=True)
    (out / "results.json").write_text(json.dumps(results, indent=2))

    # summary.md
    n = len(cases)
    b, d = totals["baseline"], totals["doppio"]
    lines = [f"Cases: {n}. Judged: {len(judged)} (judge cost ${judge_cost:.2f}).", ""]
    if judged:
        lines += ["| Judge verdict (both orders agree) | Cases |", "|---|---|"]
        lines += [f"| doppio wins | {wins['doppio']} |", f"| baseline wins | {wins['baseline']} |",
                  f"| tie | {wins['tie']} |", f"| split (orders disagree) | {wins['split']} |", ""]
        lines += ["| Criterion (mean, 1-10) | Without doppio | With doppio | Change |", "|---|---|---|---|"]
        lines += [f"| {k.replace('_', ' ')} | {mean['baseline'][k]:.1f} | {mean['doppio'][k]:.1f} | {mean['doppio'][k] - mean['baseline'][k]:+.1f} |" for k in criteria]
        lines.append("")
    rows = [("Claude input tokens (incl. cache)", "claude.total_input_tokens", "{:,.0f}"), ("Claude output tokens", "claude.output_tokens", "{:,.0f}"),
            ("Claude API requests", "claude.api_requests", "{:,.0f}"), ("Claude cost", "claude.cost_usd", "${:.2f}"),
            ("Gemini input tokens", "gemini.input_tokens", "{:,.0f}"), ("Gemini output tokens", "gemini.output_tokens", "{:,.0f}"),
            ("Gemini cost", "gemini.cost_usd", "${:.3f}"), ("Agent wall time", "claude.wall_time_s", "{:,.0f} s")]
    lines += [f"| Total over {n} edits | Without doppio | With doppio | Change |", "|---|---|---|---|"]
    lines += [f"| {name} | {fmt.format(b[k])} | {fmt.format(d[k])} | {pct(d[k], b[k])} |" for name, k, fmt in rows]
    lines += [f"| **Total cost** | **${b['total_cost_usd']:.2f}** | **${d['total_cost_usd']:.2f}** | **{pct(d['total_cost_usd'], b['total_cost_usd'])}** |", ""]
    lines += ["| Case | Judge | Claude in/out tokens (without / with) | Cost (without / with) | doppio analysis |", "|---|---|---|---|---|"]
    for c in cases:
        rb, rd = c["runs"]["baseline"], c["runs"]["doppio"]
        cb, cd = rb["claude"] or {}, rd["claude"] or {}
        lines.append(
            f"| {c['case']} | {c['judge']['winner'] if c['judge'] else '-'} | "
            f"{cb.get('total_input_tokens', 0):,} / {cb.get('output_tokens', 0):,} vs {cd.get('total_input_tokens', 0):,} / {cd.get('output_tokens', 0):,} | "
            f"${rb['total_cost_usd']:.3f} / ${rd['total_cost_usd']:.3f} | {rd.get('doppio', {}).get('analysis_s', '-')} s |"
        )
    (out / "summary.md").write_text("\n".join(lines) + "\n")

    # gallery.html
    parts = ["<!doctype html><meta charset=utf-8><title>doppio showcase</title><style>body{font:14px system-ui;margin:24px;background:#111;color:#ddd}"
             "img{width:100%}.row{display:grid;grid-template-columns:repeat(4,1fr);gap:8px}pre{white-space:pre-wrap;background:#1b1b1b;padding:8px;font-size:12px}"
             "a{color:#8cf}</style><h1>doppio showcase</h1><pre>" + html.escape("\n".join(lines)) + "</pre>"]
    for c in cases:
        rel = lambda p: Path(p).resolve().relative_to(root).as_posix() if p else ""  # noqa: E731
        imgs = [("Original", f"../cases/{c['case']}/original.jpg"), ("doppio depth", f"../runs/doppio/{c['case']}/pack/maps/depth_inv8.png")]
        imgs += [(LABEL[k], "../" + rel(c["runs"][k]["edited"])) for k in CONDS]
        src = c["source"]
        parts.append(f"<h2>{c['case']}</h2><p>{html.escape(c['request'])}</p><p>Photo: <a href='{src['page']}'>{html.escape(src['title'])}</a>, "
                     f"{html.escape(src['artist'])}, {src['license']}</p><div class=row>"
                     + "".join(f"<figure><img src='{s}'><figcaption>{t}</figcaption></figure>" for t, s in imgs) + "</div>")
        if c["judge"]:
            parts.append("<pre>" + html.escape("\n".join(f"[{r['order']['A']} as A] winner {r['winner']}: {r['verdict']['reason']}" for r in c["judge"]["rounds"])) + "</pre>")
        for k in CONDS:
            parts.append(f"<details><summary>{LABEL[k]} prompt</summary><pre>{html.escape(c['runs'][k]['prompt'])}</pre></details>")
    (out / "gallery.html").write_text("\n".join(parts))
    print((out / "summary.md").read_text())


if __name__ == "__main__":
    main()
