"""Sum exact Claude token usage for a Claude Code subagent from its transcript.

Claude Code writes one JSONL line per content block, and every line of an API
response repeats that response's usage, so usage is counted once per message id.

    python scripts/showcase/claude_usage.py <transcript.jsonl> [--out usage.json]
"""

from __future__ import annotations

import argparse
import json
import sys
from collections import Counter
from datetime import datetime
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pricing import claude_cost  # noqa: E402


def summarize(path: Path) -> dict:
    messages: dict[str, dict] = {}
    tools: Counter[str] = Counter()
    bash_commands: list[str] = []
    stamps: list[str] = []
    for line in path.read_text().splitlines():
        row = json.loads(line)
        if row.get("timestamp"):
            stamps.append(row["timestamp"])
        msg = row.get("message")
        if row.get("type") != "assistant" or not isinstance(msg, dict) or not msg.get("usage"):
            continue
        messages[msg["id"]] = msg
        for block in msg.get("content", []):
            if block.get("type") == "tool_use":
                tools[block["name"]] += 1
                if block["name"] == "Bash":
                    bash_commands.append(block.get("input", {}).get("command", "")[:300])

    totals = Counter()
    models = Counter()
    for msg in messages.values():
        u = msg["usage"]
        cc = u.get("cache_creation") or {}
        totals["input_tokens"] += u.get("input_tokens", 0)
        totals["output_tokens"] += u.get("output_tokens", 0)
        totals["cache_read_input_tokens"] += u.get("cache_read_input_tokens", 0)
        totals["cache_write_5m_tokens"] += cc.get("ephemeral_5m_input_tokens", 0)
        totals["cache_write_1h_tokens"] += cc.get("ephemeral_1h_input_tokens", 0)
        models[msg.get("model")] += 1
    model = models.most_common(1)[0][0]
    t0, t1 = (datetime.fromisoformat(s.replace("Z", "+00:00")) for s in (min(stamps), max(stamps)))
    usage = dict(totals)
    usage["total_input_tokens"] = (
        usage["input_tokens"] + usage["cache_read_input_tokens"] + usage["cache_write_5m_tokens"] + usage["cache_write_1h_tokens"]
    )
    return {
        "transcript": str(path),
        "model": model,
        "api_requests": len(messages),
        "tool_calls": dict(tools),
        "bash_commands": bash_commands,
        "wall_time_s": round((t1 - t0).total_seconds(), 1),
        "usage": usage,
        "cost": {k: round(v, 6) for k, v in claude_cost(model, usage).items()},
    }


def main() -> None:
    ap = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    ap.add_argument("transcript", type=Path)
    ap.add_argument("--out", type=Path)
    args = ap.parse_args()
    result = summarize(args.transcript)
    text = json.dumps(result, indent=2)
    if args.out:
        args.out.write_text(text)
    print(text)


if __name__ == "__main__":
    main()
