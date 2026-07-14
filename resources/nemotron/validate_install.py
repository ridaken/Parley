"""Fast provisioning check; full model loading is guarded again at app startup."""

from __future__ import annotations

import argparse
from pathlib import Path

import torch
from transformers import AutoConfig, AutoProcessor


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("model", type=Path)
    args = parser.parse_args()

    if not torch.cuda.is_available():
        raise RuntimeError("the installed PyTorch runtime cannot access an NVIDIA GPU")
    AutoConfig.from_pretrained(args.model, local_files_only=True)
    AutoProcessor.from_pretrained(args.model, local_files_only=True)
    print(f"CUDA ready: {torch.cuda.get_device_name(0)}")


if __name__ == "__main__":
    main()
