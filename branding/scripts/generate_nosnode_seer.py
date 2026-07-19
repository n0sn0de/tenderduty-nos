#!/usr/bin/env python3
"""Build the original NosNode Seer crystal-ball identity in Blender 4.0.2.

Run from the repository root:

    /usr/bin/blender --background --factory-startup \
      --python branding/scripts/generate_nosnode_seer.py -- \
      --output-dir branding/.render \
      --blend-output branding/nosnode-seer.blend

Only generated Blender geometry, procedural shader nodes, authored numeric
coordinates, and fixed randomness are used. No font, texture, model, logo,
stock asset, or external visual source is loaded.
"""

from __future__ import annotations

import argparse
import math
import random
import sys
from pathlib import Path

import bpy
from mathutils import Vector


SEED = 260718
BLENDER_TARGET = (4, 0, 2)

PALETTE = {
    "void": "#02030B",
    "midnight": "#070B1F",
    "indigo": "#121338",
    "deep_violet": "#291550",
    "violet": "#7657E8",
    "magenta": "#CE4FAE",
    "cyan": "#57D6E8",
    "starlight": "#EEF4FF",
    "warm": "#FFD39A",
    "slate": "#566382",
}


# ---------------------------------------------------------------------------
# Core utilities


def parse_args() -> argparse.Namespace:
    argv = sys.argv[sys.argv.index("--") + 1 :] if "--" in sys.argv else []
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--output-dir", type=Path, default=Path("branding/.render"))
    parser.add_argument("--blend-output", type=Path, default=Path("branding/nosnode-seer.blend"))
    parser.add_argument("--skip-render", action="store_true")
    return parser.parse_args(argv)


def rgba(hex_color: str, alpha: float = 1.0) -> tuple[float, float, float, float]:
    value = hex_color.lstrip("#")
    srgb = [int(value[index : index + 2], 16) / 255.0 for index in (0, 2, 4)]

    def linear(channel: float) -> float:
        return channel / 12.92 if channel <= 0.04045 else ((channel + 0.055) / 1.055) ** 2.4

    red, green, blue = (linear(channel) for channel in srgb)
    return (red, green, blue, alpha)


def clear_scene() -> None:
    bpy.ops.object.select_all(action="SELECT")
    bpy.ops.object.delete(use_global=False)
    for existing_collection in list(bpy.data.collections):
        bpy.data.collections.remove(existing_collection)
    for datablocks in (
        bpy.data.meshes,
        bpy.data.curves,
        bpy.data.materials,
        bpy.data.cameras,
        bpy.data.lights,
    ):
        for block in list(datablocks):
            if block.users == 0:
                datablocks.remove(block)


def collection(name: str) -> bpy.types.Collection:
    result = bpy.data.collections.new(name)
    bpy.context.scene.collection.children.link(result)
    return result


def move_to_collection(obj: bpy.types.Object, target: bpy.types.Collection) -> None:
    for owner in list(obj.users_collection):
        owner.objects.unlink(obj)
    target.objects.link(obj)


def add_material(obj: bpy.types.Object, material: bpy.types.Material) -> None:
    obj.data.materials.append(material)


def add_bevel(obj: bpy.types.Object, width: float, segments: int = 4) -> None:
    bevel = obj.modifiers.new(name="Rounded brand silhouette", type="BEVEL")
    bevel.width = width
    bevel.segments = segments
    bevel.limit_method = "ANGLE"


def principled_material(
    name: str,
    base_color: str,
    *,
    metallic: float = 0.0,
    roughness: float = 0.4,
    emission_color: str | None = None,
    emission_strength: float = 0.0,
    alpha: float = 1.0,
    transmission: float = 0.0,
    coat: float = 0.0,
    ior: float = 1.45,
    specular: float = 0.5,
) -> bpy.types.Material:
    material = bpy.data.materials.new(name)
    material.use_nodes = True
    bsdf = material.node_tree.nodes.get("Principled BSDF")
    bsdf.inputs["Base Color"].default_value = rgba(base_color, alpha)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    if "IOR" in bsdf.inputs:
        bsdf.inputs["IOR"].default_value = ior
    if "Specular IOR Level" in bsdf.inputs:
        bsdf.inputs["Specular IOR Level"].default_value = specular
    if "Coat Weight" in bsdf.inputs:
        bsdf.inputs["Coat Weight"].default_value = coat
    if "Transmission Weight" in bsdf.inputs:
        bsdf.inputs["Transmission Weight"].default_value = transmission
    if emission_color and "Emission Color" in bsdf.inputs:
        bsdf.inputs["Emission Color"].default_value = rgba(emission_color)
        bsdf.inputs["Emission Strength"].default_value = emission_strength
    if alpha < 1.0:
        bsdf.inputs["Alpha"].default_value = alpha
        material.blend_method = "BLEND"
        material.show_transparent_back = False
    return material


