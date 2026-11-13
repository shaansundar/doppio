"""geometry: monocular metric geometry (point map, depth, normals, intrinsics).

Outputs are in the OpenCV camera frame (x right, y down, z forward), metres.
Invalid pixels (sky, no data) are NaN in ``points``/``depth`` and 0 in
``normal``; ``mask`` marks valid pixels.
"""

from __future__ import annotations

import math
import time
from typing import Any

import numpy as np

from doppio_worker.stages.base import (
    ArtifactOut,
    ModelRef,
    StageContext,
    StageDependencyError,
    StageError,
    StageInputError,
    StageOutput,
)

SUPPORTED_FAMILIES = ("moge2",)


def intrinsics_to_camera(k_normalized: np.ndarray, width: int, height: int) -> dict[str, float]:
    """Pixel intrinsics and field of view from a normalized 3x3 intrinsics matrix."""
    fx_n, fy_n = float(k_normalized[0, 0]), float(k_normalized[1, 1])
    cx_n, cy_n = float(k_normalized[0, 2]), float(k_normalized[1, 2])
    if fx_n <= 0 or fy_n <= 0:
        raise StageError("model returned non-positive focal length")
    return {
        "fx": fx_n * width,
        "fy": fy_n * height,
        "cx": cx_n * width,
        "cy": cy_n * height,
        "fov_x_deg": 2.0 * math.degrees(math.atan(0.5 / fx_n)),
        "fov_y_deg": 2.0 * math.degrees(math.atan(0.5 / fy_n)),
    }


def depth_statistics(depth: np.ndarray, valid: np.ndarray) -> dict[str, Any]:
    """Robust depth statistics plus a coarse sky heuristic."""
    total = depth.size
    n_valid = int(valid.sum())
    stats: dict[str, Any] = {"valid_fraction": n_valid / total if total else 0.0}
    if n_valid:
        values = depth[valid].astype(np.float64)
        p1, p50, p99 = np.percentile(values, [1, 50, 99])
        stats.update(
            near_m=float(p1),
            median_m=float(p50),
            far_m=float(p99),
            min_m=float(values.min()),
            max_m=float(values.max()),
        )
    # Invalid pixels concentrated in the top third of the frame usually mean sky.
    top = valid[: max(1, valid.shape[0] // 3)]
    stats["top_third_invalid_fraction"] = float(1.0 - top.mean()) if top.size else 0.0
    stats["likely_sky"] = stats["top_third_invalid_fraction"] > 0.05
    return stats


def _load_moge2(spec: ModelRef, device: str) -> Any:
    try:
        from moge.model.v2 import MoGeModel
    except ImportError as exc:
        raise StageDependencyError(
            "the MoGe geometry backend is not installed; run `doppio setup` "
            "(installs the worker's geometry-moge extra)"
        ) from exc
    kwargs = {"revision": spec.revision} if spec.revision else {}
    model = MoGeModel.from_pretrained(spec.hf_repo, **kwargs)
    return model.to(device).eval()


def _infer_moge2(model: Any, image: np.ndarray, device: str, params: dict[str, Any]) -> dict[str, np.ndarray]:
    import torch

    tensor = torch.from_numpy(image).to(device=device, dtype=torch.float32).div_(255.0).permute(2, 0, 1)
    kwargs: dict[str, Any] = {"resolution_level": int(params.get("resolution_level", 9))}
    if (fov_x := params.get("fov_x_deg")) is not None:
        kwargs["fov_x"] = float(fov_x)
    use_fp16 = bool(params.get("use_fp16", device != "cpu"))
    with torch.inference_mode():
        try:
            out = model.infer(tensor, use_fp16=use_fp16, **kwargs)
        except RuntimeError:
            if not use_fp16:
                raise
            out = model.infer(tensor, use_fp16=False, **kwargs)
            params["_fp16_fallback"] = True
    return {k: v.detach().float().cpu().numpy() for k, v in out.items()}


def run(ctx: StageContext) -> StageOutput:
    spec = ctx.require_model()
    if spec.family not in SUPPORTED_FAMILIES:
        raise StageInputError(f"unsupported geometry model family {spec.family!r}")
    from PIL import Image

    with Image.open(ctx.input_path("image")) as img:
        image = np.asarray(img.convert("RGB"))
    height, width = image.shape[:2]

    assert ctx.models is not None  # guaranteed by require_model
    ctx.report(0.05, f"loading {spec.id}")
    model = ctx.models.get(spec, _load_moge2)
    ctx.check_cancelled()

    params = dict(ctx.params)
    ctx.report(0.2, "inferring geometry")
    started = time.perf_counter()
    with ctx.models.gpu_lock:
        out = _infer_moge2(model, image, ctx.device, params)
    infer_seconds = time.perf_counter() - started
    ctx.check_cancelled()

    mask = out["mask"].astype(bool)
    depth = out["depth"].astype(np.float32)
    valid = mask & np.isfinite(depth) & (depth > 0)
    depth[~valid] = np.nan
    points = out["points"].astype(np.float32)
    points[~valid] = np.nan

    np.save(ctx.out_dir / "points.npy", points)
    np.save(ctx.out_dir / "depth.npy", depth)
    np.save(ctx.out_dir / "mask.npy", valid)
    artifacts = [
        ArtifactOut("points", "points.npy", "application/x-npy", (height, width, 3), "float32"),
        ArtifactOut("depth", "depth.npy", "application/x-npy", (height, width), "float32"),
        ArtifactOut("mask", "mask.npy", "application/x-npy", (height, width), "bool"),
    ]
    if "normal" in out:
        normal = out["normal"].astype(np.float32)
        normal[~valid] = 0.0
        np.save(ctx.out_dir / "normal.npy", normal)
        artifacts.append(ArtifactOut("normal", "normal.npy", "application/x-npy", (height, width, 3), "float32"))

    camera = intrinsics_to_camera(out["intrinsics"], width, height)
    ctx.report(1.0, "geometry done")
    return StageOutput(
        artifacts=artifacts,
        metadata={
            "width": width,
            "height": height,
            "camera": camera,
            "fov_source": "prior" if params.get("fov_x_deg") is not None else "model",
            "metric_scale": True,
            "depth_stats": depth_statistics(depth, valid),
            "inference_seconds": infer_seconds,
            "fp16": bool(params.get("use_fp16", ctx.device != "cpu")) and not params.get("_fp16_fallback", False),
            "model_id": spec.id,
        },
    )
