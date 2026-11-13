"""Analytic synthetic scenes with exact geometry, for tests, benchmarks and docs.

``room`` ray-casts a closed room with a box on the floor and a window in the
back wall (treated as invalid, like sky). All outputs follow the pack
conventions: OpenCV camera frame, meters, NaN for invalid geometry.
"""

from __future__ import annotations

import math
from dataclasses import dataclass

import numpy as np

# Room bounds in world coordinates (OpenCV-style axes: y points down).
ROOM_MIN = np.array([-3.0, -1.5, -1.0])
ROOM_MAX = np.array([3.0, 1.5, 7.0])
# Camera sits at the origin, 1.5 m above the floor (floor plane y = +1.5).
BOX_MIN = np.array([-0.6, 0.7, 3.5])
BOX_MAX = np.array([0.6, 1.5, 4.5])
WINDOW_X = (-1.2, 1.2)
WINDOW_Y = (-1.2, -0.1)

SURFACES = ("floor", "ceiling", "left", "right", "front", "back", "box", "window")
_ALBEDO = {
    "floor": (0.55, 0.40, 0.28),
    "ceiling": (0.90, 0.90, 0.90),
    "left": (0.85, 0.83, 0.80),
    "right": (0.85, 0.83, 0.80),
    "front": (0.85, 0.83, 0.80),
    "back": (0.78, 0.80, 0.84),
    "box": (0.25, 0.45, 0.70),
}
_SKY = (0.62, 0.78, 0.95)


@dataclass(frozen=True)
class Scene:
    image: np.ndarray  # HxWx3 uint8 sRGB shaded render
    points: np.ndarray  # HxWx3 float32 camera-frame points (m), NaN invalid
    depth: np.ndarray  # HxW float32 z-depth (m), NaN invalid
    normal: np.ndarray  # HxWx3 float32 unit normals (camera frame), 0 invalid
    mask: np.ndarray  # HxW bool valid geometry
    surface: np.ndarray  # HxW int index into SURFACES
    k: np.ndarray  # 3x3 pixel intrinsics
    camera_from_world: np.ndarray  # 3x3 rotation


def _pitch_down(deg: float) -> np.ndarray:
    """world_from_camera rotation for a camera pitched down by ``deg``."""
    a = math.radians(deg)
    return np.array([[1, 0, 0], [0, math.cos(a), math.sin(a)], [0, -math.sin(a), math.cos(a)]])


def room(width: int = 640, height: int = 480, fov_x_deg: float = 70.0, pitch_deg: float = 10.0) -> Scene:
    if width < 2 or height < 2:
        raise ValueError("image must be at least 2x2")
    f = (width / 2) / math.tan(math.radians(fov_x_deg) / 2)
    k = np.array([[f, 0, width / 2], [0, f, height / 2], [0, 0, 1]])
    world_from_cam = _pitch_down(pitch_deg)

    u, v = np.meshgrid(np.arange(width) + 0.5, np.arange(height) + 0.5)
    d_cam = np.stack([(u - k[0, 2]) / f, (v - k[1, 2]) / f, np.ones_like(u)], axis=-1)  # z = 1
    d_world = d_cam @ world_from_cam.T

    # Exit through the room's walls (camera is inside an axis-aligned box).
    with np.errstate(divide="ignore", invalid="ignore"):
        bound = np.where(d_world > 0, ROOM_MAX, ROOM_MIN)
        t_axes = np.where(np.abs(d_world) > 1e-12, bound / d_world, np.inf)
    axis = np.argmin(t_axes, axis=-1)
    t = np.take_along_axis(t_axes, axis[..., None], axis=-1)[..., 0]
    positive = np.take_along_axis(d_world, axis[..., None], axis=-1)[..., 0] > 0
    # axis 0: left/right, 1: ceiling/floor, 2: front/back
    surface = np.select(
        [(axis == 0) & ~positive, (axis == 0) & positive, (axis == 1) & ~positive,
         (axis == 1) & positive, (axis == 2) & ~positive],
        [2, 3, 1, 0, 4], default=5,
    )
    n_world = np.zeros_like(d_world)
    np.put_along_axis(n_world, axis[..., None], np.where(positive, -1.0, 1.0)[..., None], axis=-1)

    # Box on the floor (slab test); keep it where it is nearer than the wall.
    with np.errstate(divide="ignore", invalid="ignore"):
        t1, t2 = (BOX_MIN - 0) / d_world, (BOX_MAX - 0) / d_world
    t_near_axes = np.minimum(t1, t2)
    t_near = np.nanmax(t_near_axes, axis=-1)
    t_far = np.nanmin(np.maximum(t1, t2), axis=-1)
    box_hit = (t_far >= t_near) & (t_near > 0) & (t_near < t)
    box_axis = np.nanargmax(t_near_axes, axis=-1)
    box_n = np.zeros_like(d_world)
    sign = -np.sign(np.take_along_axis(d_world, box_axis[..., None], axis=-1))
    np.put_along_axis(box_n, box_axis[..., None], sign, axis=-1)
    t = np.where(box_hit, t_near, t)
    surface = np.where(box_hit, 6, surface)
    n_world = np.where(box_hit[..., None], box_n, n_world)

    p_world = d_world * t[..., None]
    window = (surface == 5) & (p_world[..., 0] > WINDOW_X[0]) & (p_world[..., 0] < WINDOW_X[1]) & \
             (p_world[..., 1] > WINDOW_Y[0]) & (p_world[..., 1] < WINDOW_Y[1])
    surface = np.where(window, 7, surface)
    mask = ~window

    points = (d_cam * t[..., None]).astype(np.float32)
    depth = points[..., 2].copy()
    normal = (n_world @ world_from_cam).astype(np.float32)  # camera_from_world = world_from_cam.T
    points[~mask] = np.nan
    depth[~mask] = np.nan
    normal[~mask] = 0.0

    image = _shade(surface, n_world, t)
    return Scene(image, points, depth, normal, mask, surface.astype(np.int32), k, world_from_cam.T)


def _shade(surface: np.ndarray, n_world: np.ndarray, t: np.ndarray) -> np.ndarray:
    light = np.array([0.3, 0.6, -1.0])  # light travels from the window into the room
    light = -light / np.linalg.norm(light)
    albedo = np.zeros(surface.shape + (3,))
    for idx, name in enumerate(SURFACES):
        albedo[surface == idx] = _SKY if name == "window" else _ALBEDO[name]
    lambert = np.clip(n_world @ light, 0, 1)[..., None]
    fog = np.exp(-0.04 * np.nan_to_num(t, nan=0.0))[..., None]
    lit = albedo * (0.38 + 0.62 * lambert) * (0.75 + 0.25 * fog)
    lit[surface == 7] = _SKY
    srgb = np.clip(lit, 0, 1) ** (1 / 2.2)
    return np.rint(srgb * 255).astype(np.uint8)