def emission_material(name: str, color: str, strength: float) -> bpy.types.Material:
    material = bpy.data.materials.new(name)
    material.use_nodes = True
    nodes = material.node_tree.nodes
    links = material.node_tree.links
    nodes.clear()
    output = nodes.new("ShaderNodeOutputMaterial")
    emission = nodes.new("ShaderNodeEmission")
    emission.inputs["Color"].default_value = rgba(color)
    emission.inputs["Strength"].default_value = strength
    links.new(emission.outputs["Emission"], output.inputs["Surface"])
    return material


def nebula_background_material(name: str) -> bpy.types.Material:
    """Create a quiet diagonal deep-space nebula from deterministic shader nodes."""
    material = bpy.data.materials.new(name)
    material.use_nodes = True
    nodes = material.node_tree.nodes
    links = material.node_tree.links
    nodes.clear()

    output = nodes.new("ShaderNodeOutputMaterial")
    emission = nodes.new("ShaderNodeEmission")
    emission.inputs["Strength"].default_value = 0.72
    texcoord = nodes.new("ShaderNodeTexCoord")
    separate = nodes.new("ShaderNodeSeparateXYZ")
    coarse = nodes.new("ShaderNodeTexNoise")
    coarse.noise_dimensions = "3D"
    coarse.inputs["Scale"].default_value = 2.35
    coarse.inputs["Detail"].default_value = 5.0
    coarse.inputs["Roughness"].default_value = 0.72
    coarse.inputs["Distortion"].default_value = 0.24
    fine = nodes.new("ShaderNodeTexNoise")
    fine.noise_dimensions = "3D"
    fine.inputs["Scale"].default_value = 9.0
    fine.inputs["Detail"].default_value = 2.0
    fine.inputs["Roughness"].default_value = 0.58

    x_shift = nodes.new("ShaderNodeMath")
    x_shift.operation = "SUBTRACT"
    x_shift.inputs[1].default_value = 0.28
    x_scale = nodes.new("ShaderNodeMath")
    x_scale.operation = "MULTIPLY"
    x_scale.inputs[1].default_value = 0.48
    z_shift = nodes.new("ShaderNodeMath")
    z_shift.operation = "SUBTRACT"
    z_shift.inputs[1].default_value = 0.55
    diagonal = nodes.new("ShaderNodeMath")
    diagonal.operation = "ADD"
    diagonal_abs = nodes.new("ShaderNodeMath")
    diagonal_abs.operation = "ABSOLUTE"
    diagonal_scale = nodes.new("ShaderNodeMath")
    diagonal_scale.operation = "MULTIPLY"
    diagonal_scale.inputs[1].default_value = 2.25
    band = nodes.new("ShaderNodeMath")
    band.operation = "SUBTRACT"
    band.inputs[0].default_value = 1.0
    band.use_clamp = True

    cloud = nodes.new("ShaderNodeMath")
    cloud.operation = "MULTIPLY"
    fine_scale = nodes.new("ShaderNodeMath")
    fine_scale.operation = "MULTIPLY"
    fine_scale.inputs[1].default_value = 0.16
    density = nodes.new("ShaderNodeMath")
    density.operation = "ADD"
    ramp = nodes.new("ShaderNodeValToRGB")
    ramp.color_ramp.interpolation = "EASE"
    ramp.color_ramp.elements[0].position = 0.0
    ramp.color_ramp.elements[0].color = rgba(PALETTE["void"])
    ramp.color_ramp.elements[1].position = 1.0
    ramp.color_ramp.elements[1].color = rgba("#284060")
    for position, color in (
        (0.16, "#050817"),
        (0.31, "#10112F"),
        (0.46, "#281346"),
        (0.59, "#64245E"),
        (0.72, "#6B4BA2"),
        (0.86, "#497A91"),
    ):
        element = ramp.color_ramp.elements.new(position)
        element.color = rgba(color)

    links.new(texcoord.outputs["Generated"], separate.inputs["Vector"])
    links.new(texcoord.outputs["Generated"], coarse.inputs["Vector"])
    links.new(texcoord.outputs["Generated"], fine.inputs["Vector"])
    links.new(separate.outputs["X"], x_shift.inputs[0])
    links.new(x_shift.outputs[0], x_scale.inputs[0])
    links.new(separate.outputs["Z"], z_shift.inputs[0])
    links.new(x_scale.outputs[0], diagonal.inputs[0])
    links.new(z_shift.outputs[0], diagonal.inputs[1])
    links.new(diagonal.outputs[0], diagonal_abs.inputs[0])
    links.new(diagonal_abs.outputs[0], diagonal_scale.inputs[0])
    links.new(diagonal_scale.outputs[0], band.inputs[1])
    links.new(coarse.outputs["Fac"], cloud.inputs[0])
    links.new(band.outputs[0], cloud.inputs[1])
    links.new(fine.outputs["Fac"], fine_scale.inputs[0])
    links.new(cloud.outputs[0], density.inputs[0])
    links.new(fine_scale.outputs[0], density.inputs[1])
    links.new(density.outputs[0], ramp.inputs["Fac"])
    links.new(ramp.outputs["Color"], emission.inputs["Color"])
    links.new(emission.outputs["Emission"], output.inputs["Surface"])
    return material


