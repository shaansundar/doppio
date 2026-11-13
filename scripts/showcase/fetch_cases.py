"""Download the 10 showcase photos from Wikimedia Commons with their license metadata.

    python3 scripts/showcase/fetch_cases.py --root showcase

Writes showcase/cases/<case>/original.jpg and meta.json. Existing cases are skipped.
All photos are CC0 or CC BY; meta.json records the author, license and source page.
"""

from __future__ import annotations

import argparse
import json
import re
import time
import urllib.error
import urllib.parse
import urllib.request
from pathlib import Path

UA = "spatial-edit-benchmark/0.1 (research script)"
API = "https://commons.wikimedia.org/w/api.php?"
WIDTH = 3840

CASES = {
    "01-living-room": "Living room in apartment of Condomínio do Edifício Zaher, Le Blond, Rio de Janeiro, Brazil.jpg",
    "02-library": "NYC Public Library Research Room Jan 2006-1- 3.jpg",
    "03-station-hall-sanatorium": "Large room leading to the kitchen in Sanatorium du Basil, Stoumont, Belgium (DSCF3534-hdr).jpg",
    "04-station-plaza": "Warszawa Centralna train station, 2026, Warsaw, Poland.jpg",
    "05-restaurant": "Restaurant in Place D'Youville, Quebec City.jpg",
    "06-alley": "Rue du Souterrain depuis la rue des Rondes à Pérouges en avril 2021.jpg",
    "07-hotel-corridor": "Frontenac Hotel, Quebec ville, downtown 007.jpg",
    "08-courtyard": "Courtyard Zappeion Athens, Greece.jpg",
    "09-wide-angle-house": "Old house typical of the island of Margarita.jpg",
    "10-classroom": "Grande salle ENC n1.jpg",
}


def get(url: str, raw: bool = False):
    for attempt in range(6):
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers={"User-Agent": UA}), timeout=60) as r:
                return r.read() if raw else json.load(r)
        except urllib.error.HTTPError as e:
            if e.code != 429:
                raise
            time.sleep(int(e.headers.get("Retry-After") or 15 * (attempt + 1)))
    raise SystemExit("Wikimedia rate limit: try again later")


def clean(s: str | None) -> str:
    return re.sub(r"\s+", " ", re.sub("<[^>]+>", "", s or "")).strip()


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", type=Path, default=Path(__file__).resolve().parents[2] / "showcase")
    args = ap.parse_args()
    for case, title in CASES.items():
        d = args.root / "cases" / case
        if (d / "original.jpg").exists():
            print(f"{case}: exists, skipped")
            continue
        q = {"action": "query", "format": "json", "titles": "File:" + title, "prop": "imageinfo",
             "iiprop": "url|size|extmetadata", "iiurlwidth": WIDTH}
        page = next(iter(get(API + urllib.parse.urlencode(q))["query"]["pages"].values()))
        ii = page["imageinfo"][0]
        md = ii["extmetadata"]
        meta = {
            "title": page["title"], "page": ii["descriptionurl"],
            "license": md.get("LicenseShortName", {}).get("value"), "license_url": md.get("LicenseUrl", {}).get("value"),
            "artist": clean(md.get("Artist", {}).get("value")), "credit": clean(md.get("Credit", {}).get("value"))[:200],
            "width": ii["width"], "height": ii["height"], "thumb_url": ii["thumburl"],
        }
        d.mkdir(parents=True, exist_ok=True)
        (d / "original.jpg").write_bytes(get(ii["thumburl"], raw=True))
        (d / "meta.json").write_text(json.dumps(meta, indent=2, ensure_ascii=False))
        print(f"{case}: {meta['license']}, {meta['artist']}")
        time.sleep(2)


if __name__ == "__main__":
    main()
