#!/usr/bin/env python3
"""Generate the NVR Status app icon (1024x1024 PNG for MyGo).

Design: a modern CCTV bullet camera on a deep navy rounded-square, with a
cyan lens ring, green status LED and signal arcs. Drawn at 4x and downsampled
for smooth edges; the rounded plate fills the whole 1024px canvas.

Run:  python3 scripts/make_icon.py
Out:  resources/icon.png   (MyGo picks this up automatically)
      /tmp/nvrstatus_icon_preview.png
"""
from __future__ import annotations

import math
import os
from pathlib import Path

import numpy as np
from PIL import Image, ImageDraw, ImageFilter

ROOT = Path(__file__).resolve().parent.parent
OUT = ROOT / "resources" / "icon.png"
PREVIEW = Path("/tmp/nvrstatus_icon_preview.png")

S = 1024          # final size
SS = 4            # supersampling factor
N = S * SS
# Fill the canvas: a full-bleed rounded square (macOS shows the PNG as-is;
# the old 824/1024 safe margin made the Dock icon look small).
ART = 1.0
MARGIN = int(N * (1 - ART) / 2)
ART_N = N - 2 * MARGIN
RADIUS = int(ART_N * 0.225)


def rgba(hex_color: str, alpha: int = 255) -> tuple[int, int, int, int]:
    h = hex_color.lstrip("#")
    return int(h[0:2], 16), int(h[2:4], 16), int(h[4:6], 16), alpha


def vgrad(size: tuple[int, int], top: str, bottom: str) -> Image.Image:
    w, h = size
    t = np.array(rgba(top)[:3], dtype=np.float32)
    b = np.array(rgba(bottom)[:3], dtype=np.float32)
    y = np.linspace(0, 1, h, dtype=np.float32)[:, None, None]
    arr = (t * (1 - y) + b * y).astype(np.uint8)
    arr = np.repeat(arr, w, axis=1)
    return Image.fromarray(arr, "RGB").convert("RGBA")


def radial_glow(size: tuple[int, int], center: tuple[float, float], radius: float,
                color: str, alpha: int) -> Image.Image:
    w, h = size
    yy, xx = np.mgrid[0:h, 0:w].astype(np.float32)
    d = np.sqrt((xx - center[0]) ** 2 + (yy - center[1]) ** 2)
    a = np.clip(1 - d / radius, 0, 1) ** 2 * alpha
    rgba_arr = np.zeros((h, w, 4), dtype=np.uint8)
    rgba_arr[..., 0], rgba_arr[..., 1], rgba_arr[..., 2] = rgba(color)[:3]
    rgba_arr[..., 3] = a.astype(np.uint8)
    return Image.fromarray(rgba_arr, "RGBA")


def rounded_mask(size: tuple[int, int], radius: int) -> Image.Image:
    m = Image.new("L", size, 0)
    ImageDraw.Draw(m).rounded_rectangle((0, 0, size[0] - 1, size[1] - 1), radius=radius, fill=255)
    return m


