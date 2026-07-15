"""Download only the Transformers checkpoint, excluding the duplicate NeMo archive."""

from __future__ import annotations

import argparse
from pathlib import Path

from huggingface_hub import snapshot_download


MODEL_ID = "nvidia/nemotron-3.5-asr-streaming-0.6b"
MODEL_FILES = [
    "config.json",
    "generation_config.json",
    "model.safetensors",
    "processor_config.json",
    "tokenizer.json",
    "tokenizer_config.json",
]


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("destination", type=Path)
    args = parser.parse_args()
    args.destination.mkdir(parents=True, exist_ok=True)
    snapshot_download(
        repo_id=MODEL_ID,
        local_dir=args.destination,
        allow_patterns=MODEL_FILES,
    )


if __name__ == "__main__":
    main()