# ---------------------------------------------------------------------------
# Geometry helpers


def cube(
    name: str,
    location: tuple[float, float, float],
    dimensions: tuple[float, float, float],
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    bevel: float = 0.0,
) -> bpy.types.Object:
    bpy.ops.mesh.primitive_cube_add(size=1.0, location=location)
    obj = bpy.context.object
    obj.name = name
    obj.dimensions = dimensions
    bpy.ops.object.transform_apply(location=False, rotation=False, scale=True)
    if bevel:
        add_bevel(obj, bevel)
    add_material(obj, material)
    move_to_collection(obj, target)
    return obj


def uv_sphere(
    name: str,
    location: tuple[float, float, float],
    radius: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    segments: int = 96,
    rings: int = 64,
    scale: tuple[float, float, float] = (1.0, 1.0, 1.0),
) -> bpy.types.Object:
    bpy.ops.mesh.primitive_uv_sphere_add(
        segments=segments,
        ring_count=rings,
        radius=radius,
        location=location,
    )
    obj = bpy.context.object
    obj.name = name
    obj.scale = scale
    bpy.ops.object.transform_apply(location=False, rotation=False, scale=True)
    for polygon in obj.data.polygons:
        polygon.use_smooth = True
    add_material(obj, material)
    move_to_collection(obj, target)
    return obj


def ico_sphere(
    name: str,
    location: tuple[float, float, float],
    radius: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    subdivisions: int = 2,
    scale: tuple[float, float, float] = (1.0, 1.0, 1.0),
) -> bpy.types.Object:
    bpy.ops.mesh.primitive_ico_sphere_add(
        subdivisions=subdivisions,
        radius=radius,
        location=location,
    )
    obj = bpy.context.object
    obj.name = name
    obj.scale = scale
    bpy.ops.object.transform_apply(location=False, rotation=False, scale=True)
    for polygon in obj.data.polygons:
        polygon.use_smooth = subdivisions > 1
    add_material(obj, material)
    move_to_collection(obj, target)
    return obj


def poly_curve(
    name: str,
    points: list[tuple[float, float, float]],
    bevel_depth: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    cyclic: bool = False,
    bevel_resolution: int = 2,
) -> bpy.types.Object:
    data = bpy.data.curves.new(name=name, type="CURVE")
    data.dimensions = "3D"
    data.resolution_u = 2
    data.bevel_depth = bevel_depth
    data.bevel_resolution = bevel_resolution
    data.resolution_u = 2
    spline = data.splines.new("POLY")
    spline.points.add(len(points) - 1)
    for point, coordinate in zip(spline.points, points):
        point.co = (*coordinate, 1.0)
    spline.use_cyclic_u = cyclic
    obj = bpy.data.objects.new(name, data)
    target.objects.link(obj)
    add_material(obj, material)
    return obj


def ellipse_curve(
    name: str,
    center: tuple[float, float, float],
    radius_x: float,
    radius_z: float,
    bevel_depth: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    segments: int = 160,
) -> bpy.types.Object:
    cx, cy, cz = center
    points = [
        (
            cx + math.cos(2.0 * math.pi * step / segments) * radius_x,
            cy,
            cz + math.sin(2.0 * math.pi * step / segments) * radius_z,
        )
        for step in range(segments)
    ]
    return poly_curve(name, points, bevel_depth, material, target, cyclic=True)


def arc_curve(
    name: str,
    center: tuple[float, float, float],
    radius_x: float,
    radius_z: float,
    start: float,
    end: float,
    bevel_depth: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    segments: int = 64,
) -> bpy.types.Object:
    cx, cy, cz = center
    points = [
        (
            cx + math.cos(start + (end - start) * step / (segments - 1)) * radius_x,
            cy,
            cz + math.sin(start + (end - start) * step / (segments - 1)) * radius_z,
        )
        for step in range(segments)
    ]
    return poly_curve(name, points, bevel_depth, material, target)


def plane_xz(
    name: str,
    center: tuple[float, float, float],
    width: float,
    height: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
) -> bpy.types.Object:
    cx, cy, cz = center
    vertices = [
        (cx - width / 2, cy, cz - height / 2),
        (cx + width / 2, cy, cz - height / 2),
        (cx + width / 2, cy, cz + height / 2),
        (cx - width / 2, cy, cz + height / 2),
    ]
    mesh = bpy.data.meshes.new(name)
    mesh.from_pydata(vertices, [], [(0, 1, 2, 3)])
    mesh.update()
    obj = bpy.data.objects.new(name, mesh)
    target.objects.link(obj)
    add_material(obj, material)
    return obj


