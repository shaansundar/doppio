import math

import numpy as np
import pytest

from doppio_worker.stages import geometry
from doppio_worker.stages.base import ModelRef, StageContext, StageError, StageInputError


def test_intrinsics_to_camera_converts_normalized_matrix():
    k = np.array([[0.5, 0, 0.5], [0, 0.75, 0.5], [0, 0, 1]])
    cam = geometry.intrinsics_to_camera(k, width=800, height=600)
    assert cam["fx"] == pytest.approx(400)
    assert cam["fy"] == pytest.approx(450)
    assert (cam["cx"], cam["cy"]) == (400, 300)
    assert cam["fov_x_deg"] == pytest.approx(90.0)
    assert cam["fov_y_deg"] == pytest.approx(2 * math.degrees(math.atan(0.5 / 0.75)))


def test_intrinsics_to_camera_rejects_bad_focal():
    with pytest.raises(StageError):
        geometry.intrinsics_to_camera(np.zeros((3, 3)), 10, 10)


def test_depth_statistics_reports_percentiles_and_sky():
    depth = np.tile(np.arange(1, 101, dtype=np.float32), (30, 1))
    valid = np.ones_like(depth, bool)
    valid[:10, :] = False  # top third invalid -> sky
    stats = geometry.depth_statistics(depth, valid)
    assert stats["valid_fraction"] == pytest.approx(2 / 3)
    assert stats["near_m"] < stats["median_m"] < stats["far_m"]
    assert stats["likely_sky"] is True


def test_depth_statistics_all_invalid():
    stats = geometry.depth_statistics(np.zeros((4, 4)), np.zeros((4, 4), bool))
    assert stats["valid_fraction"] == 0.0
    assert "median_m" not in stats


def test_run_requires_supported_model(tmp_path):
    ctx = StageContext(inputs={}, params={}, out_dir=tmp_path, device="cpu", models=object(),
                       model=ModelRef(id="x", family="unknown", hf_repo="r"))
    with pytest.raises(StageInputError, match="unsupported geometry model family"):
        geometry.run(ctx)
    with pytest.raises(StageInputError, match="requires a model"):
        geometry.run(StageContext(inputs={}, params={}, out_dir=tmp_path, device="cpu"))


@pytest.mark.model
def test_moge2_smoke(tmp_path):
    """Runs real MoGe-2 ViT-S inference. Requires the geometry-moge extra and network/weights."""
    from PIL import Image

    from doppio_worker.model_manager import ModelManager

    src = tmp_path / "img.png"
    rng = np.random.default_rng(0)
    Image.fromarray(rng.integers(0, 255, (240, 320, 3), dtype=np.uint8)).save(src)
    out = tmp_path / "out"
    out.mkdir()
    ctx = StageContext(
        inputs={"image": src}, params={"resolution_level": 0}, out_dir=out, device="cpu",
        models=ModelManager("cpu"),
        model=ModelRef(id="moge2-vits-normal", family="moge2", hf_repo="Ruicheng/moge-2-vits-normal", memory_gb=0.2),
    )
    result = geometry.run(ctx)
    assert {a.name for a in result.artifacts} >= {"points", "depth", "mask"}
    assert result.metadata["camera"]["fov_x_deg"] > 0
