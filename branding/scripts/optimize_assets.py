#!/usr/bin/env python3
"""Create optimized NosNode Seer assets from deterministic Blender masters.

Requires pinned Pillow. Metadata is stripped, transparent pixels are normalized,
and every resize/compression parameter is explicit.
"""

from __future__ import annotations

import argparse
import shutil
from pathlib import Path

from PIL import Image


MARK_MASTER = "nosnode-seer-mark-master.png"
ICON_MASTER = "nosnode-seer-icon-master.png"
HERO_MASTER = "nosnode-seer-hero-master.png"
SPACE_MASTER = "nosnode-seer-space-master.png"
SVG_SOURCE = "nosnode-seer-mark.svg"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input-dir", type=Path, default=Path("branding/.render"))
    parser.add_argument("--output-dir", type=Path, default=Path("branding/assets"))
    return parser.parse_args()


def normalize_transparent_rgb(image: Image.Image) -> Image.Image:
    image = image.convert("RGBA")
    pixels = list(image.get_flattened_data())
    image.putdata(
        [
            (0, 0, 0, 0) if alpha == 0 else (red, green, blue, alpha)
            for red, green, blue, alpha in pixels
        ]
    )
    return image


def clear_one_pixel_border(image: Image.Image) -> Image.Image:
    image = image.copy()
    pixels = image.load()
    width, height = image.size
    for x in range(width):
        pixels[x, 0] = (0, 0, 0, 0)
        pixels[x, height - 1] = (0, 0, 0, 0)
    for y in range(height):
        pixels[0, y] = (0, 0, 0, 0)
        pixels[width - 1, y] = (0, 0, 0, 0)
    return image


def resize_rgba(image: Image.Image, size: tuple[int, int]) -> Image.Image:
    resized = normalize_transparent_rgb(image.resize(size, Image.Resampling.LANCZOS))
    return clear_one_pixel_border(resized)


def save_png(image: Image.Image, path: Path) -> None:
    image.save(path, format="PNG", optimize=True, compress_level=9)


def main() -> None:
    args = parse_args()
    input_dir = args.input_dir.resolve()
    output_dir = args.output_dir.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)

    source_paths = {
        "mark": input_dir / MARK_MASTER,
        "icon": input_dir / ICON_MASTER,
        "hero": input_dir / HERO_MASTER,
        "space": input_dir / SPACE_MASTER,
        "svg": input_dir / SVG_SOURCE,
    }
    for required in source_paths.values():
        if not required.is_file():
            raise FileNotFoundError(f"Missing generated source: {required}")

    # The output directory is exclusively generated brand delivery material.
    # Remove obsolete identity renders so old watchtower assets cannot coexist
    # under ambiguous active names.
    for stale in output_dir.glob("nosnode-seer-*"):
        if stale.is_file():
            stale.unlink()

    with Image.open(source_paths["mark"]) as source:
        mark = normalize_transparent_rgb(source)
        if mark.size != (1024, 1024):
            raise ValueError(f"Unexpected mark master dimensions: {mark.size}")
        save_png(mark, output_dir / "nosnode-seer-mark-1024.png")
        resize_rgba(mark, (512, 512)).save(
            output_dir / "nosnode-seer-mark-512.webp",
            format="WEBP",
            lossless=True,
            method=6,
            exact=True,
        )

    with Image.open(source_paths["icon"]) as source:
        icon = normalize_transparent_rgb(source)
        if icon.size != (1024, 1024):
            raise ValueError(f"Unexpected icon master dimensions: {icon.size}")
        for dimension in (256, 64, 32, 16):
            save_png(
                resize_rgba(icon, (dimension, dimension)),
                output_dir / f"nosnode-seer-favicon-{dimension}.png",
            )

    with Image.open(source_paths["hero"]) as source:
        hero = source.convert("RGB")
        if hero.size != (1920, 720):
            raise ValueError(f"Unexpected hero master dimensions: {hero.size}")
        hero.save(
            output_dir / "nosnode-seer-hero-1920x720.webp",
            format="WEBP",
            quality=80,
            method=6,
        )
        save_png(
            hero.resize((1280, 480), Image.Resampling.LANCZOS),
            output_dir / "nosnode-seer-hero-1280x480.png",
        )

    with Image.open(source_paths["space"]) as source:
        space = source.convert("RGB")
        if space.size != (1920, 1080):
            raise ValueError(f"Unexpected space master dimensions: {space.size}")
        space.save(
            output_dir / "nosnode-seer-space-1920x1080.webp",
            format="WEBP",
            quality=70,
            method=6,
        )
        space.resize((1280, 720), Image.Resampling.LANCZOS).save(
            output_dir / "nosnode-seer-space-1280x720.webp",
            format="WEBP",
            quality=68,
            method=6,
        )

    shutil.copyfile(source_paths["svg"], output_dir / SVG_SOURCE)
    print(f"NOSNODE_SEER_ASSET_DIR={output_dir}")
    print("NOSNODE_SEER_WEBP=hero:q80:m6 space:q70/q68:m6")


if __name__ == "__main__":
    main()