def create_camera(
    name: str,
    location: tuple[float, float, float],
    target_point: tuple[float, float, float],
    ortho_scale: float,
    target: bpy.types.Collection,
) -> bpy.types.Object:
    data = bpy.data.cameras.new(name)
    data.type = "ORTHO"
    data.ortho_scale = ortho_scale
    data.lens = 50
    data.dof.use_dof = False
    obj = bpy.data.objects.new(name, data)
    obj.location = location
    obj.rotation_euler = (Vector(target_point) - obj.location).to_track_quat("-Z", "Y").to_euler()
    target.objects.link(obj)
    return obj


def create_area_light(
    name: str,
    location: tuple[float, float, float],
    target_point: tuple[float, float, float],
    color: str,
    energy: float,
    size: float,
    target: bpy.types.Collection,
) -> bpy.types.Object:
    data = bpy.data.lights.new(name=name, type="AREA")
    data.energy = energy
    data.color = rgba(color)[:3]
    data.shape = "DISK"
    data.size = size
    obj = bpy.data.objects.new(name=name, object_data=data)
    obj.location = location
    obj.rotation_euler = (Vector(target_point) - obj.location).to_track_quat("-Z", "Y").to_euler()
    target.objects.link(obj)
    return obj


# ---------------------------------------------------------------------------
# Identity geometry


def build_materials() -> dict[str, bpy.types.Material]:
    return {
        "orb": principled_material(
            "Crystal ball / deep indigo interior",
            PALETTE["midnight"],
            roughness=0.26,
            emission_color=PALETTE["deep_violet"],
            emission_strength=0.2,
        ),
        "glass": principled_material(
            "Crystal ball / refractive glass shell",
            "#203B55",
            roughness=0.24,
            emission_color=PALETTE["cyan"],
            emission_strength=0.025,
            alpha=0.075,
            transmission=0.25,
            coat=0.1,
            ior=1.46,
            specular=0.18,
        ),
        "pedestal": principled_material(
            "Crystal ball / midnight pedestal",
            "#0B1028",
            metallic=0.58,
            roughness=0.25,
            emission_color=PALETTE["deep_violet"],
            emission_strength=0.1,
        ),
        "pedestal_edge": principled_material(
            "Crystal ball / violet pedestal edge",
            PALETTE["deep_violet"],
            metallic=0.34,
            roughness=0.24,
            emission_color=PALETTE["violet"],
            emission_strength=0.18,
        ),
        "void": emission_material("Orb / void depth", PALETTE["midnight"], 0.42),
        "violet": emission_material("Cosmos / violet nebula", PALETTE["violet"], 1.15),
        "magenta": emission_material("Cosmos / magenta nebula", PALETTE["magenta"], 1.05),
        "cyan": emission_material("Glass / cyan rim", PALETTE["cyan"], 1.25),
        "starlight": emission_material("Cosmos / cool starlight", PALETTE["starlight"], 1.55),
        "warm": emission_material("Cosmos / restrained warm core", PALETTE["warm"], 1.35),
        "slate": emission_material("Cosmos / quiet constellation", PALETTE["slate"], 0.26),
        "background": nebula_background_material("Space / procedural indigo-magenta nebula"),
    }


def transform_xz(
    x: float,
    z: float,
    *,
    origin: tuple[float, float, float],
    scale: float,
    y: float,
) -> tuple[float, float, float]:
    ox, oy, oz = origin
    return (ox + x * scale, oy + y * scale, oz + z * scale)


def build_pedestal(
    prefix: str,
    origin: tuple[float, float, float],
    scale: float,
    mats: dict[str, bpy.types.Material],
    target: bpy.types.Collection,
    *,
    simplified: bool,
) -> None:
    ox, oy, oz = origin

    def p(x: float, y: float, z: float) -> tuple[float, float, float]:
        return (ox + x * scale, oy + y * scale, oz + z * scale)

    # Three broad rounded solids make a literal, sturdy fortune-teller pedestal.
    # The icon variant raises luminance and weight so the base remains two clear
    # pixel rows at 16 px instead of collapsing into the transparent canvas.
    cube(
        f"{prefix} / pedestal cradle",
        p(0.0, 0.22, -1.98),
        (2.52 * scale, 0.92 * scale, 0.44 * scale),
        mats["pedestal_edge"],
        target,
        bevel=0.17 * scale,
    )
    cube(
        f"{prefix} / pedestal neck",
        p(0.0, 0.3, -2.36),
        (1.5 * scale, 0.98 * scale, (0.64 if simplified else 0.58) * scale),
        mats["pedestal_edge"] if simplified else mats["pedestal"],
        target,
        bevel=0.16 * scale,
    )
    cube(
        f"{prefix} / pedestal foot",
        p(0.0, 0.36, -2.78 if simplified else -2.82),
        ((2.92 if simplified else 2.72) * scale, 1.1 * scale, (0.68 if simplified else 0.52) * scale),
        mats["pedestal_edge"] if simplified else mats["pedestal"],
        target,
        bevel=0.18 * scale,
    )
    cube(
        f"{prefix} / pedestal foot inset",
        p(0.0, -0.28, -2.72),
        (2.2 * scale, 0.14 * scale, 0.09 * scale),
        mats["cyan"] if simplified else mats["violet"],
        target,
        bevel=0.035 * scale,
    )


