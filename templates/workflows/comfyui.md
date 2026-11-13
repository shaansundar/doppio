# Using a doppio pack in ComfyUI

Dedicated ComfyUI nodes are on the [roadmap](../../README.md#roadmap). Until
then, a pack plugs into stock nodes:

1. `doppio analyze photo.jpg --preset standard` → `photo.doppio/`
2. **Load Image** → `photo.doppio/maps/depth_inv16.png` (or `depth_inv8.png`).
   Inverse depth with near = bright is what depth ControlNets expect, so no
   preprocessor is needed. Skip the "Depth Anything" / "MiDaS" nodes.
3. **Load ControlNet Model** → a depth ControlNet for your base model
   (for example a Flux ControlNet Union or an SDXL depth ControlNet).
4. **Apply ControlNet** with the loaded depth image, strength 0.5–0.8.
5. Optional: a second **Load Image** → `maps/normal.png` into a normal
   ControlNet. doppio writes the OpenGL convention (R right, G up, B toward
   viewer). If your model was trained on maps with red flipped, invert the red
   channel first.
6. Match your latent size to the map's aspect ratio (multiples of 8 or 16).

For instruction editors in ComfyUI (for example Flux Kontext or Qwen-Image-Edit
workflows), feed the photo as the image to edit and put the output of
`templates/scripts/build-prompt.sh photo.doppio "your edit"` into the text prompt.
