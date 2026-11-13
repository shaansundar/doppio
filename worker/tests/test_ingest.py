import math
from pathlib import Path

import numpy as np
import pytest
from PIL import Image

from doppio_worker.stages import ingest
from doppio_worker.stages.base import StageContext, StageInputError


def _ctx(tmp_path: Path, source: Path, **params) -> StageContext:
    out = tmp_path / "out"
    out.mkdir()
    return StageContext(inputs={"source": source}, params=params, out_dir=out, device="cpu")


def _save_with_exif(path: Path, size=(300, 200), orientation=None, focal35=None):
    img = Image.new("RGB", size, (200, 10, 10))
    img.paste((10, 200, 10), (0, 0, size[0] // 2, size[1]))  # left half green
    exif = Image.Exif()
    if orientation is not None:
        exif[274] = orientation
    if focal35 is not None:
        exif.get_ifd(0x8769)[41989] = focal35
    img.save(path, format="JPEG", exif=exif.tobytes(), quality=95)


@pytest.mark.parametrize(
    ("focal35", "width", "height", "expected_fov_x"),
    [
        (18.0, 3000, 2000, 90.0),  # 3:2 frame: hfov = 2*atan(36 / (2 * 18))
        (36.0, 3000, 2000, 2 * math.degrees(math.atan(0.5))),
        (50.0, 6000, 4000, 2 * math.degrees(math.atan(18 / 50))),
    ],
)
def test_fov_from_35mm_matches_full_frame_geometry(focal35, width, height, expected_fov_x):
    fov = ingest.fov_from_35mm(focal35, width, height)
    assert fov["fov_x_deg"] == pytest.approx(expected_fov_x, abs=1e-6)
    assert fov["fov_y_deg"] < fov["fov_x_deg"]


def test_fov_from_35mm_rejects_non_positive():
    with pytest.raises(ValueError):
        ingest.fov_from_35mm(0, 10, 10)


def test_ingest_applies_exif_orientation_and_reads_focal(tmp_path):
    src = tmp_path / "in.jpg"
    _save_with_exif(src, size=(300, 200), orientation=6, focal35=26)  # 6 = rotate 90 CW on display
    result = ingest.run(_ctx(tmp_path, src))

    meta = result.metadata
    assert (meta["width"], meta["height"]) == (200, 300)  # rotated to portrait
    assert meta["exif"]["orientation"] == 6
    assert meta["exif"]["focal_length_35mm"] == 26
    assert meta["fov_prior"]["source"] == "exif_35mm"
    with Image.open(tmp_path / "out" / "image.png") as out:
        assert out.mode == "RGB"
        assert out.size == (200, 300)


def test_ingest_downscales_long_side(tmp_path):
    src = tmp_path / "big.png"
    Image.new("RGB", (1000, 500), (1, 2, 3)).save(src)
    result = ingest.run(_ctx(tmp_path, src, max_side=256))
    assert (result.metadata["width"], result.metadata["height"]) == (256, 128)
    assert result.metadata["scale"] == pytest.approx(0.256)
    assert result.artifacts[0].shape == (128, 256, 3)


def test_ingest_handles_alpha_and_16bit(tmp_path):
    rgba = tmp_path / "a.png"
    Image.new("RGBA", (64, 64), (10, 20, 30, 0)).save(rgba)
    assert ingest.run(_ctx(tmp_path, rgba)).metadata["had_alpha"] is True

    gray16 = tmp_path / "g16.png"
    Image.fromarray(np.full((32, 32), 65535, dtype=np.uint16)).save(gray16)
    sub = tmp_path / "second"
    sub.mkdir()
    result = ingest.run(_ctx(sub, gray16))
    with Image.open(sub / "out" / "image.png") as out:
        assert np.asarray(out).max() == 255


def test_ingest_rejects_garbage_and_pixel_bombs(tmp_path):
    bad = tmp_path / "bad.jpg"
    bad.write_bytes(b"not an image")
    with pytest.raises(StageInputError, match="cannot decode"):
        ingest.run(_ctx(tmp_path, bad))

    big = tmp_path / "huge.png"
    Image.new("RGB", (400, 400)).save(big)
    sub = tmp_path / "bomb"
    sub.mkdir()
    with pytest.raises(StageInputError, match="pixel limit"):
        ingest.run(_ctx(sub, big, max_pixels=10_000))


def test_ingest_requires_source(tmp_path):
    out = tmp_path / "out"
    out.mkdir()
    with pytest.raises(StageInputError, match="missing required input"):
        ingest.run(StageContext(inputs={}, params={}, out_dir=out, device="cpu"))
