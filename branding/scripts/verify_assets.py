#!/usr/bin/env python3
"""Verify NosNode Seer asset dimensions, transparency, size budgets, and hashes."""

from __future__ import annotations

import argparse
import hashlib
import json
from pathlib import Path

from PIL import Image


EXPECTED = {
    "nosnode-seer-mark-1024.png": ((1024, 1024), "PNG", True, 1_200_000),
    "nosnode-seer-mark-512.webp": ((512, 512), "WEBP", True, 450_000),
    "nosnode-seer-favicon-256.png": ((256, 256), "PNG", True, 220_000),
    "nosnode-seer-favicon-64.png": ((64, 64), "PNG", True, 45_000),
    "nosnode-seer-favicon-32.png": ((32, 32), "PNG", True, 18_000),
    "nosnode-seer-favicon-16.png": ((16, 16), "PNG", True, 8_000),
    "nosnode-seer-hero-1920x720.webp": ((1920, 720), "WEBP", False, 700_000),
    "nosnode-seer-hero-1280x480.png": ((1280, 480), "PNG", False, 1_250_000),
}
SVG_NAME = "nosnode-seer-mark.svg"
SVG_MAX_BYTES = 12_000
TOTAL_ASSET_BUDGET = 3_500_000


def parse_args() -> argparse.Namespace:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--asset-dir", type=Path, default=Path("branding/assets"))
    parser.add_argument("--json", action="store_true", help="Print machine-readable inventory.")
    return parser.parse_args()


def sha256(path: Path) -> str:
    digest = hashlib.sha256()
    with path.open("rb") as handle:
        for chunk in iter(lambda: handle.read(1024 * 1024), b""):
            digest.update(chunk)
    return digest.hexdigest()


def visible_bbox_and_coverage(image: Image.Image) -> tuple[tuple[int, int, int, int], float]:
    alpha = image.convert("RGBA").getchannel("A")
    bbox = alpha.getbbox()
    if bbox is None:
        raise ValueError("transparent asset contains no visible pixels")
    nonzero = sum(1 for value in alpha.get_flattened_data() if value > 16)
    coverage = nonzero / (image.width * image.height)
    return bbox, coverage


def edge_alpha_is_clear(image: Image.Image) -> bool:
    alpha = image.convert("RGBA").getchannel("A")
    edge_values = []
    edge_values.extend(alpha.crop((0, 0, image.width, 1)).get_flattened_data())
    edge_values.extend(alpha.crop((0, image.height - 1, image.width, image.height)).get_flattened_data())
    edge_values.extend(alpha.crop((0, 0, 1, image.height)).get_flattened_data())
    edge_values.extend(alpha.crop((image.width - 1, 0, image.width, image.height)).get_flattened_data())
    return max(edge_values, default=0) == 0


def main() -> None:
    args = parse_args()
    asset_dir = args.asset_dir.resolve()
    errors: list[str] = []
    inventory: list[dict[str, object]] = []

    for name, (expected_size, expected_format, transparent, max_bytes) in EXPECTED.items():
        path = asset_dir / name
        if not path.is_file():
            errors.append(f"missing: {name}")
            continue
        byte_size = path.stat().st_size
        if byte_size > max_bytes:
            errors.append(f"{name}: {byte_size} bytes exceeds {max_bytes}")
        with Image.open(path) as image:
            image.load()
            if image.size != expected_size:
                errors.append(f"{name}: dimensions {image.size}, expected {expected_size}")
            if image.format != expected_format:
                errors.append(f"{name}: format {image.format}, expected {expected_format}")
            alpha = "A" in image.getbands()
            if alpha != transparent:
                errors.append(f"{name}: alpha={alpha}, expected {transparent}")
            record: dict[str, object] = {
                "path": f"branding/assets/{name}",
                "format": image.format,
                "mode": image.mode,
                "width": image.width,
                "height": image.height,
                "bytes": byte_size,
                "sha256": sha256(path),
            }
            if transparent:
                bbox, coverage = visible_bbox_and_coverage(image)
                record["visible_bbox"] = list(bbox)
                record["coverage"] = round(coverage, 5)
                if not edge_alpha_is_clear(image):
                    errors.append(f"{name}: visible alpha touches image edge")
                if name.startswith("nosnode-seer-favicon-") and image.width <= 32 and not 0.22 <= coverage <= 0.72:
                    errors.append(f"{name}: favicon visible coverage {coverage:.3f} is outside 0.22–0.72")
            inventory.append(record)

    svg = asset_dir / SVG_NAME
    if not svg.is_file():
        errors.append(f"missing: {SVG_NAME}")
    else:
        svg_text = svg.read_text(encoding="utf-8")
        if "viewBox=\"0 0 512 512\"" not in svg_text:
            errors.append(f"{SVG_NAME}: missing expected 512 viewBox")
        if any(term in svg_text.lower() for term in ("<image", "font-family", "data:")):
            errors.append(f"{SVG_NAME}: fallback must not embed images, data URIs, or fonts")
        if svg.stat().st_size > SVG_MAX_BYTES:
            errors.append(f"{SVG_NAME}: exceeds {SVG_MAX_BYTES} bytes")
        inventory.append(
            {
                "path": f"branding/assets/{SVG_NAME}",
                "format": "SVG",
                "mode": "vector",
                "width": 512,
                "height": 512,
                "bytes": svg.stat().st_size,
                "sha256": sha256(svg),
            }
        )

    total_bytes = sum(int(record["bytes"]) for record in inventory)
    if total_bytes > TOTAL_ASSET_BUDGET:
        errors.append(f"asset total {total_bytes} bytes exceeds {TOTAL_ASSET_BUDGET}")

    payload = {
        "asset_dir": str(asset_dir),
        "asset_total_bytes": total_bytes,
        "asset_budget_bytes": TOTAL_ASSET_BUDGET,
        "assets": sorted(inventory, key=lambda item: str(item["path"])),
        "errors": errors,
        "ok": not errors,
    }
    if args.json:
        print(json.dumps(payload, indent=2, sort_keys=True))
    else:
        for record in payload["assets"]:
            print(
                f"{record['path']} {record['width']}x{record['height']} "
                f"{record['format']} {record['bytes']} {record['sha256']}"
            )
        print(f"TOTAL {total_bytes}/{TOTAL_ASSET_BUDGET} bytes")
        print("VERIFY OK" if not errors else "VERIFY FAILED")
        for error in errors:
            print(f"ERROR {error}")
    if errors:
        raise SystemExit(1)


if __name__ == "__main__":
    main()