def spiral_point(theta: float, arm_offset: float, strand_offset: float) -> tuple[float, float]:
    radius = 0.16 + 0.14 * theta + strand_offset
    angle = theta + arm_offset
    x = radius * math.cos(angle)
    z = 0.48 * radius * math.sin(angle)
    rotation = -0.28
    return (
        x * math.cos(rotation) - z * math.sin(rotation),
        x * math.sin(rotation) + z * math.cos(rotation),
    )


def build_internal_cosmos(
    prefix: str,
    origin: tuple[float, float, float],
    scale: float,
    mats: dict[str, bpy.types.Material],
    target: bpy.types.Collection,
    *,
    detailed: bool,
) -> None:
    ox, oy, oz = origin

    def p(x: float, y: float, z: float) -> tuple[float, float, float]:
        return (ox + x * scale, oy + y * scale, oz + z * scale)

    arm_materials = (mats["magenta"], mats["violet"], mats["cyan"])
    if detailed:
        for arm in range(3):
            for strand, strand_offset in enumerate((-0.055, 0.055)):
                points = []
                for step in range(74):
                    theta = 0.22 + 11.65 * step / 73.0
                    x, z = spiral_point(theta, arm * 2.0 * math.pi / 3.0, strand_offset)
                    points.append(p(x, -1.91 - strand * 0.018, z + 0.12))
                poly_curve(
                    f"{prefix} / natural spiral arm {arm + 1}.{strand + 1}",
                    points,
                    (0.05 if strand == 0 else 0.032) * scale,
                    arm_materials[arm],
                    target,
                    bevel_resolution=3,
                )
    else:
        # The icon composition retains one heavy natural-galaxy sweep rather than
        # attempting to downsample the full star field into visual noise.
        for arm in range(2):
            points = []
            for step in range(58):
                theta = 0.15 + 10.2 * step / 57.0
                x, z = spiral_point(theta, arm * math.pi, 0.03)
                points.append(p(x, -1.91, z + 0.12))
            poly_curve(
                f"{prefix} / simplified galaxy sweep {arm + 1}",
                points,
                0.12 * scale,
                mats["magenta"] if arm == 0 else mats["violet"],
                target,
                bevel_resolution=3,
            )

    ico_sphere(
        f"{prefix} / warm galactic core",
        p(-0.03, -1.99, 0.13),
        0.34 * scale,
        mats["warm"],
        target,
        subdivisions=4,
        scale=(1.0, 0.45, 0.74),
    )
    ellipse_curve(
        f"{prefix} / abstract monitored orbit",
        p(0.0, -1.94, 0.15),
        1.72 * scale,
        0.79 * scale,
        0.023 * scale,
        mats["slate"],
        target,
        segments=128,
    )

    constellation_points = [
        (-1.12, 0.88),
        (-0.56, 1.14),
        (0.02, 0.91),
        (0.63, 1.21),
        (1.15, 0.74),
        (0.72, 0.28),
        (1.28, -0.24),
    ]
    edges = ((0, 1), (1, 2), (2, 3), (3, 4), (4, 5), (5, 6))
    if detailed:
        for index, (start, end) in enumerate(edges):
            poly_curve(
                f"{prefix} / monitored chain link {index + 1}",
                [
                    p(constellation_points[start][0], -1.96, constellation_points[start][1]),
                    p(constellation_points[end][0], -1.96, constellation_points[end][1]),
                ],
                0.018 * scale,
                mats["slate"],
                target,
                bevel_resolution=1,
            )
        for index, (x, z) in enumerate(constellation_points):
            ico_sphere(
                f"{prefix} / abstract chain node {index + 1}",
                p(x, -1.99, z),
                (0.07 if index not in (1, 4) else 0.105) * scale,
                mats["cyan"] if index % 3 == 0 else mats["starlight"],
                target,
                subdivisions=2,
            )

    rng = random.Random(SEED + (0 if detailed else 41))
    star_count = 66 if detailed else 7
    for index in range(star_count):
        while True:
            x = rng.uniform(-2.08, 2.08)
            z = rng.uniform(-1.92, 2.02)
            if x * x + (z - 0.08) * (z - 0.08) < 4.1:
                break
        radius = rng.choice((0.026, 0.034, 0.046, 0.065)) if detailed else rng.choice((0.075, 0.1, 0.13))
        material = mats["warm"] if index % 11 == 0 else mats["starlight"]
        ico_sphere(
            f"{prefix} / internal star {index + 1:02d}",
            p(x, -2.02, z),
            radius * scale,
            material,
            target,
            subdivisions=2,
            scale=(1.0, 0.5, 1.0),
        )


