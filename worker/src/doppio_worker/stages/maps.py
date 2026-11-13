"""maps: render conditioning maps from geometry arrays.

Conventions (see docs/conventions.md):
- Depth maps are *inverse* depth, normalized between robust percentiles, with
  near = bright and invalid (sky / no data) = 0. This matches the MiDaS-style
  input most depth ControlNets were trained on.
- Normal maps use the OpenGL convention: R = +x right, G = +y up, B = +z toward
  the viewer. Geometry normals arrive in the OpenCV camera frame (x right,
  y down, z forward), so (x, y, z)_gl = (x, -y, -z)_cv. Invalid pixels are 0.
"""

from __future__ import annotations

import cv2
import numpy as np

from doppio_worker.stages.base import ArtifactOut, StageContext, StageInputError, StageOutput

LOW_PERCENTILE = 0.5
HIGH_PERCENTILE = 99.5


def valid_depth_mask(depth: np.ndarray, mask: np.ndarray | None = None) -> np.ndarray:
    valid = np.isfinite(depth) & (depth > 0)
    if mask is not None:
        valid &= mask.astype(bool)
    return valid


def inverse_depth_normalized(depth: np.ndarray, valid: np.ndarray) -> np.ndarray:
    """float32 map in [0, 1]: robustly normalized inverse depth, 0 where invalid."""
    out = np.zeros(depth.shape, dtype=np.float32)
    if not valid.any():
        return out
    disparity = np.reciprocal(depth[valid], dtype=np.float32)
    lo, hi = np.percentile(disparity, [LOW_PERCENTILE, HIGH_PERCENTILE])
    span = float(hi - lo)
    if span <= 0:
        out[valid] = 1.0
        return out
    disparity -= lo
    disparity /= span
    out[valid] = np.clip(disparity, 0.0, 1.0)
    return out


def normal_to_rgb_gl(normal_cv: np.ndarray, valid: np.ndarray) -> np.ndarray:
    """uint8 HxWx3 normal map in the OpenGL convention.

    Works one channel at a time in float32 so temporaries stay at HxW.
    """
    n = normal_cv.astype(np.float32, copy=False)
    norm = np.linalg.norm(n, axis=-1)
    good = valid & (norm > 1e-8)
    # rgb = (n_gl / |n| + 1) / 2 * 255, with (x, y, z)_gl = (x, -y, -z)_cv
    scale = np.where(good, np.float32(127.5) / np.maximum(norm, np.float32(1e-8)), np.float32(0))
    rgb = np.empty(n.shape, dtype=np.uint8)
    for channel, sign in ((0, 1.0), (1, -1.0), (2, -1.0)):
        value = n[..., channel] * (scale * np.float32(sign))
        value += np.float32(127.5)
        np.clip(np.rint(value, out=value), 0, 255, out=value)
        rgb[..., channel] = value
    rgb[~good] = 0
    return rgb


def run(ctx: StageContext) -> StageOutput:
    depth = np.load(ctx.input_path("depth"))
    mask_path = ctx.optional_input("mask")
    mask = np.load(mask_path) if mask_path else None
    if depth.ndim != 2:
        raise StageInputError(f"depth must be HxW, got shape {depth.shape}")
    if mask is not None and mask.shape != depth.shape:
        raise StageInputError("mask shape does not match depth")
    h, w = depth.shape
    valid = valid_depth_mask(depth, mask)

    inv = inverse_depth_normalized(depth, valid)
    artifacts: list[ArtifactOut] = []

    cv2.imwrite(str(ctx.out_dir / "depth_inv16.png"), np.rint(inv * 65535.0).astype(np.uint16))
    artifacts.append(ArtifactOut("depth_inv16", "depth_inv16.png", "image/png", (h, w), "uint16"))
    cv2.imwrite(str(ctx.out_dir / "depth_inv8.png"), np.rint(inv * 255.0).astype(np.uint8))
    artifacts.append(ArtifactOut("depth_inv8", "depth_inv8.png", "image/png", (h, w), "uint8"))
    cv2.imwrite(str(ctx.out_dir / "valid_mask.png"), valid.astype(np.uint8) * 255)
    artifacts.append(ArtifactOut("valid_mask", "valid_mask.png", "image/png", (h, w), "uint8"))
    ctx.report(0.6, "depth maps written")
    ctx.check_cancelled()

    if (normal_path := ctx.optional_input("normal")) is not None:
        normal = np.load(normal_path)
        if normal.shape != (h, w, 3):
            raise StageInputError(f"normal must be HxWx3 matching depth, got {normal.shape}")
        rgb = normal_to_rgb_gl(normal, valid)
        cv2.imwrite(str(ctx.out_dir / "normal.png"), cv2.cvtColor(rgb, cv2.COLOR_RGB2BGR))
        artifacts.append(ArtifactOut("normal", "normal.png", "image/png", (h, w, 3), "uint8"))

    ctx.report(1.0, "maps written")
    return StageOutput(
        artifacts=artifacts,
        metadata={
            "depth_normalization": {
                "kind": "inverse_depth_percentile",
                "low_percentile": LOW_PERCENTILE,
                "high_percentile": HIGH_PERCENTILE,
            },
            "normal_convention": "opengl",
        },
    )
