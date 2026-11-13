"""Create showcase run folders and render each agent's brief into them.

    python scripts/showcase/prepare_runs.py --root showcase [case ...]

Writes showcase/runs/{baseline,doppio}/<case>/brief.md. The two briefs for a case
differ only in rule 5 and the last line of the report request.
"""

from __future__ import annotations

import argparse
import json
from pathlib import Path

HERE = Path(__file__).resolve().parent
REPO = HERE.parent.parent

CONDITIONS = {
    "baseline": {
        "condition_rule": (
            "Work from the photo alone. Do not run `doppio`, read doppio output, or use any "
            "depth-estimation or 3D-reconstruction tool."
        ),
        "report_extra": "anything you worked out about the scene's geometry",
    },
    "doppio": {
        "condition_rule": (
            "Use doppio, the local tool in this repository that measures a photo's 3D layout "
            "(camera field of view, metric depth, surface normals). Follow its agent workflow:\n"
            "   a. Analyze: `bin/doppio analyze {photo} --preset standard --out {run_dir}/pack`.\n"
            "   b. Read `{run_dir}/pack/scene.json`: `camera.fov_x_deg`, `camera.focal_35mm_equiv`, "
            "`depth_stats` and `quality.warnings`.\n"
            "   c. Start from doppio's prompt template: `templates/scripts/build-prompt.sh {run_dir}/pack "
            "\"<edit request>\" > {run_dir}/prompt.txt`, then refine it with what you measured. To place "
            "an object of height h metres at pixel (u, v) of the analyzed image, read its distance from "
            "`{run_dir}/pack/arrays/depth_metric.npy` (use `worker/.venv/bin/python`); it is about "
            "h * fy / depth[v, u] pixels tall, with fy from `camera` in scene.json.\n"
            "   d. Attach doppio's depth map: `--attach {run_dir}/pack/maps/depth_inv8.png`."
        ),
        "report_extra": "the doppio measurements you used",
    },
}


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("--root", type=Path, default=REPO / "showcase")
    ap.add_argument("cases", nargs="*")
    args = ap.parse_args()
    root = args.root.resolve()
    requests = json.loads((HERE / "requests.json").read_text())
    template = (HERE / "agent_brief.md").read_text()
    for case in args.cases or sorted(requests):
        photo = root / "cases" / case / "original.jpg"
        assert photo.exists(), photo
        for cond, parts in CONDITIONS.items():
            run_dir = root / "runs" / cond / case
            run_dir.mkdir(parents=True, exist_ok=True)
            fill = {"photo": str(photo), "run_dir": str(run_dir), "request": requests[case]}
            rule = parts["condition_rule"].format(**fill)
            brief = template.format(**fill, condition_rule=rule, report_extra=parts["report_extra"])
            (run_dir / "brief.md").write_text(brief)
            print(run_dir / "brief.md")


if __name__ == "__main__":
    main()
