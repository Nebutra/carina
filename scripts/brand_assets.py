#!/usr/bin/env python3

import argparse
import hashlib
import json
import struct
import sys
import xml.etree.ElementTree as ET
from pathlib import Path


ROOT = Path(__file__).resolve().parent.parent
BRAND = ROOT / "docs" / "brand"
MANIFEST = BRAND / "asset-manifest.json"

CANONICAL_LOGOS = [
    "docs/brand/assets/logo/carina-symbol.svg",
    "docs/brand/assets/logo/carina-wordmark.svg",
]
LOGO_DERIVATIVES = [
    "docs/brand/assets/logo/carina-horizontal-brand.svg",
    "docs/brand/assets/logo/carina-horizontal-monochrome.svg",
    "docs/brand/assets/logo/carina-stacked-brand.svg",
    "docs/brand/assets/logo/carina-symbol-high-contrast.svg",
    "docs/brand/assets/logo/carina-sprite.svg",
    "docs/brand/assets/logo/raster/carina-symbol.png",
    "docs/brand/assets/logo/raster/carina-wordmark.png",
]
FONT_FILES = [
    "docs/brand/assets/fonts/CarinaDisplayAlpha-Regular.ttf",
    "docs/brand/assets/fonts/CarinaDisplayAlpha-Regular.woff2",
    "docs/brand/assets/fonts/carina-display-alpha.css",
    "docs/brand/assets/fonts/fonts.css",
    "docs/brand/assets/fonts/geist-sans-latin-variable.woff2",
    "docs/brand/assets/fonts/geist-mono-latin-variable.woff2",
    "docs/brand/assets/fonts/LICENSE-Geist-Sans.txt",
    "docs/brand/assets/fonts/LICENSE-Geist-Mono.txt",
]
FONT_GLYPHS = [
    *(f"docs/brand/assets/fonts/glyphs-svg/uppercase/{letter}.svg" for letter in "ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
    *(f"docs/brand/assets/fonts/glyphs-svg/lowercase/{letter}.svg" for letter in "abcdefghijklmnopqrstuvwxyz"),
]
FONT_GLYPH_PNGS = [
    *(f"docs/brand/assets/fonts/glyphs-png/uppercase/{letter}.png" for letter in "ABCDEFGHIJKLMNOPQRSTUVWXYZ"),
    *(f"docs/brand/assets/fonts/glyphs-png/lowercase/{letter}.png" for letter in "abcdefghijklmnopqrstuvwxyz"),
]
SPECIMENS = [
    "docs/brand/assets/specimens/font-carina.png",
    "docs/brand/assets/specimens/font-uppercase.png",
    "docs/brand/assets/specimens/font-lowercase.png",
]
TOKEN_SOURCES = [
    "docs/brand/design-system/DESIGN.md",
    "docs/brand/design-system/design-tokens.json",
    "docs/brand/design-system/variables.css",
    "docs/brand/design-system/tailwind-v4.css",
    "docs/brand/design-system/carina.ts",
]
CONSUMER_DERIVATIVES = [
    "docs/brand/assets/hero/carina-readme-hero.webp",
    "integrations/vscode/media/carina.svg",
]


def media_type(path: Path) -> str:
    return {
        ".svg": "image/svg+xml",
        ".png": "image/png",
        ".webp": "image/webp",
        ".ttf": "font/ttf",
        ".woff2": "font/woff2",
        ".css": "text/css",
        ".json": "application/json",
        ".md": "text/markdown",
        ".ts": "text/typescript",
        ".txt": "text/plain",
    }[path.suffix.lower()]


def digest(path: Path) -> str:
    hasher = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            hasher.update(chunk)
    return hasher.hexdigest()


def inventory() -> list[dict[str, object]]:
    groups = [
        (CANONICAL_LOGOS, "canonical"),
        (LOGO_DERIVATIVES, "derivative"),
        (FONT_FILES, "font"),
        (FONT_GLYPHS, "font-source"),
        (FONT_GLYPH_PNGS, "font-raster-derivative"),
        (SPECIMENS, "evidence"),
        (TOKEN_SOURCES, "token-source"),
        (CONSUMER_DERIVATIVES, "consumer-derivative"),
    ]
    assets: list[dict[str, object]] = []
    for paths, role in groups:
        for relative in paths:
            absolute = ROOT / relative
            if not absolute.is_file():
                raise ValueError(f"missing approved asset: {relative}")
            assets.append(
                {
                    "path": relative,
                    "role": role,
                    "mediaType": media_type(absolute),
                    "bytes": absolute.stat().st_size,
                    "sha256": digest(absolute),
                }
            )
    return assets


def validate_structure() -> None:
    for path in [*(ROOT / item for item in CANONICAL_LOGOS + LOGO_DERIVATIVES), *(ROOT / item for item in FONT_GLYPHS)]:
        if path.suffix == ".svg":
            ET.parse(path)

    ttf = ROOT / FONT_FILES[0]
    if ttf.read_bytes()[:4] != b"\x00\x01\x00\x00":
        raise ValueError("CarinaDisplayAlpha-Regular.ttf has an invalid TrueType header")
    for relative in FONT_FILES:
        path = ROOT / relative
        if path.suffix == ".woff2" and path.read_bytes()[:4] != b"wOF2":
            raise ValueError(f"invalid WOFF2 header: {relative}")

    font_css = (BRAND / "assets" / "fonts" / "fonts.css").read_text(encoding="utf-8")
    for name in ["Carina Display Alpha", "Geist Sans", "Geist Mono"]:
        if f'font-family: "{name}"' not in font_css:
            raise ValueError(f"fonts.css does not register {name}")

    for relative in FONT_GLYPH_PNGS:
        data = (ROOT / relative).read_bytes()
        if data[:8] != b"\x89PNG\r\n\x1a\n":
            raise ValueError(f"invalid PNG signature: {relative}")
        width, height = struct.unpack(">II", data[16:24])
        if (width, height) != (512, 512):
            raise ValueError(f"glyph PNG must be 512x512: {relative}")

    tokens = json.loads((BRAND / "design-system" / "design-tokens.json").read_text())
    primitives = tokens["color"]["primitive"]
    semantic = tokens["color"]["semantic"]
    brand_font = tokens["font"]["family"]["brand"]["$value"]
    display_font = tokens["font"]["family"]["display"]["$value"]
    expected = {
        "void": "#0d1214",
        "brand-rose": "#8e4053",
        "ion-cyan": "#8edbd2",
        "starlight": "#f3f0e8",
    }
    for name, value in expected.items():
        if primitives[name]["$value"].lower() != value:
            raise ValueError(f"design token {name} drifted")
    if semantic["brand-mark"]["$value"] != "{color.primitive.brand-rose}":
        raise ValueError("semantic brand-mark must reference brand-rose")
    if not brand_font or brand_font[0] != "Carina Display Alpha":
        raise ValueError("brand font must begin with Carina Display Alpha")
    if not display_font or display_font[0] != "Geist Sans":
        raise ValueError("product display font must begin with Geist Sans")

    forbidden = ["/Users/", "docs/assets/carina-hero.png"]
    for path in BRAND.rglob("*"):
        if path.is_file() and path.suffix.lower() in {".md", ".json", ".css", ".ts"}:
            text = path.read_text(encoding="utf-8")
            for needle in forbidden:
                if needle in text:
                    raise ValueError(f"forbidden stale reference {needle!r} in {path.relative_to(ROOT)}")

    for path in (BRAND / "design-system").rglob("*"):
        if path.is_file() and path.suffix.lower() in {".md", ".json", ".css", ".ts"}:
            text = path.read_text(encoding="utf-8").lower()
            if "newsreader" in text:
                raise ValueError(f"unrelated display font remains in {path.relative_to(ROOT)}")

    canonical = ROOT / CANONICAL_LOGOS[0]
    vscode_icon = ROOT / "integrations/vscode/media/carina.svg"
    if canonical.read_bytes() != vscode_icon.read_bytes():
        raise ValueError("VS Code icon must be byte-identical to the canonical symbol")

    expected_hero = "docs/brand/assets/hero/carina-readme-hero.webp"
    old_badges = ["0033FE", "0B7285", "0BF1C3", "6D28D9"]
    for readme_name in ["README.md", "README.zh-CN.md", "README.ja.md"]:
        text = (ROOT / readme_name).read_text(encoding="utf-8")
        if expected_hero not in text:
            raise ValueError(f"{readme_name} does not consume the approved hero")
        for color in old_badges:
            if color in text:
                raise ValueError(f"legacy badge color {color} remains in {readme_name}")

    theme = (ROOT / "go/tui/theme/theme.go").read_text(encoding="utf-8")
    for value in expected.values():
        if value not in theme:
            raise ValueError(f"TUI theme does not consume token {value}")
    if "#1a191d" in theme or "#733445" in theme:
        raise ValueError("legacy TUI palette remains in go/tui/theme/theme.go")

    junk = [path for path in BRAND.rglob("*") if path.name in {".DS_Store", "__pycache__"}]
    if junk:
        raise ValueError(f"junk files in brand package: {junk}")


def build_manifest() -> dict[str, object]:
    return {
        "schemaVersion": 1,
        "brand": "Carina",
        "sourceImportedAt": "2026-07-16",
        "assets": inventory(),
    }


def main() -> int:
    parser = argparse.ArgumentParser(description="Update or verify the approved Carina brand asset inventory")
    parser.add_argument("--update", action="store_true", help="write approved sizes and SHA-256 digests")
    args = parser.parse_args()
    try:
        validate_structure()
        current = build_manifest()
        if args.update:
            MANIFEST.write_text(json.dumps(current, indent=2) + "\n", encoding="utf-8")
            print(f"updated {MANIFEST.relative_to(ROOT)} with {len(current['assets'])} assets")
            return 0
        if not MANIFEST.is_file():
            raise ValueError("asset manifest is missing; run with --update after approval")
        recorded = json.loads(MANIFEST.read_text(encoding="utf-8"))
        if recorded != current:
            raise ValueError("asset manifest drifted; inspect changes, then run with --update only if approved")
        print(f"brand assets valid: {len(current['assets'])} files")
        return 0
    except (KeyError, OSError, ValueError, ET.ParseError, json.JSONDecodeError) as error:
        print(f"brand asset validation failed: {error}", file=sys.stderr)
        return 1


if __name__ == "__main__":
    raise SystemExit(main())
