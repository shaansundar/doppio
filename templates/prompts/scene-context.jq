# Render a doppio scene.json as a short geometry brief for prompts.
#
#   jq -r -f templates/prompts/scene-context.jq room.doppio/scene.json
#
# The output is plain text that you can paste into any image model's prompt.

def r1: (. * 10 | round) / 10;
def r2: (. * 100 | round) / 100;

. as $s
| [
    "Scene geometry (measured by doppio on the \($s.image.width)x\($s.image.height) px image):",
    "- Camera: \($s.camera.fov_x_deg | r1)° horizontal x \($s.camera.fov_y_deg | r1)° vertical field of view, about \($s.camera.focal_35mm_equiv | round) mm full-frame equivalent (\(if $s.camera.source == "exif+model" then "focal length from EXIF" else "estimated from the image" end)).",
    (if $s.depth_stats.median_m then
      "- Depth: nearest surfaces about \($s.depth_stats.near_m | r2) m away, median \($s.depth_stats.median_m | r2) m, farthest about \($s.depth_stats.far_m | r2) m (\(if $s.quality.metric_scale then "metric" else "relative" end) scale)."
    else empty end),
    "- \(($s.depth_stats.valid_fraction * 100) | round)% of pixels have estimated geometry\(if $s.depth_stats.likely_sky then "; the upper frame has areas without geometry (sky, windows or very distant background)" else "" end).",
    ($s.quality.warnings[]? | "- Note: \(.)"),
    "Keep the result physically consistent with this camera and depth: preserve perspective and vanishing lines, scale anything inserted to its distance, and respect near/far ordering and occlusion."
  ]
| join("\n")