def build_crystal_ball(
    prefix: str,
    origin: tuple[float, float, float],
    scale: float,
    mats: dict[str, bpy.types.Material],
    target: bpy.types.Collection,
    *,
    detailed: bool,
) -> None:
    ox, oy, oz = origin

    def p(x: float, y: float, z: float) -> tuple[float, float, float]:
        return (ox + x * scale, oy + y * scale, oz + z * scale)

    build_pedestal(prefix, origin, scale, mats, target, simplified=not detailed)
    uv_sphere(
        f"{prefix} / deep-space globe",
        p(0.0, 0.0, 0.43),
        2.48 * scale,
        mats["orb"],
        target,
        segments=96 if detailed else 64,
        rings=64 if detailed else 48,
        scale=(1.0, 0.72, 1.0),
    )
    build_internal_cosmos(prefix, origin, scale, mats, target, detailed=detailed)

    # A real UV-sphere glass shell plus broad cyan refraction and hand-authored
    # reflected crescents keep the object unmistakably glass rather than a planet.
    uv_sphere(
        f"{prefix} / transparent glass shell",
        p(0.0, -0.38, 0.43),
        2.5 * scale,
        mats["glass"],
        target,
        segments=96 if detailed else 64,
        rings=64 if detailed else 48,
        scale=(1.0, 0.72, 1.0),
    )
    ellipse_curve(
        f"{prefix} / cyan refractive rim",
        p(0.0, -1.98, 0.43),
        2.5 * scale,
        2.5 * scale,
        0.052 * scale if detailed else 0.1 * scale,
        mats["cyan"],
        target,
        segments=192,
    )
    arc_curve(
        f"{prefix} / upper-left glass reflection",
        p(-0.05, -2.08, 0.43),
        2.18 * scale,
        2.18 * scale,
        1.76,
        2.82,
        0.055 * scale if detailed else 0.08 * scale,
        mats["starlight"],
        target,
    )
    arc_curve(
        f"{prefix} / lower-right glass reflection",
        p(0.0, -2.05, 0.43),
        2.26 * scale,
        2.26 * scale,
        -0.82,
        -0.28,
        0.035 * scale if detailed else 0.055 * scale,
        mats["violet"],
        target,
    )


def build_space_environment(
    mats: dict[str, bpy.types.Material],
    target: bpy.types.Collection,
) -> None:
    plane_xz(
        "Space / procedural deep-space nebula field",
        (0.0, 4.0, 0.0),
        27.0,
        15.5,
        mats["background"],
        target,
    )

    rng = random.Random(SEED + 700)
    for index in range(128):
        x = rng.uniform(-12.1, 12.1)
        z = rng.uniform(-6.6, 6.6)
        # Preserve a deliberately quiet title/overlay zone on the right half.
        if x > 0.5 and -3.2 < z < 3.2 and rng.random() < 0.82:
            continue
        radius = rng.choice((0.018, 0.022, 0.03, 0.04, 0.055))
        material = mats["warm"] if index % 17 == 0 else mats["starlight"]
        ico_sphere(
            f"Space / field star {index + 1:03d}",
            (x, 2.6, z),
            radius,
            material,
            target,
            subdivisions=2,
        )

    clusters = (
        ("upper asterism", (-8.8, 4.25), ((0.0, 0.0), (0.8, 0.42), (1.55, 0.18), (2.15, 0.78))),
        ("lower asterism", (-9.7, -4.15), ((0.0, 0.0), (0.72, 0.58), (1.48, 0.12), (2.18, 0.44))),
        ("far asterism", (6.9, 4.55), ((0.0, 0.0), (0.78, 0.28), (1.42, -0.22))),
    )
    for label, (cx, cz), offsets in clusters:
        points = [(cx + dx, 2.5, cz + dz) for dx, dz in offsets]
        for index in range(len(points) - 1):
            poly_curve(
                f"Space / {label} link {index + 1}",
                [points[index], points[index + 1]],
                0.012,
                mats["slate"],
                target,
                bevel_resolution=1,
            )
        for index, point in enumerate(points):
            ico_sphere(
                f"Space / {label} node {index + 1}",
                point,
                0.06 if index else 0.085,
                mats["cyan"] if index == 0 else mats["starlight"],
                target,
                subdivisions=2,
            )


