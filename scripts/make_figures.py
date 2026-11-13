"""Regenerate README figures from a synthetic scene and doppio's real encoders.

    cd worker && uv run python ../scripts/make_figures.py

Writes docs/assets/cover.png (1280x640, also usable as the GitHub social
preview) and docs/assets/encoders.png. The scene is analytic (ray-cast), so
the figures show encoder conventions, not model accuracy.
"""

from __future__ import annotations

import math
import sys
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFont

from doppio_worker import synthetic
from doppio_worker.stages import geometry, maps

ROOT = Path(__file__).resolve().parents[1]
ASSETS = ROOT / "docs" / "assets"

BG_TOP, BG_BOTTOM = (13, 17, 23), (22, 28, 41)
TEXT, MUTED, ACCENT, CARD = (236, 239, 244), (139, 148, 163), (122, 162, 247), (30, 37, 51)


def _font(candidates: list[tuple[str, int]], size: int) -> ImageFont.FreeTypeFont:
    for path, index in candidates:
        try:
            return ImageFont.truetype(path, size, index=index)
        except OSError:
            continue
    sys.exit("no usable font found; edit _font candidates for your OS")


def fonts(size: int, kind: str = "sans", bold: bool = False) -> ImageFont.FreeTypeFont:
    if kind == "mono":
        return _font([("/System/Library/Fonts/Menlo.ttc", 0),
                      ("/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf", 0)], size)
    return _font([("/System/Library/Fonts/HelveticaNeue.ttc", 1 if bold else 0),
                  ("/System/Library/Fonts/Helvetica.ttc", 1 if bold else 0),
                  ("/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf" if bold
                   else "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf", 0)], size)


def panels(width: int = 960, height: int = 720) -> tuple[synthetic.Scene, dict[str, Image.Image]]:
    scene = synthetic.room(width, height, fov_x_deg=70, pitch_deg=10)
    depth = maps.inverse_depth_normalized(scene.depth, maps.valid_depth_mask(scene.depth, scene.mask))
    depth_rgb = np.repeat(np.rint(depth * 255).astype(np.uint8)[..., None], 3, axis=-1)
    normal_rgb = maps.normal_to_rgb_gl(scene.normal, scene.mask)
    return scene, {
        "render": Image.fromarray(scene.image),
        "depth": Image.fromarray(depth_rgb),
        "normal": Image.fromarray(normal_rgb),
    }


def gradient(size: tuple[int, int]) -> Image.Image:
    w, h = size
    t = np.linspace(0, 1, h)[:, None, None]
    rows = (np.array(BG_TOP) * (1 - t) + np.array(BG_BOTTOM) * t).astype(np.uint8)
    return Image.fromarray(np.broadcast_to(rows, (h, w, 3)).copy())


def rounded(img: Image.Image, radius: int) -> tuple[Image.Image, Image.Image]:
    mask = Image.new("L", img.size, 0)
    ImageDraw.Draw(mask).rounded_rectangle((0, 0, img.size[0] - 1, img.size[1] - 1), radius, fill=255)
    return img, mask


def scene_card(size: tuple[int, int], scene: synthetic.Scene) -> Image.Image:
    w, h = size
    card = Image.new("RGB", size, CARD)
    d = ImageDraw.Draw(card)
    mono = fonts(13, "mono")
    height, width = scene.image.shape[:2]
    fov_x = 2 * math.degrees(math.atan(scene.k[0, 2] / scene.k[0, 0]))
    f35 = scene.k[0, 0] * math.hypot(36, 24) / math.hypot(width, height)
    stats = geometry.depth_statistics(scene.depth, scene.mask)
    lines = [
        ("{", MUTED),
        ('  "camera": {', TEXT),
        (f'    "fov_x_deg": {fov_x:.1f},', ACCENT),
        (f'    "focal_35mm_equiv": {f35:.0f}', ACCENT),
        ("  },", TEXT),
        ('  "depth_stats": {', TEXT),
        (f'    "near_m": {stats["near_m"]:.2f},', ACCENT),
        (f'    "median_m": {stats["median_m"]:.2f},', ACCENT),
        (f'    "likely_sky": {str(stats["likely_sky"]).lower()}', ACCENT),
        ("  }", TEXT),
        ("}", MUTED),
    ]
    y = 12
    for text, color in lines:
        d.text((12, y), text, font=mono, fill=color)
        y += (h - 24) // len(lines)
    return card


