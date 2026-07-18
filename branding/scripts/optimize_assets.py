#!/usr/bin/env python3
"""Create review-sized NosNode Seer web assets from Blender master renders.

Requires Pillow. The script deliberately strips metadata, normalizes fully
transparent pixels, and applies fixed resize/compression settings.
"""

from __future__ import annotations

import argparse
import shutil
from pathlib import Path

from PIL import Image


MARK_MASTER = "nosnode-seer-mark-master.png"
HERO_MASTER = "nosnode-seer-hero-master.png"
SVG_SOURCE = "nosnode-seer-mark.svg"


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--input-dir", type=Path, default=Path("branding/.render"))
    parser.add_argument("--output-dir", type=Path, default=Path("branding/assets"))
    return parser.parse_args()


def normalize_transparent_rgb(image: Image.Image) -> Image.Image:
    image = image.convert("RGBA")
    pixels = list(image.get_flattened_data())
    image.putdata([(0, 0, 0, 0) if alpha == 0 else (red, green, blue, alpha) for red, green, blue, alpha in pixels])
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

    mark_path = input_dir / MARK_MASTER
    hero_path = input_dir / HERO_MASTER
    svg_path = input_dir / SVG_SOURCE
    for required in (mark_path, hero_path, svg_path):
        if not required.is_file():
            raise FileNotFoundError(f"Missing generated source: {required}")

    with Image.open(mark_path) as source:
        mark = normalize_transparent_rgb(source)
        if mark.size != (1024, 1024):
            raise ValueError(f"Unexpected mark master dimensions: {mark.size}")
        save_png(mark, output_dir / "nosnode-seer-mark-1024.png")
        mark_512 = resize_rgba(mark, (512, 512))
        mark_512.save(
            output_dir / "nosnode-seer-mark-512.webp",
            format="WEBP",
            lossless=True,
            method=6,
            exact=True,
        )
        for dimension in (256, 64, 32, 16):
            save_png(
                resize_rgba(mark, (dimension, dimension)),
                output_dir / f"nosnode-seer-favicon-{dimension}.png",
            )

    with Image.open(hero_path) as source:
        hero = source.convert("RGB")
        if hero.size != (1920, 720):
            raise ValueError(f"Unexpected hero master dimensions: {hero.size}")
        hero.save(
            output_dir / "nosnode-seer-hero-1920x720.webp",
            format="WEBP",
            quality=88,
            method=6,
        )
        hero_1280 = hero.resize((1280, 480), Image.Resampling.LANCZOS)
        hero_1280.save(
            output_dir / "nosnode-seer-hero-1280x480.png",
            format="PNG",
            optimize=True,
            compress_level=9,
        )

    shutil.copyfile(svg_path, output_dir / SVG_SOURCE)
    print(f"NOSNODE_SEER_ASSET_DIR={output_dir}")


if __name__ == "__main__":
    main()