def configure_scene() -> None:
    scene = bpy.context.scene
    scene.render.engine = "BLENDER_EEVEE"
    scene.eevee.taa_render_samples = 64
    scene.eevee.taa_samples = 32
    scene.eevee.use_gtao = True
    scene.eevee.gtao_distance = 3.0
    scene.eevee.gtao_factor = 1.15
    if hasattr(scene.eevee, "use_soft_shadows"):
        scene.eevee.use_soft_shadows = True
    if hasattr(scene.eevee, "use_bloom"):
        scene.eevee.use_bloom = False

    scene.render.resolution_percentage = 100
    scene.render.image_settings.file_format = "PNG"
    scene.render.image_settings.color_mode = "RGBA"
    scene.render.image_settings.color_depth = "8"
    scene.render.image_settings.compression = 15
    scene.render.film_transparent = True
    scene.render.use_file_extension = True
    scene.render.pixel_aspect_x = 1.0
    scene.render.pixel_aspect_y = 1.0
    scene.render.use_overwrite = True
    scene.render.use_placeholder = False
    scene.render.dither_intensity = 1.0

    scene.view_settings.view_transform = "AgX"
    scene.view_settings.look = "None"
    scene.view_settings.exposure = 0.15
    scene.view_settings.gamma = 1.0
    scene.view_settings.use_curve_mapping = False

    world = bpy.data.worlds.new("NosNode Seer / explicit deep-space world") if not scene.world else scene.world
    scene.world = world
    world.use_nodes = True
    background = world.node_tree.nodes.get("Background")
    background.inputs["Color"].default_value = rgba(PALETTE["void"])
    background.inputs["Strength"].default_value = 0.08

    scene.render.filepath = "//.render/nosnode-seer-hero-master.png"
    scene.frame_start = 1
    scene.frame_end = 1
    scene.frame_set(1)


def set_composition(
    collections: dict[str, bpy.types.Collection],
    *,
    mark: bool = False,
    icon: bool = False,
    space: bool = False,
    hero: bool = False,
) -> None:
    states = {"mark": mark, "icon": icon, "space": space, "hero": hero, "rig": True}
    for key, show in states.items():
        collections[key].hide_render = not show
        collections[key].hide_viewport = not show


def render_master(
    filepath: Path,
    camera: bpy.types.Object,
    width: int,
    height: int,
    *,
    transparent: bool,
) -> None:
    scene = bpy.context.scene
    scene.camera = camera
    scene.render.resolution_x = width
    scene.render.resolution_y = height
    scene.render.resolution_percentage = 100
    scene.render.film_transparent = transparent
    scene.render.image_settings.color_mode = "RGBA" if transparent else "RGB"
    scene.render.filepath = str(filepath.resolve())
    bpy.ops.render.render(write_still=True)


def write_svg(path: Path) -> None:
    # This flat fallback uses the same orb/base proportions, natural spiral idea,
    # rim, star nodes, and palette as the Blender compositions.
    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" role="img" aria-labelledby="title desc">
  <title id="title">NosNode Seer crystal ball mark</title>
  <desc id="desc">A glass crystal ball with a spiral cosmos inside and a clear pedestal below.</desc>
  <defs><clipPath id="orb"><circle cx="256" cy="217" r="172"/></clipPath></defs>
  <path d="M172 376 Q172 353 196 345 H316 Q340 353 340 376 L326 402 H186 Z" fill="{PALETTE['deep_violet']}"/>
  <path d="M207 392 H305 L320 461 Q320 474 306 474 H206 Q192 474 192 461 Z" fill="{PALETTE['midnight']}"/>
  <path d="M170 452 Q170 436 187 432 H325 Q342 436 342 452 V469 H170 Z" fill="{PALETTE['midnight']}" stroke="{PALETTE['violet']}" stroke-width="7"/>
  <circle cx="256" cy="217" r="172" fill="{PALETTE['midnight']}"/>
  <g clip-path="url(#orb)" fill="none" stroke-linecap="round">
    <path d="M155 251 C185 146 347 138 364 227 C377 299 274 334 212 287 C161 248 199 185 268 188 C320 191 330 240 297 264 C272 283 233 268 235 239 C237 218 262 209 278 221" stroke="{PALETTE['magenta']}" stroke-width="18"/>
    <path d="M348 168 C277 117 170 169 171 245 C172 311 252 336 314 302 C365 274 356 210 311 190 C271 173 227 196 224 231 C221 259 248 278 273 269" stroke="{PALETTE['violet']}" stroke-width="11"/>
    <path d="M160 207 C219 281 328 289 360 220" stroke="{PALETTE['cyan']}" stroke-width="6" opacity=".82"/>
  </g>
  <circle cx="256" cy="217" r="19" fill="{PALETTE['warm']}"/>
  <g fill="{PALETTE['starlight']}"><circle cx="188" cy="151" r="5"/><circle cx="312" cy="130" r="4"/><circle cx="337" cy="250" r="6"/><circle cx="194" cy="285" r="4"/><circle cx="272" cy="316" r="5"/></g>
  <circle cx="256" cy="217" r="172" fill="none" stroke="{PALETTE['cyan']}" stroke-width="10"/>
  <path d="M151 169 A145 145 0 0 1 207 92" fill="none" stroke="{PALETTE['starlight']}" stroke-linecap="round" stroke-width="9"/>