def arrow(d: ImageDraw.ImageDraw, x: int, y: int, length: int, width: int = 3) -> None:
    d.line((x, y, x + length - 8, y), fill=ACCENT, width=width)
    d.polygon([(x + length - 10, y - 7), (x + length, y), (x + length - 10, y + 7)], fill=ACCENT)


def make_cover(scene: synthetic.Scene, imgs: dict[str, Image.Image]) -> Image.Image:
    W, H = 1280, 640
    cover = gradient((W, H))
    d = ImageDraw.Draw(cover)
    d.text((64, 48), "doppio", font=fonts(104, bold=True), fill=TEXT)
    d.text((68, 178), "3D spatial context for AI image generation and editing", font=fonts(34), fill=TEXT)
    d.text((68, 226), "local  ·  metric depth  ·  normals  ·  camera  ·  ControlNet-ready  ·  license-aware",
           font=fonts(22), fill=MUTED)

    pw, ph, gap, top = 258, 194, 40, 300
    items = [
        ("photo", imgs["render"]),
        ("maps/depth_inv16.png", imgs["depth"]),
        ("maps/normal.png", imgs["normal"]),
        ("scene.json", None),
    ]
    label_font = fonts(18)
    for i, (label, img) in enumerate(items):
        x = 64 + i * (pw + gap)
        tile = scene_card((pw, ph), scene) if img is None else img.resize((pw, ph), Image.Resampling.LANCZOS)
        tile, mask = rounded(tile, 12)
        cover.paste(tile, (x, top), mask)
        d.text((x + 2, top + ph + 14), label, font=label_font, fill=MUTED)
        if i < len(items) - 1:
            arrow(d, x + pw + 8, top + ph // 2, gap - 16)

    arrow(d, 64, 588, 34)
    d.text((110, 573), "your diffusion, multimodal-edit or 3D model", font=fonts(24, bold=True), fill=ACCENT)
    note = "synthetic scene, real doppio encoder output"
    nf = fonts(15)
    d.text((W - 64 - d.textlength(note, font=nf), 600), note, font=nf, fill=MUTED)
    return cover


def make_encoders(imgs: dict[str, Image.Image]) -> Image.Image:
    pw, ph, gap, bar = 480, 360, 12, 46
    out = Image.new("RGB", (3 * pw + 4 * gap, ph + bar + 2 * gap), BG_TOP)
    d = ImageDraw.Draw(out)
    f = fonts(18)
    labels = [("render", "synthetic render (input)"), ("depth", "maps/depth_inv16.png · near = bright"),
              ("normal", "maps/normal.png · R right, G up, B toward viewer")]
    for i, (key, label) in enumerate(labels):
        x = gap + i * (pw + gap)
        out.paste(imgs[key].resize((pw, ph), Image.Resampling.LANCZOS), (x, gap))
        d.text((x + 4, gap + ph + 12), label, font=f, fill=TEXT)
    return out


def main() -> None:
    ASSETS.mkdir(parents=True, exist_ok=True)
    scene, imgs = panels()
    make_cover(scene, imgs).save(ASSETS / "cover.png", optimize=True)
    make_encoders(imgs).save(ASSETS / "encoders.png", optimize=True)
    print(f"wrote {ASSETS / 'cover.png'} and {ASSETS / 'encoders.png'}")


if __name__ == "__main__":
    main()
