"""Exercise Parley's Nemotron streaming endpoint with a WAV file in real time."""

from __future__ import annotations

import argparse
import io
import time
import uuid
import wave
from pathlib import Path

import requests


def wav_payload(samples: bytes, sample_rate: int, channels: int, sample_width: int) -> bytes:
    output = io.BytesIO()
    with wave.open(output, "wb") as wav_file:
        wav_file.setnchannels(channels)
        wav_file.setsampwidth(sample_width)
        wav_file.setframerate(sample_rate)
        wav_file.writeframes(samples)
    return output.getvalue()


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("wav", type=Path)
    parser.add_argument("--url", default="http://127.0.0.1:8765")
    parser.add_argument("--frame-ms", type=int, default=320)
    parser.add_argument("--no-pace", action="store_true")
    args = parser.parse_args()

    with wave.open(str(args.wav), "rb") as wav_file:
        channels = wav_file.getnchannels()
        sample_width = wav_file.getsampwidth()
        sample_rate = wav_file.getframerate()
        frames = wav_file.readframes(wav_file.getnframes())
    if channels != 1 or sample_width != 2 or sample_rate != 16000:
        raise ValueError("expected a mono, 16-bit, 16 kHz WAV")

    samples_per_frame = sample_rate * args.frame_ms // 1000
    bytes_per_frame = samples_per_frame * sample_width * channels
    frame_duration = samples_per_frame / sample_rate
    stream_id = "smoke-" + uuid.uuid4().hex
    started = time.monotonic()
    first_text_at: float | None = None
    parts: list[str] = []

    for index, offset in enumerate(range(0, len(frames), bytes_per_frame)):
        if not args.no_pace:
            delay = started + index * frame_duration - time.monotonic()
            if delay > 0:
                time.sleep(delay)
        response = requests.post(
            args.url.rstrip("/") + "/stream",
            data={"stream_id": stream_id, "action": "feed"},
            files={"file": ("chunk.wav", wav_payload(frames[offset : offset + bytes_per_frame], sample_rate, channels, sample_width), "audio/wav")},
            timeout=120,
        )
        response.raise_for_status()
        delta = response.json()["text"]
        if delta:
            if first_text_at is None:
                first_text_at = time.monotonic() - started
            parts.append(delta)

    response = requests.post(
        args.url.rstrip("/") + "/stream",
        data={"stream_id": stream_id, "action": "finish"},
        files={"_": (None, "")},
        timeout=120,
    )
    response.raise_for_status()
    parts.append(response.json()["text"])

    elapsed = time.monotonic() - started
    audio_seconds = len(frames) / (sample_rate * sample_width * channels)
    print(f"audio: {audio_seconds:.3f}s")
    print(f"first text: {first_text_at:.3f}s" if first_text_at is not None else "first text: none")
    print(f"finished: {elapsed:.3f}s (tail latency {elapsed - audio_seconds:+.3f}s)")
    print("transcript:", " ".join("".join(parts).split()))


if __name__ == "__main__":
    main()
