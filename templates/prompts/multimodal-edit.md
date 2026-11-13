You are editing the FIRST attached image (the photo).

The SECOND attachment is the same scene's depth map, produced by doppio: brighter
means closer to the camera, darker means farther, and pure black means no
geometry (sky, windows or very distant background). Use it to understand the 3D
layout. Do not reproduce it in the output.

{{SCENE_CONTEXT}}

Edit request: {{EDIT}}

Rules:
1. Change only what the request asks for. Keep the framing, camera, and all other content unchanged.
2. Anything added or moved must rest on a plausible surface and follow this camera's perspective: its apparent size shrinks with distance as the depth map shows, and its edges converge to the same vanishing points as the scene.
3. Respect occlusion: surfaces brighter in the depth map stay in front of anything placed behind them.
4. Match the existing light direction, shadow softness, and color temperature.
5. Return a single image with the same aspect ratio as the photo.
