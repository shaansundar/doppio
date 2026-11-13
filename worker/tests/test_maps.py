from pathlib import Path

import cv2
import numpy as np
import pytest

from doppio_worker.stages import maps
from doppio_worker.stages.base import StageContext, StageInputError


def test_inverse_depth_near_is_bright_and_invalid_is_zero():
    depth = np.array([[1.0, 2.0], [4.0, np.nan]], dtype=np.float32)
    valid = maps.valid_depth_mask(depth)
    inv = maps.inverse_depth_normalized(depth, valid)
    assert inv[0, 0] > inv[0, 1] > inv[1, 0]
    assert inv[1, 1] == 0.0
    assert inv.max() <= 1.0 and inv.min() >= 0.0


def test_inverse_depth_constant_depth_is_full_brightness():
    depth = np.full((3, 3), 5.0)
    inv = maps.inverse_depth_normalized(depth, np.ones((3, 3), bool))
    assert np.all(inv == 1.0)


def test_valid_mask_respects_explicit_mask_and_nonpositive_depth():
    depth = np.array([[1.0, -1.0, 0.0, np.inf]])
    mask = np.array([[False, True, True, True]])
    assert not maps.valid_depth_mask(depth, mask).any()


@pytest.mark.parametrize(
    ("normal_cv", "expected_rgb"),
    [
        ((0.0, 0.0, -1.0), (128, 128, 255)),  # facing the camera -> blue
        ((0.0, -1.0, 0.0), (128, 255, 128)),  # world up in OpenCV (y down) -> green
        ((1.0, 0.0, 0.0), (255, 128, 128)),  # pointing right -> red
    ],
)
def test_normal_to_rgb_uses_opengl_convention(normal_cv, expected_rgb):
    n = np.array(normal_cv, dtype=np.float32).reshape(1, 1, 3)
    rgb = maps.normal_to_rgb_gl(n, np.ones((1, 1), bool))
    assert tuple(int(v) for v in rgb[0, 0]) == expected_rgb


def test_normal_invalid_and_zero_vectors_are_black():
    n = np.array([[[0, 0, -1], [0, 0, 0]]], dtype=np.float32)
    rgb = maps.normal_to_rgb_gl(n, np.array([[False, True]]))
    assert rgb[0, 0].tolist() == [0, 0, 0]
    assert rgb[0, 1].tolist() == [0, 0, 0]


def _write_inputs(tmp_path: Path, depth, normal=None, mask=None) -> dict[str, Path]:
    inputs = {"depth": tmp_path / "depth.npy"}
    np.save(inputs["depth"], depth)
    if normal is not None:
        inputs["normal"] = tmp_path / "normal.npy"
        np.save(inputs["normal"], normal)
    if mask is not None:
        inputs["mask"] = tmp_path / "mask.npy"
        np.save(inputs["mask"], mask)
    return inputs


def test_run_writes_all_maps(tmp_path):
    depth = np.linspace(1, 10, 16 * 24, dtype=np.float32).reshape(16, 24)
    depth[0, 0] = np.nan
    normal = np.zeros((16, 24, 3), np.float32)
    normal[..., 2] = -1
    out = tmp_path / "out"
    out.mkdir()
    ctx = StageContext(inputs=_write_inputs(tmp_path, depth, normal), params={}, out_dir=out, device="cpu")
    result = maps.run(ctx)

    names = {a.name for a in result.artifacts}
    assert names == {"depth_inv16", "depth_inv8", "valid_mask", "normal"}
    d16 = cv2.imread(str(out / "depth_inv16.png"), cv2.IMREAD_UNCHANGED)
    assert d16.dtype == np.uint16 and d16.shape == (16, 24)
    assert d16[0, 0] == 0  # invalid
    assert d16[0, 1] > d16[-1, -1]  # nearer is brighter
    normal_bgr = cv2.imread(str(out / "normal.png"))
    assert normal_bgr[5, 5].tolist() == [255, 128, 128]  # BGR of (128,128,255)
    assert result.metadata["normal_convention"] == "opengl"


def test_run_rejects_shape_mismatch(tmp_path):
    out = tmp_path / "out"
    out.mkdir()
    inputs = _write_inputs(tmp_path, np.ones((4, 4), np.float32), mask=np.ones((3, 3), bool))
    with pytest.raises(StageInputError, match="mask shape"):
        maps.run(StageContext(inputs=inputs, params={}, out_dir=out, device="cpu"))
