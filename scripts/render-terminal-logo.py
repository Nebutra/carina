#!/usr/bin/env python3

"""Render terminal derivatives from the approved Carina symbol."""

from __future__ import annotations

import re
import xml.etree.ElementTree as ET
from pathlib import Path

try:
    from PIL import Image, ImageChops, ImageDraw, ImageFont
except ImportError as error:
    raise SystemExit("render-terminal-logo: install Pillow to regenerate this asset") from error


ROOT = Path(__file__).resolve().parent.parent
SYMBOL = ROOT / "docs/brand/assets/logo/carina-symbol.svg"
FONT = ROOT / "docs/brand/assets/fonts/geist-mono-latin-variable.woff2"
OUTPUT = ROOT / "docs/brand/assets/logo/raster/carina-symbol-terminal-microtype.png"
BRAILLE_OUTPUT = ROOT / "docs/brand/assets/logo/carina-symbol-terminal-braille.txt"
SIZE = 1024
BRAND_ROSE = (0x8E, 0x40, 0x53)
BRAILLE_WIDTH = 10
BRAILLE_HEIGHT = 5

# Unicode Braille stores its 2 x 4 pixels in this non-linear bit order.
BRAILLE_BITS = ((0x01, 0x08), (0x02, 0x10), (0x04, 0x20), (0x40, 0x80))


def canonical_mask() -> Image.Image:
    root = ET.parse(SYMBOL).getroot()
    view_box = [float(value) for value in root.attrib["viewBox"].split()]
    path = root.find("{http://www.w3.org/2000/svg}path")
    if path is None:
        raise SystemExit("render-terminal-logo: canonical symbol path is missing")

    subpaths = path.attrib["d"].split("Z")
    polygons: list[list[tuple[float, float]]] = []
    for subpath in subpaths:
        values = [float(value) for value in re.findall(r"-?\d+(?:\.\d+)?", subpath)]
        if values:
            polygons.append(list(zip(values[::2], values[1::2], strict=True)))
    if len(polygons) != 2:
        raise SystemExit("render-terminal-logo: expected outer silhouette and one counterform")

    x, y, width, height = view_box
    inset = 32
    scale = min((SIZE - inset * 2) / width, (SIZE - inset * 2) / height)
    offset_x = (SIZE - width * scale) / 2
    offset_y = (SIZE - height * scale) / 2

    def transform(points: list[tuple[float, float]]) -> list[tuple[float, float]]:
        return [
            ((point_x - x) * scale + offset_x, (point_y - y) * scale + offset_y)
            for point_x, point_y in points
        ]

    mask = Image.new("L", (SIZE, SIZE), 0)
    draw = ImageDraw.Draw(mask)
    draw.polygon(transform(polygons[0]), fill=255)
    draw.polygon(transform(polygons[1]), fill=0)
    return mask


def render_braille(mask: Image.Image) -> None:
    sample_width = BRAILLE_WIDTH * 2
    sample_height = BRAILLE_HEIGHT * 4
    sampled = mask.resize((sample_width, sample_height), Image.Resampling.LANCZOS)
    pixels = sampled.load()
    rows: list[str] = []
    for cell_y in range(BRAILLE_HEIGHT):
        row: list[str] = []
        for cell_x in range(BRAILLE_WIDTH):
            bits = 0
            for dot_y in range(4):
                for dot_x in range(2):
                    # A moderate threshold preserves the counterform while allowing
                    # partially filled contour cells to carry the symbol's rotation.
                    if pixels[cell_x * 2 + dot_x, cell_y * 4 + dot_y] >= 112:
                        bits |= BRAILLE_BITS[dot_y][dot_x]
            row.append(chr(0x2800 + bits))
        rows.append("".join(row).rstrip())

    BRAILLE_OUTPUT.write_text("\n".join(rows) + "\n", encoding="utf-8")
    print(f"rendered {BRAILLE_OUTPUT.relative_to(ROOT)}")


def render_microtype(mask: Image.Image) -> None:
    font = ImageFont.truetype(str(FONT), 20)
    text_layer = Image.new("L", (SIZE, SIZE), 0)
    draw = ImageDraw.Draw(text_layer)
    token = "carina"
    token_width = draw.textlength(token, font=font)
    step_x = int(token_width + 40)
    step_y = 29

    for row, row_y in enumerate(range(22, SIZE, step_y)):
        column_x = -step_x + (row % 3) * step_x // 3
        while column_x < SIZE:
            opacity = 236 if (row + column_x // max(step_x, 1)) % 4 else 188
            draw.text((column_x, row_y), token, font=font, fill=opacity)
            column_x += step_x

    clipped = Image.composite(text_layer, Image.new("L", (SIZE, SIZE), 0), mask)
    quiet_silhouette = mask.point(lambda value: 10 if value > 0 else 0)
    alpha = ImageChops.lighter(clipped, quiet_silhouette)
    output = Image.new("RGBA", (SIZE, SIZE), (*BRAND_ROSE, 0))
    output.putalpha(alpha)
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    output.save(OUTPUT, optimize=True)
    print(f"rendered {OUTPUT.relative_to(ROOT)}")


def render() -> None:
    mask = canonical_mask()
    render_microtype(mask)
    render_braille(mask)


if __name__ == "__main__":
    render()
