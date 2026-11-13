Install **doppio** (https://github.com/shaansundar/doppio), a local tool that gives image generation and editing models 3D context (camera, metric depth, normals) about a photo. Run the commands yourself, show me the important output, and follow these rules:

- Do not use `sudo`, change system settings, or edit my shell profile without asking.
- Stop and ask me before anything that downloads code from outside PyPI, the Go module proxy, or Homebrew.
- Never make the doppio daemon listen on anything other than 127.0.0.1.
- If a step fails, show me the error, explain the likely cause, and propose a fix before retrying.

Steps:

1. **Check the platform and tools.** Report the results of `uname -sm`, `sw_vers` (macOS), `go version` (needs 1.25 or newer), `uv --version`, `git --version`, `jq --version` (optional), free disk space in my home directory (needs about 3 GB), and total RAM (8 GB minimum, 16 GB recommended).
   - The primary target is Apple Silicon macOS. Linux with an NVIDIA GPU or CPU-only also works.
   - If Go, uv or jq is missing, propose installing it (macOS: `brew install go uv jq`; uv elsewhere: https://docs.astral.sh/uv/getting-started/installation/) and wait for my approval.
2. **Get the code.** Run `git clone https://github.com/shaansundar/doppio.git && cd doppio`. If the folder already exists, `cd` into it and run `git pull --ff-only` instead.
3. **Build the CLI.** Run `make build`. It should produce `bin/doppio`; confirm with `bin/doppio version`.
4. **Create the worker environment** (PyPI packages only, about 0.9 GB): `make worker-setup`.
5. **Ask me before this step.** `bin/doppio setup` installs the MoGe-2 geometry model code (MIT, from github.com/microsoft/MoGe at a pinned commit) plus two pinned GitHub dependencies (EasternJournalist/utils3d and EasternJournalist/pipeline). Model weights (134 MB for fast, 1.2 GB for standard/full, MIT) download from Hugging Face on first use. Explain this to me and run it only after I say yes.
6. **Verify.** Run `bin/doppio doctor` and show me the output. Every line should start with `[ok  ]`; diagnose any `FAIL`.
7. **Smoke test.** Ask me for an image path (or use any JPEG/PNG I point you to), then run `bin/doppio analyze <image> --preset fast`. Show the summary and list the files in the created `<name>.doppio/` folder. The first run downloads the model weights, so allow a few minutes.
8. **Optional:** offer to add `bin/` to my PATH (ask first) and to stop the background daemon with `bin/doppio stop`.

Finish with a short summary: what was installed, where (`~/Library/Application Support/doppio`, `~/Library/Caches/doppio`, the repo's `worker/.venv`), how much disk it used, and the next commands to try:

- `bin/doppio analyze photo.jpg`
- `templates/scripts/build-prompt.sh photo.doppio "your edit"`
- the system prompt in `templates/prompts/agent-system-prompt.md`
