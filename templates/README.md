# Templates

Ready-to-use glue between a doppio Spatial Context Pack and the model you edit with.

| Template | Use it for |
|---|---|
| [`prompts/scene-context.jq`](prompts/scene-context.jq) | Turns `scene.json` into a short geometry brief for any prompt |
| [`prompts/multimodal-edit.md`](prompts/multimodal-edit.md) | Instruction editors (GPT-image, Gemini, Flux Kontext, Qwen-Image-Edit): photo + depth map + brief + edit request |
| [`scripts/build-prompt.sh`](scripts/build-prompt.sh) | Fills a prompt template from a pack and an edit request |
| [`prompts/agent-system-prompt.md`](prompts/agent-system-prompt.md) | System prompt that teaches an AI agent (Claude Code, Cursor, Codex…) to use doppio |
| [`prompts/install-with-ai.md`](prompts/install-with-ai.md) | Prompt that has an AI coding assistant install doppio for you |
| [`workflows/diffusers_depth_controlnet.py`](workflows/diffusers_depth_controlnet.py) | Python: generate from a pack's depth map with an SDXL depth ControlNet |
| [`workflows/comfyui.md`](workflows/comfyui.md) | Wiring pack maps into stock ComfyUI nodes |

## Example

```sh
doppio analyze room.jpg --preset fast
templates/scripts/build-prompt.sh room.doppio "add a brass floor lamp left of the sofa" > prompt.txt
# Send to your editor: room.jpg, room.doppio/maps/depth_inv8.png, prompt.txt
```

With the synthetic example in [`examples/scene.synthetic-room.json`](../examples/scene.synthetic-room.json),
`scene-context.jq` prints:

```text
Scene geometry (measured by doppio on the 1024x768 px image):
- Camera: 70° horizontal x 55.4° vertical field of view, about 25 mm full-frame equivalent (estimated from the image).
- Depth: nearest surfaces about 2.2 m away, median 4.73 m, farthest about 7.12 m (metric scale).
- 96% of pixels have estimated geometry; the upper frame has areas without geometry (sky, windows or very distant background).
Keep the result physically consistent with this camera and depth: preserve perspective and vanishing lines, scale anything inserted to its distance, and respect near/far ordering and occlusion.
```

Templates are plain text. Copy them, change the wording for your model, and
send improvements upstream.
