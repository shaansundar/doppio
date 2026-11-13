"""Blind pairwise judging of showcase edits with Gemini 3.1 Pro.

For each case the judge sees the original photo, the edit request and two edited
images labelled only "A" and "B". It never learns which agent made which, or that
doppio exists. Every pair is judged twice with the order swapped, to cancel
position bias; a condition wins a case only if it wins both orders.

    worker/.venv/bin/python scripts/showcase/judge.py --root showcase
"""

from __future__ import annotations

import argparse
import base64
import io
import json
import os
import sys
import time
import urllib.error
import urllib.request
from pathlib import Path

from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pricing import gemini_cost  # noqa: E402

MODEL = "gemini-3.1-pro-preview"
ENDPOINT = f"https://generativelanguage.googleapis.com/v1beta/models/{MODEL}:generateContent"
MAX_SIDE = 1536
CRITERIA = ["instruction_following", "placement_and_contact", "scale", "perspective", "occlusion", "lighting_and_shadows", "preservation", "overall"]

RUBRIC = """You are an expert photo retoucher and compositor judging two AI edits of the same photo.

The FIRST image is the original photo. The SECOND image is edit A. The THIRD image is edit B.
Both edits were asked to do the same thing:

EDIT REQUEST: {request}

Score each edit from 1 (very poor) to 10 (indistinguishable from a real photo) on:
- instruction_following: every requested object is present, where the request says, with the stated real-world size.
- placement_and_contact: added objects rest on a plausible surface with correct contact (no floating, sinking or clipping).
- scale: added objects have the right size for their distance, judged against nearby known-size things (doors, chairs, people, tiles).
- perspective: added objects follow the photo's camera, horizon and vanishing lines (and lens distortion, if any).
- occlusion: near/far ordering is right; things in front hide what is behind them.
- lighting_and_shadows: light direction, shadow softness, reflections and color temperature match the scene.
- preservation: everything that was not supposed to change (framing, people, objects, text) is unchanged.
- overall: how convincing and correct the whole edit is.

Then pick the better edit overall: "A", "B" or "tie". Judge only what you see. Be strict about scale and
perspective; do not reward an edit for being bolder or more detailed if its geometry is wrong."""

SCHEMA = {
    "type": "OBJECT",
    "properties": {
        "A": {"type": "OBJECT", "properties": {c: {"type": "INTEGER"} for c in CRITERIA}, "required": CRITERIA},
        "B": {"type": "OBJECT", "properties": {c: {"type": "INTEGER"} for c in CRITERIA}, "required": CRITERIA},
        "winner": {"type": "STRING", "enum": ["A", "B", "tie"]},
        "reason": {"type": "STRING"},
    },
    "required": ["A", "B", "winner", "reason"],
    "propertyOrdering": ["A", "B", "winner", "reason"],
}


def part(path: Path) -> dict:
    im = Image.open(path).convert("RGB")
    im.thumbnail((MAX_SIDE, MAX_SIDE))
    buf = io.BytesIO()
    im.save(buf, "JPEG", quality=92)
    return {"inlineData": {"mimeType": "image/jpeg", "data": base64.b64encode(buf.getvalue()).decode()}}


def call(body: dict, key: str) -> tuple[dict, float]:
    req = urllib.request.Request(ENDPOINT, data=json.dumps(body).encode(), headers={"Content-Type": "application/json", "x-goog-api-key": key})
    for attempt in range(5):
        t0 = time.monotonic()
        try:
            with urllib.request.urlopen(req, timeout=300) as r:
                return json.load(r), time.monotonic() - t0
        except urllib.error.HTTPError as e:
            if e.code in (429, 500, 502, 503, 504) and attempt < 4:
                time.sleep(5 * 2**attempt)
                continue
            raise SystemExit(f"Gemini API error {e.code}: {e.read().decode()[:500]}") from None
    raise SystemExit("Gemini API: retries exhausted")


def judge_once(original: Path, a: Path, b: Path, request: str, key: str) -> dict:
    body = {
        "contents": [{"role": "user", "parts": [part(original), part(a), part(b), {"text": RUBRIC.format(request=request)}]}],
        "generationConfig": {"responseMimeType": "application/json", "responseSchema": SCHEMA},
    }
    resp, latency = call(body, key)
    usage = resp.get("usageMetadata", {})
    text = "".join(p.get("text", "") for p in resp["candidates"][0]["content"]["parts"] if not p.get("thought"))
    return {"verdict": json.loads(text), "usage": usage, "cost": gemini_cost(MODEL, usage), "latency_s": round(latency, 2)}


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", type=Path, default=Path("showcase"))
    ap.add_argument("--requests", type=Path, default=Path(__file__).resolve().parent / "requests.json")
    ap.add_argument("--cases", nargs="*", help="case ids (default: every case with both edits)")
    args = ap.parse_args()
    key = os.environ.get("GEMINI_API_TOKEN") or os.environ.get("GOOGLE_AI_API_KEY")
    if not key:
        raise SystemExit("set GEMINI_API_TOKEN or GOOGLE_AI_API_KEY")

    requests = json.loads(args.requests.read_text())
    out_dir = args.root / "judge"
    out_dir.mkdir(parents=True, exist_ok=True)
    for case in args.cases or sorted(requests):
        edits = {c: args.root / "runs" / c / case / "edited.png" for c in ("baseline", "doppio")}
        out = out_dir / f"{case}.json"
        if not all(p.exists() for p in edits.values()) or out.exists():
            continue
        original = args.root / "cases" / case / "original.jpg"
        rounds = []
        for order in (("baseline", "doppio"), ("doppio", "baseline")):
            r = judge_once(original, edits[order[0]], edits[order[1]], requests[case], key)
            label = {"A": order[0], "B": order[1], "tie": "tie"}
            r["order"] = {"A": order[0], "B": order[1]}
            r["winner"] = label[r["verdict"]["winner"]]
            r["scores"] = {order[0]: r["verdict"]["A"], order[1]: r["verdict"]["B"]}
            rounds.append(r)
        winners = {r["winner"] for r in rounds}
        result = {
            "case": case,
            "model": MODEL,
            "rounds": rounds,
            "winner": winners.pop() if len(winners) == 1 else "split",
            "mean_scores": {
                cond: {c: sum(r["scores"][cond][c] for r in rounds) / len(rounds) for c in CRITERIA} for cond in ("baseline", "doppio")
            },
            "cost_usd": round(sum(r["cost"]["total_usd"] for r in rounds), 6),
        }
        out.write_text(json.dumps(result, indent=2))
        ms = result["mean_scores"]
        print(f"{case}: winner {result['winner']:8} overall baseline {ms['baseline']['overall']:.1f} doppio {ms['doppio']['overall']:.1f}  ${result['cost_usd']:.4f}")


if __name__ == "__main__":
    main()