def draw_camera() -> Image.Image:
    """Draw the camera on a transparent N×N layer in 1024 design coordinates."""
    layer = Image.new("RGBA", (N, N), (0, 0, 0, 0))
    d = ImageDraw.Draw(layer)
    k = SS  # design -> supersampled pixels

    def box(x1, y1, x2, y2):
        return (x1 * k, y1 * k, x2 * k, y2 * k)

    def rrect(x1, y1, x2, y2, r, fill):
        d.rounded_rectangle(box(x1, y1, x2, y2), radius=r * k, fill=fill)

    # --- soft ground shadow under the camera ---
    shadow = Image.new("RGBA", (N, N), (0, 0, 0, 0))
    sd = ImageDraw.Draw(shadow)
    sd.ellipse(box(230, 690, 820, 790), fill=(0, 0, 0, 95))
    shadow = shadow.filter(ImageFilter.GaussianBlur(28 * k))
    layer = Image.alpha_composite(layer, shadow)
    d = ImageDraw.Draw(layer)

    # --- mounting bracket (behind the body) ---
    bracket_grad = vgrad((int(180 * k), int(300 * k)), "#E8F0FA", "#8FA2BA").resize((int(180 * k), int(300 * k)))
    bracket_mask = Image.new("L", bracket_grad.size, 0)
    ImageDraw.Draw(bracket_mask).rounded_rectangle((0, 0, bracket_grad.width - 1, bracket_grad.height - 1),
                                                   radius=34 * k, fill=255)
    layer.paste(bracket_grad, (int(430 * k), int(225 * k)), bracket_mask)
    # top plate
    plate_grad = vgrad((int(280 * k), int(62 * k)), "#F3F7FC", "#A8B8CC")
    plate_mask = Image.new("L", plate_grad.size, 0)
    ImageDraw.Draw(plate_mask).rounded_rectangle((0, 0, plate_grad.width - 1, plate_grad.height - 1),
                                                 radius=30 * k, fill=255)
    layer.paste(plate_grad, (int(355 * k), int(205 * k)), plate_mask)
    # bracket shading
    d.rounded_rectangle(box(430, 225, 466, 500), radius=18 * k, fill=(70, 89, 112, 90))

    # --- camera body, rotated slightly ---
    body = Image.new("RGBA", (N, N), (0, 0, 0, 0))
    bd = ImageDraw.Draw(body)

    def bbox(x1, y1, x2, y2):
        return (x1 * k, y1 * k, x2 * k, y2 * k)

    # main housing
    body_grad = vgrad((int(370 * k), int(190 * k)), "#F7FAFE", "#B7C6D8")
    bg_mask = Image.new("L", body_grad.size, 0)
    ImageDraw.Draw(bg_mask).rounded_rectangle((0, 0, body_grad.width - 1, body_grad.height - 1),
                                              radius=62 * k, fill=255)
    body.paste(body_grad, (int(285 * k), int(430 * k)), bg_mask)
    # lower shade
    bd.rounded_rectangle(bbox(300, 560, 650, 620), radius=32 * k, fill=(114, 135, 160, 95))
    # top shield
    shield_grad = vgrad((int(330 * k), int(58 * k)), "#334A66", "#1A2A40")
    sh_mask = Image.new("L", shield_grad.size, 0)
    ImageDraw.Draw(sh_mask).rounded_rectangle((0, 0, shield_grad.width - 1, shield_grad.height - 1),
                                              radius=28 * k, fill=255)
    body.paste(shield_grad, (int(360 * k), int(395 * k)), sh_mask)
    # rear connector (a short cable stub, not an open pipe)
    bd.rounded_rectangle(bbox(252, 512, 300, 566), radius=16 * k, fill=(56, 74, 96, 255))
    bd.rounded_rectangle(bbox(240, 526, 262, 552), radius=10 * k, fill=(35, 49, 68, 255))
    # status LED + glow
    glow = radial_glow((N, N), (350 * k, 485 * k), 34 * k, "#57F287", 170)
    body = Image.alpha_composite(body, glow)
    bd = ImageDraw.Draw(body)
    bd.ellipse(bbox(338, 473, 362, 497), fill=(88, 245, 142, 255))
    bd.ellipse(bbox(344, 479, 356, 491), fill=(226, 255, 235, 255))

    # --- lens housing and glass ---
    lens_glow = radial_glow((N, N), (734 * k, 520 * k), 125 * k, "#38BDF8", 80)
    body = Image.alpha_composite(body, lens_glow)
    bd = ImageDraw.Draw(body)
    bd.rounded_rectangle(bbox(615, 440, 735, 625), radius=56 * k, fill=(38, 55, 78, 255))
    bd.rounded_rectangle(bbox(635, 455, 748, 610), radius=50 * k, fill=(21, 34, 52, 255))
    # cyan lens ring
    bd.ellipse(bbox(668, 445, 800, 620), fill=(11, 22, 38, 255))
    bd.arc(bbox(672, 449, 796, 616), start=195, end=345, fill=(56, 189, 248, 245), width=10 * k)
    bd.arc(bbox(686, 463, 782, 602), start=200, end=340, fill=(125, 225, 255, 200), width=5 * k)
    # lens glass gradient
    lens = radial_glow((N, N), (734 * k, 520 * k), 70 * k, "#1E4E7A", 255)
    body = Image.alpha_composite(body, lens)
    bd = ImageDraw.Draw(body)
    bd.ellipse(bbox(696, 476, 776, 588), fill=(5, 14, 25, 255))
    bd.ellipse(bbox(708, 492, 756, 560), fill=(9, 32, 52, 255))
    # specular highlights
    bd.arc(bbox(706, 486, 770, 566), start=205, end=285, fill=(160, 235, 255, 210), width=5 * k)
    bd.ellipse(bbox(718, 500, 736, 518), fill=(190, 245, 255, 210))

    # rotate the body slightly around its centre
    body = body.rotate(-9, resample=Image.Resampling.BICUBIC, center=(470 * k, 525 * k))
    layer = Image.alpha_composite(layer, body)
    d = ImageDraw.Draw(layer)

    # --- signal arcs from the lens ---
    cx, cy = 805 * k, 545 * k
    for radius, width, alpha in ((95, 12, 235), (145, 9, 165), (195, 6, 95)):
        bbox_arc = (cx - radius * k, cy - radius * k, cx + radius * k, cy + radius * k)
        d.arc(bbox_arc, start=-60, end=42, fill=(56, 189, 248, alpha), width=int(width * k))

    return layer


