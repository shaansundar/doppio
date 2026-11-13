"""Generate an image that keeps a photo's 3D layout: doppio depth + SDXL depth ControlNet.

    pip install diffusers transformers accelerate torch
    python templates/workflows/diffusers_depth_controlnet.py room.doppio "a scandinavian living room, morning light"

This is an example, not part of the test suite. The two models it downloads are
third-party and have their own licenses; check them before commercial use.
"""

from __future__ import annotations

import sys
from pathlib import Path

import torch
from diffusers import ControlNetModel, StableDiffusionXLControlNetPipeline
from diffusers.utils import load_image


def main() -> None:
    if len(sys.argv) < 3:
        sys.exit(__doc__)
    pack, prompt = Path(sys.argv[1]), sys.argv[2]
    device = "cuda" if torch.cuda.is_available() else "mps" if torch.backends.mps.is_available() else "cpu"
    dtype = torch.float16 if device != "cpu" else torch.float32

    controlnet = ControlNetModel.from_pretrained("diffusers/controlnet-depth-sdxl-1.0", torch_dtype=dtype)
    pipe = StableDiffusionXLControlNetPipeline.from_pretrained(
        "stabilityai/stable-diffusion-xl-base-1.0", controlnet=controlnet, torch_dtype=dtype
    ).to(device)

    # doppio's depth maps are inverse depth with near = bright, as depth ControlNets expect.
    depth = load_image(str(pack / "maps" / "depth_inv8.png"))
    scale = 1024 / max(depth.size)
    size = (int(depth.width * scale) // 8 * 8, int(depth.height * scale) // 8 * 8)
    depth = depth.resize(size)

    image = pipe(
        prompt,
        image=depth,
        controlnet_conditioning_scale=0.6,
        num_inference_steps=30,
        width=size[0],
        height=size[1],
    ).images[0]
    out = pack / "generated.png"
    image.save(out)
    print(f"wrote {out}")


if __name__ == "__main__":
    main()
