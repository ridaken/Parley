#!/usr/bin/env python3
"""
Generates Parley's app icon from code so it is reproducible and matches the app
aesthetic (deep-violet tile + the audio-waveform motif used in the header).

Outputs:
  build/appicon.png        1024x1024 RGBA — the Wails source icon
  build/windows/icon.ico    multi-size .ico used by the Windows build

Run:  python scripts/gen-icon.py
Requires: Pillow  (pip install Pillow)
"""
from __future__ import annotations

import os
from PIL import Image, ImageDraw

SIZE = 1024
ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))


def lerp(a, b, t):
    return tuple(round(a[i] + (b[i] - a[i]) * t) for i in range(len(a)))


def rounded_mask(size: int, radius: int) -> Image.Image:
    m = Image.new("L", (size, size), 0)
    d = ImageDraw.Draw(m)
    d.rounded_rectangle([0, 0, size - 1, size - 1], radius=radius, fill=255)
    return m


def diagonal_gradient(size: int, top_left, bottom_right) -> Image.Image:
    """Smooth diagonal (TL->BR) gradient — premium violet, matches --primary."""
    g = Image.new("RGB", (size, size))
    px = g.load()
    for y in range(size):
        for x in range(size):
            t = (x + y) / (2 * (size - 1))
            px[x, y] = lerp(top_left, bottom_right, t)
    return g


def main() -> None:
    # Supersample for crisp rounded corners and bar caps, then downscale.
    ss = 2
    s = SIZE * ss

    # Violet palette drawn from the app's --primary (oklch ~295 hue).
    tl = (0x8B, 0x5C, 0xF6)  # violet-500
    br = (0x4C, 0x1D, 0x95)  # violet-900
    bg = diagonal_gradient(s, tl, br)

    # Soft top highlight so the tile reads as a glossy surface.
    hi = Image.new("L", (s, s), 0)
    hd = ImageDraw.Draw(hi)
    hd.ellipse([-s * 0.3, -s * 0.6, s * 1.3, s * 0.5], fill=40)
    bg = Image.composite(Image.new("RGB", (s, s), (255, 255, 255)), bg, hi)

    icon = Image.new("RGBA", (s, s), (0, 0, 0, 0))
    icon.paste(bg, (0, 0))
    icon.putalpha(rounded_mask(s, radius=int(s * 0.225)))

    # Audio-waveform bars (the header's AudioLines motif): symmetric equalizer.
    draw = ImageDraw.Draw(icon)
    heights = [0.40, 0.64, 0.86, 1.00, 0.86, 0.64, 0.40]
    n = len(heights)
    bar_w = s * 0.072
    gap = s * 0.052
    total_w = n * bar_w + (n - 1) * gap
    x = (s - total_w) / 2
    cy = s / 2
    max_h = s * 0.52
    light = (0xF5, 0xF3, 0xFF, 255)  # near-white lavender for contrast on violet
    for h in heights:
        bh = max_h * h
        draw.rounded_rectangle(
            [x, cy - bh / 2, x + bar_w, cy + bh / 2],
            radius=bar_w / 2,
            fill=light,
        )
        x += bar_w + gap

    icon = icon.resize((SIZE, SIZE), Image.LANCZOS)

    png_path = os.path.join(ROOT, "build", "appicon.png")
    icon.save(png_path)
    print("wrote", png_path)

    ico_path = os.path.join(ROOT, "build", "windows", "icon.ico")
    icon.save(
        ico_path,
        sizes=[(16, 16), (24, 24), (32, 32), (48, 48), (64, 64), (128, 128), (256, 256)],
    )
    print("wrote", ico_path)


if __name__ == "__main__":
    main()
