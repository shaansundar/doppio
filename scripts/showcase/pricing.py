"""List prices (USD per 1M tokens) used to turn recorded token counts into cost.

Sources, checked 2026-11-13:
- Gemini: https://ai.google.dev/gemini-api/docs/pricing (paid tier, prompts <= 200k tokens).
- Claude: Anthropic API list prices for Claude Opus 5.5. Claude Code's transcripts
  report cache writes split by TTL; 5-minute writes cost 1.25x input, 1-hour 2x.
"""

from __future__ import annotations

GEMINI = {
    "gemini-2.5-flash-image": {"input": 0.30, "output_text": 2.50, "output_image": 30.00},
    "gemini-3.1-pro-preview": {"input": 2.00, "output_text": 12.00, "output_image": 12.00},
}

CLAUDE = {
    "claude-opus-5-5": {"input": 4.00, "output": 20.00, "cache_read": 0.20, "cache_write_5m": 5.00, "cache_write_1h": 8.00},
    "claude-sonnet-5-5": {"input": 2.00, "output": 10.00, "cache_read": 0.10, "cache_write_5m": 2.50, "cache_write_1h": 4.00},
    "claude-haiku-5-5": {"input": 0.10, "output": 0.50, "cache_read": 0.01, "cache_write_5m": 0.125, "cache_write_1h": 0.20},
}


def gemini_cost(model: str, usage: dict) -> dict:
    """Cost of one generateContent call from its usageMetadata.

    Thinking tokens are billed as text output. Output tokens are split by modality
    when the API reports candidatesTokensDetails; otherwise they count as text.
    """
    p = GEMINI[model]
    prompt = usage.get("promptTokenCount", 0)
    out_image = sum(d.get("tokenCount", 0) for d in usage.get("candidatesTokensDetails", []) if d.get("modality") == "IMAGE")
    out_total = usage.get("candidatesTokenCount", 0)
    out_text = out_total - out_image + usage.get("thoughtsTokenCount", 0)
    cost = {
        "input_usd": prompt * p["input"] / 1e6,
        "output_text_usd": out_text * p["output_text"] / 1e6,
        "output_image_usd": out_image * p["output_image"] / 1e6,
    }
    cost["total_usd"] = sum(cost.values())
    return cost


def claude_cost(model: str, usage: dict) -> dict:
    """Cost of summed Messages API usage (input, output, cache read, cache writes by TTL)."""
    p = CLAUDE[model]
    cost = {
        "input_usd": usage["input_tokens"] * p["input"] / 1e6,
        "output_usd": usage["output_tokens"] * p["output"] / 1e6,
        "cache_read_usd": usage["cache_read_input_tokens"] * p["cache_read"] / 1e6,
        "cache_write_usd": (usage["cache_write_5m_tokens"] * p["cache_write_5m"] + usage["cache_write_1h_tokens"] * p["cache_write_1h"]) / 1e6,
    }
    cost["total_usd"] = sum(cost.values())
    return cost
