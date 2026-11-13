import math

import numpy as np
import pytest

from doppio_worker import synthetic


@pytest.fixture(scope="module")
def scene():
    return synthetic.room(320, 240, fov_x_deg=70, pitch_deg=10)


def test_shapes_and_validity(scene):
    assert scene.image.shape == (240, 320, 3) and scene.image.dtype == np.uint8
    assert scene.points.shape == (240, 320, 3)
    valid = scene.mask
    assert 0.8 < valid.mean() < 1.0  # the window is invalid, the rest is valid
    assert np.isnan(scene.depth[~valid]).all()
    assert (scene.depth[valid] > 0).all()
    np.testing.assert_allclose(np.linalg.norm(scene.normal[valid], axis=-1), 1.0, atol=1e-5)


def test_points_reproject_to_their_pixels(scene):
    v, u = 200, 50
    p = scene.points[v, u]
    k = scene.k
    assert (k[0, 0] * p[0] / p[2] + k[0, 2]) == pytest.approx(u + 0.5, abs=1e-3)
    assert (k[1, 1] * p[1] / p[2] + k[1, 2]) == pytest.approx(v + 0.5, abs=1e-3)


def test_floor_is_horizontal_and_camera_height_is_1_5m(scene):
    floor = scene.surface == synthetic.SURFACES.index("floor")
    assert floor[-1].all()  # bottom row sees the floor
    world_from_cam = scene.camera_from_world.T
    n_world = scene.normal[floor] @ world_from_cam.T
    np.testing.assert_allclose(n_world, np.tile([0, -1, 0], (len(n_world), 1)), atol=1e-6)  # up
    p_world = scene.points[floor] @ world_from_cam.T
    np.testing.assert_allclose(p_world[:, 1], 1.5, atol=1e-4)


def test_normals_face_the_camera(scene):
    valid = scene.mask
    assert (np.einsum("ij,ij->i", scene.normal[valid], scene.points[valid]) < 0).all()


def test_box_is_visible_and_nearer_than_back_wall(scene):
    box = scene.surface == synthetic.SURFACES.index("box")
    back = scene.surface == synthetic.SURFACES.index("back")
    assert box.any() and back.any()
    assert np.nanmax(scene.depth[box]) < np.nanmin(scene.depth[back])


def test_fov_matches_intrinsics(scene):
    fov = 2 * math.degrees(math.atan(scene.k[0, 2] / scene.k[0, 0]))
    assert fov == pytest.approx(70)


def test_rejects_tiny_images():
    with pytest.raises(ValueError):
        synthetic.room(1, 1)
