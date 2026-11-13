#!/usr/bin/env bash
# Fill a doppio prompt template from a Spatial Context Pack.
#
#   templates/scripts/build-prompt.sh room.doppio "add a floor lamp left of the sofa"
#   templates/scripts/build-prompt.sh room.doppio "..." templates/prompts/multimodal-edit.md
#
# Requires jq. Prints the prompt to stdout; attach the photo and
# <pack>/maps/depth_inv8.png alongside it.
set -euo pipefail

if [[ $# -lt 2 ]]; then
  echo "usage: $0 PACK_DIR \"edit request\" [TEMPLATE]" >&2
  exit 64
fi

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
pack="$1"
edit="$2"
template="${3:-$here/../prompts/multimodal-edit.md}"

command -v jq >/dev/null || { echo "jq is required (brew install jq)" >&2; exit 69; }
[[ -f "$pack/scene.json" ]] || { echo "no scene.json in $pack" >&2; exit 66; }

context="$(jq -r -f "$here/../prompts/scene-context.jq" "$pack/scene.json")"

# Literal (not regex) substitution so user text with & or \ is kept as is.
SCENE_CONTEXT="$context" EDIT="$edit" awk '
  function repl(s, token, value,   out, i) {
    out = ""
    while ((i = index(s, token)) > 0) {
      out = out substr(s, 1, i - 1) value
      s = substr(s, i + length(token))
    }
    return out s
  }
  {
    line = repl($0, "{{SCENE_CONTEXT}}", ENVIRON["SCENE_CONTEXT"])
    print repl(line, "{{EDIT}}", ENVIRON["EDIT"])
  }
' "$template"
