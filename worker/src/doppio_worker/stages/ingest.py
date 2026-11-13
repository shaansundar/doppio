"""ingest: decode an image into a normalized working copy plus camera priors.

Produces an EXIF-oriented, sRGB, 8-bit RGB PNG resized so its long side is at
most ``max_side``, and metadata including an EXIF-derived field-of-view prior.
"""

from __future__ import annotations

import io
import math
from typing import Any

import numpy as np
from PIL import Image, ImageCms, ImageOps, UnidentifiedImageError

from doppio_worker.stages.base import ArtifactOut, StageContext, StageInputError, StageOutput

# Full-frame 35 mm film diagonal in millimetres: sqrt(36^2 + 24^2).
FULL_FRAME_DIAGONAL_MM = math.hypot(36.0, 24.0)

DEFAULT_MAX_SIDE = 2048
DEFAULT_MAX_PIXELS = 100_000_000

_EXIF_IFD = 0x8769
_TAG_MAKE = 271
_TAG_MODEL = 272
_TAG_ORIENTATION = 274
_TAG_FOCAL_LENGTH = 37386
_TAG_FOCAL_LENGTH_35MM = 41989

try:  # HEIC/HEIF support is optional at import time.
    from pillow_heif import register_heif_opener

    register_heif_opener()
except ImportError:  # pragma: no cover - dependency is declared, guard anyway
    pass


def fov_from_35mm(focal_35mm: float, width: int, height: int) -> dict[str, float]:
    """Field of view implied by a 35 mm-equivalent focal length.

    The 35 mm equivalent is defined so the image diagonal maps to the full-frame
    diagonal, which keeps this correct for any aspect ratio.
    """
    if focal_35mm <= 0 or width <= 0 or height <= 0:
        raise ValueError("focal length and image size must be positive")
    diagonal_px = math.hypot(width, height)
    focal_px = diagonal_px * focal_35mm / FULL_FRAME_DIAGONAL_MM
    fov_x = 2.0 * math.degrees(math.atan(width / (2.0 * focal_px)))
    fov_y = 2.0 * math.degrees(math.atan(height / (2.0 * focal_px)))
    return {"fov_x_deg": fov_x, "fov_y_deg": fov_y, "focal_px": focal_px}


def _rational(value: Any) -> float | None:
    try:
        number = float(value)
    except (TypeError, ValueError, ZeroDivisionError):
        return None
    if not math.isfinite(number) or number <= 0:
        return None
    return number


def read_exif(img: Image.Image) -> dict[str, Any]:
    exif = img.getexif()
    sub = exif.get_ifd(_EXIF_IFD) if exif else {}
    out: dict[str, Any] = {}
    if make := exif.get(_TAG_MAKE):
        out["make"] = str(make).strip("\x00 ")
    if model := exif.get(_TAG_MODEL):
        out["model"] = str(model).strip("\x00 ")
    if (orientation := exif.get(_TAG_ORIENTATION)) is not None:
        out["orientation"] = int(orientation)
    if (focal := _rational(sub.get(_TAG_FOCAL_LENGTH))) is not None:
        out["focal_length_mm"] = focal
    if (focal35 := _rational(sub.get(_TAG_FOCAL_LENGTH_35MM))) is not None:
        out["focal_length_35mm"] = focal35
    return out


def _to_srgb_rgb(img: Image.Image) -> tuple[Image.Image, bool, bool]:
    """Return (8-bit sRGB RGB image, had_alpha, icc_converted)."""
    had_alpha = img.mode in ("RGBA", "LA", "PA") or (img.mode == "P" and "transparency" in img.info)

    if img.mode in ("I;16", "I;16B", "I;16L", "I"):
        arr = np.asarray(img, dtype=np.float64)
        peak = 65535.0 if img.mode.startswith("I;16") else max(float(arr.max()), 1.0)
        img = Image.fromarray(np.clip(arr / peak * 255.0, 0, 255).astype(np.uint8), mode="L")
    elif img.mode == "F":
        arr = np.asarray(img, dtype=np.float64)
        img = Image.fromarray(np.clip(arr * 255.0, 0, 255).astype(np.uint8), mode="L")

    icc_converted = False
    icc = img.info.get("icc_profile")
    if icc:
        try:
            src = ImageCms.ImageCmsProfile(io.BytesIO(icc))
            dst = ImageCms.createProfile("sRGB")
            base = img.convert("RGBA" if had_alpha else "RGB") if img.mode not in ("RGB", "RGBA", "CMYK") else img
            converted = ImageCms.profileToProfile(base, src, dst, outputMode="RGB")
            if converted is not None:
                img = converted
                icc_converted = True
        except (ImageCms.PyCMSError, OSError, ValueError):
            icc_converted = False

    return img.convert("RGB"), had_alpha, icc_converted


def run(ctx: StageContext) -> StageOutput:
    source = ctx.input_path("source")
    max_side = int(ctx.param("max_side", DEFAULT_MAX_SIDE))
    max_pixels = int(ctx.param("max_pixels", DEFAULT_MAX_PIXELS))
    if max_side < 64:
        raise StageInputError("max_side must be at least 64")

    Image.MAX_IMAGE_PIXELS = max_pixels
    try:
        with Image.open(source) as opened:
            fmt = opened.format or "unknown"
            exif = read_exif(opened)
            oriented = ImageOps.exif_transpose(opened)
            oriented.load()
    except Image.DecompressionBombError as exc:
        raise StageInputError(f"image exceeds the {max_pixels} pixel limit") from exc
    except (UnidentifiedImageError, OSError) as exc:
        raise StageInputError(f"cannot decode image: {exc}") from exc

    ctx.check_cancelled()
    original_w, original_h = oriented.size
    rgb, had_alpha, icc_converted = _to_srgb_rgb(oriented)

    scale = min(1.0, max_side / max(original_w, original_h))
    if scale < 1.0:
        size = (max(1, round(original_w * scale)), max(1, round(original_h * scale)))
        rgb = rgb.resize(size, Image.Resampling.LANCZOS)
    width, height = rgb.size

    out_path = ctx.out_dir / "image.png"
    rgb.save(out_path, format="PNG")
    ctx.report(1.0, "decoded")

    metadata: dict[str, Any] = {
        "format": fmt,
        "original_width": original_w,
        "original_height": original_h,
        "width": width,
        "height": height,
        "scale": width / original_w,
        "had_alpha": had_alpha,
        "icc_converted": icc_converted,
        "exif": exif,
    }
    if "focal_length_35mm" in exif:
        prior = fov_from_35mm(exif["focal_length_35mm"], width, height)
        metadata["fov_prior"] = {"source": "exif_35mm", **prior}

    return StageOutput(
        artifacts=[ArtifactOut("image", "image.png", "image/png", (height, width, 3), "uint8")],
        metadata=metadata,
    )
