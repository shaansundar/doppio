"""Edit a photo with Gemini 2.5 Flash Image and log exact token usage and cost.

Both showcase agents call this script, so input sizing, output settings and
accounting are identical with and without doppio:

    worker/.venv/bin/python scripts/showcase/gemini_edit.py \
        --image photo.jpg [--attach depth_inv8.png] \
        --prompt-file prompt.txt --out edited.png --log gemini.json

Every image is resized to at most 1536 px on its long side before upload. The
output aspect ratio is the supported ratio closest to the photo's. The API key is
read from GEMINI_API_TOKEN or GOOGLE_AI_API_KEY and never printed.
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
from datetime import datetime, timezone
from pathlib import Path

from PIL import Image

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pricing import gemini_cost  # noqa: E402

MODEL = "gemini-2.5-flash-image"
ENDPOINT = f"https://generativelanguage.googleapis.com/v1beta/models/{MODEL}:generateContent"
MAX_SIDE = 1536
RATIOS = ["1:1", "2:3", "3:2", "3:4", "4:3", "4:5", "5:4", "9:16", "16:9", "21:9"]
MAX_ATTEMPTS = 3  # attempts that return no image are retried; all usage is counted


def closest_ratio(w: int, h: int) -> str:
    def value(r: str) -> float:
        a, b = r.split(":")
        return int(a) / int(b)

    return min(RATIOS, key=lambda r: abs(value(r) - w / h))


def encode(path: Path) -> tuple[dict, dict]:
    im = Image.open(path)
    im.load()
    orig = im.size
    im.thumbnail((MAX_SIDE, MAX_SIDE))
    buf = io.BytesIO()
    if path.suffix.lower() in (".jpg", ".jpeg"):
        im.convert("RGB").save(buf, "JPEG", quality=92)
        mime = "image/jpeg"
    else:
        im.save(buf, "PNG")
        mime = "image/png"
    part = {"inlineData": {"mimeType": mime, "data": base64.b64encode(buf.getvalue()).decode()}}
    info = {"path": str(path), "original_size": list(orig), "sent_size": list(im.size), "bytes": buf.tell(), "mime": mime}
    return part, info


def post(body: dict, key: str) -> tuple[dict, float]:
    req = urllib.request.Request(
        ENDPOINT,
        data=json.dumps(body).encode(),
        headers={"Content-Type": "application/json", "x-goog-api-key": key},
    )
    for attempt in range(5):
        t0 = time.monotonic()
        try:
            with urllib.request.urlopen(req, timeout=180) as r:
                return json.load(r), time.monotonic() - t0
        except urllib.error.HTTPError as e:
            if e.code in (429, 500, 502, 503, 504) and attempt < 4:
                time.sleep(5 * 2**attempt)
                continue
            raise SystemExit(f"Gemini API error {e.code}: {e.read().decode()[:500]}") from None
    raise SystemExit("Gemini API: retries exhausted")


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--image", required=True, type=Path, help="photo to edit (sent first)")
    ap.add_argument("--attach", action="append", default=[], type=Path, help="extra reference image, in order")
    ap.add_argument("--prompt-file", required=True, type=Path)
    ap.add_argument("--out", required=True, type=Path, help="where to write the edited image (.png)")
    ap.add_argument("--log", required=True, type=Path, help="where to write the usage/cost JSON")
    args = ap.parse_args()

    key = os.environ.get("GEMINI_API_TOKEN") or os.environ.get("GOOGLE_AI_API_KEY")
    if not key:
        raise SystemExit("set GEMINI_API_TOKEN or GOOGLE_AI_API_KEY")
    if args.out.exists():
        raise SystemExit(f"{args.out} exists; each edit is single-shot, refusing to overwrite")

    prompt = args.prompt_file.read_text()
    photo_part, photo_info = encode(args.image)
    parts, inputs = [photo_part], [photo_info]
    for p in args.attach:
        part, info = encode(p)
        parts.append(part)
        inputs.append(info)
    parts.append({"text": prompt})
    w, h = photo_info["original_size"]
    body = {
        "contents": [{"role": "user", "parts": parts}],
        "generationConfig": {"responseModalities": ["IMAGE"], "imageConfig": {"aspectRatio": closest_ratio(w, h)}},
    }

    attempts, image_bytes = [], None
    for _ in range(MAX_ATTEMPTS):
        resp, latency = post(body, key)
        usage = resp.get("usageMetadata", {})
        cand = (resp.get("candidates") or [{}])[0]
        found = [p for p in cand.get("content", {}).get("parts", []) if "inlineData" in p]
        attempts.append({
            "latency_s": round(latency, 2),
            "finish_reason": cand.get("finishReason"),
            "returned_image": bool(found),
            "usage": usage,
            "cost": gemini_cost(MODEL, usage),
        })
        if found:
            image_bytes = base64.b64decode(found[0]["inlineData"]["data"])
            break

    def total(field: str) -> int:
        return sum(a["usage"].get(field, 0) for a in attempts)

    log = {
        "model": MODEL,
        "finished_at": datetime.now(timezone.utc).isoformat(timespec="seconds"),
        "aspect_ratio": body["generationConfig"]["imageConfig"]["aspectRatio"],
        "inputs": inputs,
        "prompt_chars": len(prompt),
        "prompt": prompt,
        "attempts": attempts,
        "totals": {
            "attempts": len(attempts),
            "input_tokens": total("promptTokenCount"),
            "output_tokens": total("candidatesTokenCount"),
            "latency_s": round(sum(a["latency_s"] for a in attempts), 2),
            "cost_usd": round(sum(a["cost"]["total_usd"] for a in attempts), 6),
        },
        "output": None,
    }
    if image_bytes:
        args.out.parent.mkdir(parents=True, exist_ok=True)
        out = Image.open(io.BytesIO(image_bytes))
        out.save(args.out)
        log["output"] = {"path": str(args.out), "size": list(out.size)}
    args.log.parent.mkdir(parents=True, exist_ok=True)
    args.log.write_text(json.dumps(log, indent=2))

    t = log["totals"]
    status = f"wrote {args.out}" if image_bytes else "NO IMAGE RETURNED"
    print(f"{status} | attempts {t['attempts']} | input {t['input_tokens']} tok | output {t['output_tokens']} tok | ${t['cost_usd']:.4f} | {t['latency_s']}s")
    if not image_bytes:
        sys.exit(1)


if __name__ == "__main__":
    main()
