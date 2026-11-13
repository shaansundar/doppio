# Task: edit a photo

A user wants this photo edited:

- Photo: `{photo}`
- Edit request: "{request}"

The edit itself is made by Google's `gemini-2.5-flash-image` model, which you call
through this script. Run it from `/Users/notthatsundar/code/doppio-pro`:

```sh
worker/.venv/bin/python scripts/showcase/gemini_edit.py \
  --image {photo} [--attach <reference image> ...] \
  --prompt-file {run_dir}/prompt.txt --out {run_dir}/edited.png --log {run_dir}/gemini.json
```

The script sends the photo first, then any `--attach` images in order, then your
prompt. Images are resized to at most 1536 px. The model returns one image.

Your job: write the best prompt you can for that model so the result does what the
user asked and is physically convincing. Every added object must rest on a real
surface, have the right size for its distance, follow the photo's perspective, be
hidden by anything in front of it, and match the scene's lighting. Nothing else in
the photo should change.

Rules:

1. Save your final prompt as `{run_dir}/prompt.txt`, then call `gemini_edit.py`
   exactly once. It is single-shot and refuses to overwrite. Do not retry or make a
   second edit. If it fails, report the error and stop.
2. Look at the photo as much as you like before editing. Do not open the edited result.
3. Write files only inside `{run_dir}`. Do not change anything else in the repository.
4. Do not open other folders under `showcase/runs/` or `showcase/judge/`, or read the
   files in `scripts/showcase/`. You only need to run `gemini_edit.py`.
5. {condition_rule}

When you're done, reply with a short report: the final prompt, the files you
attached, and {report_extra}.