</svg>
'''
    path.write_text(svg, encoding="utf-8", newline="\n")


def main() -> None:
    args = parse_args()
    if tuple(bpy.app.version) != BLENDER_TARGET:
        raise RuntimeError(
            f"This source targets Blender {'.'.join(map(str, BLENDER_TARGET))}; running {bpy.app.version_string}."
        )

    output_dir = args.output_dir.resolve()
    blend_output = args.blend_output.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    blend_output.parent.mkdir(parents=True, exist_ok=True)

    clear_scene()
    configure_scene()
    mats = build_materials()
    collections = {
        "mark": collection("MARK — crystal ball cosmos master"),
        "icon": collection("ICON — simplified orb and pedestal"),
        "space": collection("SPACE — procedural nebula background"),
        "hero": collection("HERO — crystal ball foreground"),
        "rig": collection("CAMERAS + LIGHTS"),
    }

    build_crystal_ball("Mark", (0.0, 0.0, 0.12), 1.0, mats, collections["mark"], detailed=True)
    build_crystal_ball("Icon", (0.0, 0.0, 0.12), 1.0, mats, collections["icon"], detailed=False)
    build_space_environment(mats, collections["space"])
    build_crystal_ball("Hero", (-5.65, 0.0, 0.04), 1.06, mats, collections["hero"], detailed=True)

    square_camera = create_camera(
        "Camera / detailed crystal ball 1:1",
        (0.0, -18.0, 0.05),
        (0.0, 0.0, 0.05),
        7.45,
        collections["rig"],
    )
    icon_camera = create_camera(
        "Camera / simplified crystal ball 1:1",
        (0.0, -18.0, 0.05),
        (0.0, 0.0, 0.05),
        7.45,
        collections["rig"],
    )
    hero_camera = create_camera(
        "Camera / crystal ball hero 8:3",
        (0.0, -26.0, 0.0),
        (0.0, 0.0, 0.0),
        24.0,
        collections["rig"],
    )
    space_camera = create_camera(
        "Camera / standalone space 16:9",
        (0.0, -26.0, 0.0),
        (0.0, 0.0, 0.0),
        24.0,
        collections["rig"],
    )
    create_area_light(
        "Key / restrained warm starlight",
        (-5.5, -7.5, 6.5),
        (-2.0, 0.0, 0.3),
        PALETTE["starlight"],
        760.0,
        5.2,
        collections["rig"],
    )
    create_area_light(
        "Rim / cyan refraction",
        (7.0, -4.0, 3.5),
        (0.0, 0.0, 0.3),
        PALETTE["cyan"],
        620.0,
        4.5,
        collections["rig"],
    )
    create_area_light(
        "Fill / violet nebula",
        (-6.0, 0.5, -3.8),
        (-2.0, 0.0, 0.0),
        PALETTE["violet"],
        420.0,
        5.5,
        collections["rig"],
    )

    write_svg(output_dir / "nosnode-seer-mark.svg")

    # Save a useful default hero view before and after deterministic renders.
    set_composition(collections, space=True, hero=True)
    bpy.context.scene.camera = hero_camera
    bpy.context.scene.render.filepath = "//.render/nosnode-seer-hero-master.png"
    bpy.ops.wm.save_as_mainfile(filepath=str(blend_output))

    if not args.skip_render:
        set_composition(collections, mark=True)
        render_master(output_dir / "nosnode-seer-mark-master.png", square_camera, 1024, 1024, transparent=True)
        set_composition(collections, icon=True)
        render_master(output_dir / "nosnode-seer-icon-master.png", icon_camera, 1024, 1024, transparent=True)
        set_composition(collections, space=True, hero=True)
        render_master(output_dir / "nosnode-seer-hero-master.png", hero_camera, 1920, 720, transparent=False)
        set_composition(collections, space=True)
        render_master(output_dir / "nosnode-seer-space-master.png", space_camera, 1920, 1080, transparent=False)

    set_composition(collections, space=True, hero=True)
    bpy.context.scene.camera = hero_camera
    bpy.context.scene.render.resolution_x = 1920
    bpy.context.scene.render.resolution_y = 720
    bpy.context.scene.render.film_transparent = False
    bpy.context.scene.render.image_settings.color_mode = "RGB"
    bpy.context.scene.render.filepath = "//.render/nosnode-seer-hero-master.png"
    bpy.ops.wm.save_as_mainfile(filepath=str(blend_output))

    print(f"NOSNODE_SEER_SEED={SEED}")
    print(f"NOSNODE_SEER_BLEND={blend_output}")
    print(f"NOSNODE_SEER_OUTPUT_DIR={output_dir}")
    print("NOSNODE_SEER_ENGINE=BLENDER_EEVEE")
    print("NOSNODE_SEER_SAMPLES=64")


if __name__ == "__main__":
    main()