def main() -> None:
    canvas = Image.new("RGBA", (N, N), (0, 0, 0, 0))

    # rounded-square art plate
    plate = Image.new("RGBA", (ART_N, ART_N), (0, 0, 0, 0))
    plate = Image.alpha_composite(plate, vgrad((ART_N, ART_N), "#223854", "#0B1424"))
    # soft light behind the camera
    plate = Image.alpha_composite(plate, radial_glow((ART_N, ART_N), (ART_N * 0.38, ART_N * 0.30), ART_N * 0.72,
                                                     "#3A6699", 110))
    # very subtle diagonal sheen
    sheen = Image.new("RGBA", (ART_N, ART_N), (0, 0, 0, 0))
    shd = ImageDraw.Draw(sheen)
    shd.polygon([(0, 0), (ART_N * 0.55, 0), (0, ART_N * 0.6)], fill=(255, 255, 255, 10))
    plate = Image.alpha_composite(plate, sheen)

    mask = rounded_mask((ART_N, ART_N), RADIUS)
    canvas.paste(plate, (MARGIN, MARGIN), mask)
    # Clip all camera art (including the signal arcs) to the rounded plate.
    full_mask = Image.new("L", (N, N), 0)
    ImageDraw.Draw(full_mask).rounded_rectangle(
        (MARGIN, MARGIN, MARGIN + ART_N - 1, MARGIN + ART_N - 1),
        radius=RADIUS, fill=255)
    camera = Image.new("RGBA", (N, N), (0, 0, 0, 0))
    camera.paste(draw_camera(), (0, 0), full_mask)
    canvas = Image.alpha_composite(canvas, camera)

    icon = canvas.resize((S, S), Image.Resampling.LANCZOS)
    OUT.parent.mkdir(parents=True, exist_ok=True)
    icon.save(OUT, "PNG")
    icon.resize((256, 256), Image.Resampling.LANCZOS).save(PREVIEW, "PNG")
    print(f"wrote {OUT} ({S}x{S})")
    print(f"preview {PREVIEW}")


if __name__ == "__main__":
    main()
