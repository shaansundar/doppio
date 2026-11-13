"""Compute device selection: CUDA, then MPS, then CPU (override with DOPPIO_DEVICE)."""

from __future__ import annotations

import os

VALID_DEVICES = ("cuda", "mps", "cpu")


def select_device(preferred: str | None = None) -> str:
    choice = (preferred or os.environ.get("DOPPIO_DEVICE", "")).strip().lower()
    if choice and choice not in VALID_DEVICES:
        raise ValueError(f"unknown device {choice!r}; expected one of {VALID_DEVICES}")
    try:
        import torch
    except ImportError:
        return "cpu"
    available = {
        "cuda": torch.cuda.is_available(),
        "mps": torch.backends.mps.is_available(),
        "cpu": True,
    }
    if choice:
        if not available[choice]:
            raise RuntimeError(f"requested device {choice!r} is not available")
        return choice
    for device in VALID_DEVICES:
        if available[device]:
            return device
    return "cpu"


def torch_version() -> str:
    try:
        import torch
    except ImportError:
        return ""
    return str(torch.__version__)
