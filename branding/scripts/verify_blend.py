#!/usr/bin/env python3
"""Headlessly verify the generated NosNode Seer Blender scene structure."""

from __future__ import annotations

import json

import bpy


BLENDER_TARGET = (4, 0, 2)
EXPECTED_COLLECTIONS = {
    "MARK — crystal ball cosmos master",
    "ICON — simplified orb and pedestal",
    "SPACE — procedural nebula background",
    "HERO — crystal ball foreground",
    "CAMERAS + LIGHTS",
}
EXPECTED_CAMERAS = {
    "Camera / detailed crystal ball 1:1",
    "Camera / simplified crystal ball 1:1",
    "Camera / crystal ball hero 8:3",
    "Camera / standalone space 16:9",
}
EXPECTED_COLLECTION_OBJECTS = {
    "MARK — crystal ball cosmos master": 96,
    "ICON — simplified orb and pedestal": 20,
    "SPACE — procedural nebula background": 122,
    "HERO — crystal ball foreground": 96,
    "CAMERAS + LIGHTS": 7,
}
EXPECTED_OBJECTS = 341


def main() -> None:
    scene = bpy.context.scene
    file_images = [image.name for image in bpy.data.images if image.source == "FILE"]
    fonts = [obj.name for obj in bpy.data.objects if obj.type == "FONT"]
    linked = [library.filepath for library in bpy.data.libraries]
    collections = {item.name for item in bpy.data.collections}
    collection_objects = {item.name: len(item.objects) for item in bpy.data.collections}
    cameras = {item.name for item in bpy.data.cameras}

    assertions = {
        "blender_version": tuple(bpy.app.version) == BLENDER_TARGET,
        "engine": scene.render.engine == "BLENDER_EEVEE",
        "render_samples": scene.eevee.taa_render_samples == 64,
        "viewport_samples": scene.eevee.taa_samples == 32,
        "frame_range": (scene.frame_start, scene.frame_end) == (1, 1),
        "collections": collections == EXPECTED_COLLECTIONS,
        "collection_object_counts": collection_objects == EXPECTED_COLLECTION_OBJECTS,
        "cameras": cameras == EXPECTED_CAMERAS,
        "object_count": len(bpy.data.objects) == EXPECTED_OBJECTS,
        "no_actions": len(bpy.data.actions) == 0,
        "no_linked_libraries": not linked,
        "no_file_images": not file_images,
        "no_font_objects": not fonts,
        "relative_render_path": scene.render.filepath.startswith("//.render/"),
        "hero_default": scene.camera is not None and scene.camera.name == "Camera / crystal ball hero 8:3",
        "one_frame": scene.frame_current == 1,
    }
    payload = {
        "blend": bpy.data.filepath,
        "version": bpy.app.version_string,
        "engine": scene.render.engine,
        "samples": scene.eevee.taa_render_samples,
        "viewport_samples": scene.eevee.taa_samples,
        "collections": sorted(collections),
        "collection_objects": collection_objects,
        "object_count": len(bpy.data.objects),
        "actions": len(bpy.data.actions),
        "linked_libraries": len(linked),
        "file_images": len(file_images),
        "font_objects": len(fonts),
        "render_filepath": scene.render.filepath,
        "assertions": assertions,
        "ok": all(assertions.values()),
    }
    print("NOSNODE_SEER_SCENE=" + json.dumps(payload, sort_keys=True))
    if not payload["ok"]:
        for name, passed in assertions.items():
            if not passed:
                print(f"SCENE ERROR {name}")
        raise SystemExit(1)


if __name__ == "__main__":
    main()
