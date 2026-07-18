#!/usr/bin/env python3
"""Build and render the original NosNode Seer identity in Blender 4.0.2.

Run from the repository root:

    /usr/bin/blender --background --factory-startup \
      --python branding/scripts/generate_nosnode_seer.py -- \
      --output-dir /tmp/nosnode-seer-render \
      --blend-output branding/nosnode-seer.blend

The script uses only Blender's bundled Python API and generated geometry. It does
not load fonts, textures, logos, or other external visual sources.
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
    "void": "#030512",
    "midnight": "#091129",
    "deep_violet": "#302064",
    "violet": "#795CFF",
    "soft_violet": "#B7A7FF",
    "cyan": "#58D5E8",
    "starlight": "#F1F4FF",
    "slate": "#68718F",
}


def parse_args() -> argparse.Namespace:
    argv = sys.argv[sys.argv.index("--") + 1 :] if "--" in sys.argv else []
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument(
        "--output-dir",
        type=Path,
        default=Path("branding/.render"),
        help="Directory for unoptimized master renders and the generated SVG.",
    )
    parser.add_argument(
        "--blend-output",
        type=Path,
        default=Path("branding/nosnode-seer.blend"),
        help="Path for the generated Blender source file.",
    )
    parser.add_argument(
        "--skip-render",
        action="store_true",
        help="Build and save the .blend source without rendering masters.",
    )
    return parser.parse_args(argv)


def rgba(hex_color: str, alpha: float = 1.0) -> tuple[float, float, float, float]:
    value = hex_color.lstrip("#")
    srgb = [int(value[i : i + 2], 16) / 255.0 for i in (0, 2, 4)]

    def to_linear(channel: float) -> float:
        return channel / 12.92 if channel <= 0.04045 else ((channel + 0.055) / 1.055) ** 2.4

    red, green, blue = (to_linear(channel) for channel in srgb)
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
    col = bpy.data.collections.new(name)
    bpy.context.scene.collection.children.link(col)
    return col


def move_to_collection(obj: bpy.types.Object, target: bpy.types.Collection) -> None:
    for owner in list(obj.users_collection):
        owner.objects.unlink(obj)
    target.objects.link(obj)


def principled_material(
    name: str,
    base_color: str,
    *,
    metallic: float = 0.0,
    roughness: float = 0.4,
    emission_color: str | None = None,
    emission_strength: float = 0.0,
) -> bpy.types.Material:
    material = bpy.data.materials.new(name)
    material.use_nodes = True
    bsdf = material.node_tree.nodes.get("Principled BSDF")
    bsdf.inputs["Base Color"].default_value = rgba(base_color)
    bsdf.inputs["Metallic"].default_value = metallic
    bsdf.inputs["Roughness"].default_value = roughness
    if emission_color and "Emission Color" in bsdf.inputs:
        bsdf.inputs["Emission Color"].default_value = rgba(emission_color)
        bsdf.inputs["Emission Strength"].default_value = emission_strength
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


def gradient_material(name: str) -> bpy.types.Material:
    material = bpy.data.materials.new(name)
    material.use_nodes = True
    nodes = material.node_tree.nodes
    links = material.node_tree.links
    nodes.clear()
    output = nodes.new("ShaderNodeOutputMaterial")
    emission = nodes.new("ShaderNodeEmission")
    texcoord = nodes.new("ShaderNodeTexCoord")
    separate = nodes.new("ShaderNodeSeparateXYZ")
    ramp = nodes.new("ShaderNodeValToRGB")
    ramp.color_ramp.interpolation = "EASE"
    ramp.color_ramp.elements.remove(ramp.color_ramp.elements[1])
    ramp.color_ramp.elements[0].position = 0.0
    ramp.color_ramp.elements[0].color = rgba(PALETTE["void"])
    mid = ramp.color_ramp.elements.new(0.52)
    mid.color = rgba("#07102A")
    high = ramp.color_ramp.elements.new(1.0)
    high.color = rgba("#160F32")
    emission.inputs["Strength"].default_value = 0.82
    links.new(texcoord.outputs["Generated"], separate.inputs["Vector"])
    links.new(separate.outputs["Z"], ramp.inputs["Fac"])
    links.new(ramp.outputs["Color"], emission.inputs["Color"])
    links.new(emission.outputs["Emission"], output.inputs["Surface"])
    return material


def add_material(obj: bpy.types.Object, material: bpy.types.Material) -> None:
    obj.data.materials.append(material)


def add_bevel(obj: bpy.types.Object, width: float, segments: int = 3) -> None:
    bevel = obj.modifiers.new(name="Controlled bevel", type="BEVEL")
    bevel.width = width
    bevel.segments = segments
    bevel.limit_method = "ANGLE"


def cube(
    name: str,
    location: tuple[float, float, float],
    dimensions: tuple[float, float, float],
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    bevel: float = 0.0,
    rotation: tuple[float, float, float] = (0.0, 0.0, 0.0),
) -> bpy.types.Object:
    bpy.ops.mesh.primitive_cube_add(size=1.0, location=location, rotation=rotation)
    obj = bpy.context.object
    obj.name = name
    obj.dimensions = dimensions
    bpy.ops.object.transform_apply(location=False, rotation=False, scale=True)
    if bevel:
        add_bevel(obj, bevel)
    add_material(obj, material)
    move_to_collection(obj, target)
    return obj


def bar_between(
    name: str,
    start: tuple[float, float, float],
    end: tuple[float, float, float],
    depth: float,
    thickness: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    bevel: float,
) -> bpy.types.Object:
    p1 = Vector(start)
    p2 = Vector(end)
    vector = p2 - p1
    midpoint = (p1 + p2) / 2.0
    obj = cube(
        name,
        tuple(midpoint),
        (vector.length, depth, thickness),
        material,
        target,
        bevel=bevel,
    )
    obj.rotation_mode = "QUATERNION"
    obj.rotation_quaternion = vector.to_track_quat("X", "Z")
    return obj


def ico_sphere(
    name: str,
    location: tuple[float, float, float],
    radius: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    subdivisions: int = 3,
    scale: tuple[float, float, float] = (1.0, 1.0, 1.0),
    smooth: bool = False,
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
    if smooth:
        for polygon in obj.data.polygons:
            polygon.use_smooth = True
    add_material(obj, material)
    move_to_collection(obj, target)
    return obj


def cylinder(
    name: str,
    location: tuple[float, float, float],
    radius: float,
    depth: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    vertices: int = 64,
    rotation: tuple[float, float, float] = (math.pi / 2.0, 0.0, 0.0),
    bevel: float = 0.0,
) -> bpy.types.Object:
    bpy.ops.mesh.primitive_cylinder_add(
        vertices=vertices,
        radius=radius,
        depth=depth,
        location=location,
        rotation=rotation,
    )
    obj = bpy.context.object
    obj.name = name
    if bevel:
        add_bevel(obj, bevel)
    add_material(obj, material)
    move_to_collection(obj, target)
    return obj


def tapered_crystal(
    name: str,
    location: tuple[float, float, float],
    radius_bottom: float,
    radius_top: float,
    depth: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    vertices: int = 6,
    rotation: tuple[float, float, float] = (0.0, 0.0, 0.0),
) -> bpy.types.Object:
    bpy.ops.mesh.primitive_cone_add(
        vertices=vertices,
        radius1=radius_bottom,
        radius2=radius_top,
        depth=depth,
        location=location,
        rotation=rotation,
    )
    obj = bpy.context.object
    obj.name = name
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
    data.resolution_u = 1
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
    segments: int = 128,
) -> bpy.types.Object:
    cx, cy, cz = center
    points = [
        (
            cx + math.cos((2.0 * math.pi * i) / segments) * radius_x,
            cy,
            cz + math.sin((2.0 * math.pi * i) / segments) * radius_z,
        )
        for i in range(segments)
    ]
    return poly_curve(
        name,
        points,
        bevel_depth,
        material,
        target,
        cyclic=True,
        bevel_resolution=2,
    )


def star_mesh(
    name: str,
    location: tuple[float, float, float],
    outer_radius: float,
    inner_radius: float,
    material: bpy.types.Material,
    target: bpy.types.Collection,
    *,
    points: int = 4,
    rotation: float = math.pi / 4.0,
) -> bpy.types.Object:
    cx, cy, cz = location
    vertices = [(cx, cy, cz)]
    for i in range(points * 2):
        angle = rotation + i * math.pi / points
        radius = outer_radius if i % 2 == 0 else inner_radius
        vertices.append((cx + math.cos(angle) * radius, cy, cz + math.sin(angle) * radius))
    faces = []
    for i in range(1, points * 2 + 1):
        faces.append((0, i, 1 if i == points * 2 else i + 1))
    mesh = bpy.data.meshes.new(name)
    mesh.from_pydata(vertices, [], faces)
    mesh.update()
    obj = bpy.data.objects.new(name, mesh)
    target.objects.link(obj)
    add_material(obj, material)
    return obj


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
    direction = Vector(target_point) - obj.location
    obj.rotation_euler = direction.to_track_quat("-Z", "Y").to_euler()
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


def build_materials() -> dict[str, bpy.types.Material]:
    return {
        "frame": principled_material(
            "Seer frame / midnight metal",
            PALETTE["midnight"],
            metallic=0.68,
            roughness=0.28,
            emission_color=PALETTE["deep_violet"],
            emission_strength=0.08,
        ),
        "violet": principled_material(
            "Faceted violet crystal",
            PALETTE["violet"],
            metallic=0.22,
            roughness=0.27,
            emission_color=PALETTE["violet"],
            emission_strength=0.18,
        ),
        "soft_violet": emission_material(
            "Soft violet signal",
            PALETTE["soft_violet"],
            1.35,
        ),
        "cyan": emission_material("Cyan watch signal", PALETTE["cyan"], 1.65),
        "starlight": emission_material("Starlight aperture", PALETTE["starlight"], 2.0),
        "slate": emission_material("Muted constellation line", PALETTE["slate"], 0.28),
        "orb": principled_material(
            "Sovereign validator core",
            "#11183D",
            metallic=0.42,
            roughness=0.2,
            emission_color=PALETTE["deep_violet"],
            emission_strength=0.24,
        ),
        "background": gradient_material("Midnight observatory gradient"),
    }


def build_mark(
    prefix: str,
    origin: tuple[float, float, float],
    scale: float,
    mats: dict[str, bpy.types.Material],
    target: bpy.types.Collection,
) -> None:
    ox, oy, oz = origin

    def p(x: float, y: float, z: float) -> tuple[float, float, float]:
        return (ox + x * scale, oy + y * scale, oz + z * scale)

    # The outer diamond is both a protective aperture and the silhouette of a
    # sovereign watchtower. Thick dark bars keep it legible at 16–32 pixels;
    # inset signal rails reveal its crystal construction at larger sizes.
    top = p(0.0, 0.0, 3.0)
    right = p(2.72, 0.0, 0.12)
    bottom = p(0.0, 0.0, -2.94)
    left = p(-2.72, 0.0, 0.12)
    frame_edges = [(top, right), (right, bottom), (bottom, left), (left, top)]
    for index, (start, end) in enumerate(frame_edges):
        bar_between(
            f"{prefix} / shield rail {index + 1}",
            start,
            end,
            0.66 * scale,
            0.5 * scale,
            mats["frame"],
            target,
            bevel=0.11 * scale,
        )

    inset_edges = [
        (p(0.0, -0.37, 2.86), p(2.55, -0.37, 0.12), mats["cyan"]),
        (p(2.55, -0.37, 0.12), p(0.0, -0.37, -2.75), mats["violet"]),
        (p(0.0, -0.37, -2.75), p(-2.55, -0.37, 0.12), mats["violet"]),
        (p(-2.55, -0.37, 0.12), p(0.0, -0.37, 2.86), mats["soft_violet"]),
    ]
    for index, (start, end, material) in enumerate(inset_edges):
        bar_between(
            f"{prefix} / crystal rail {index + 1}",
            start,
            end,
            0.16 * scale,
            0.18 * scale,
            material,
            target,
            bevel=0.045 * scale,
        )

    # A tapered lower observatory tower anchors the orb without adding text or
    # a familiar chain-logo shape.
    tapered_crystal(
        f"{prefix} / observatory tower",
        p(0.0, 0.22, -1.92),
        0.62 * scale,
        0.28 * scale,
        1.86 * scale,
        mats["frame"],
        target,
        vertices=6,
    )
    cube(
        f"{prefix} / observatory sill",
        p(0.0, -0.02, -2.55),
        (1.52 * scale, 0.72 * scale, 0.31 * scale),
        mats["frame"],
        target,
        bevel=0.1 * scale,
    )

    # Faceted validator core and an intentionally simple high-contrast iris.
    ico_sphere(
        f"{prefix} / faceted validator orb",
        p(0.0, -0.52, 0.14),
        1.34 * scale,
        mats["orb"],
        target,
        subdivisions=4,
        scale=(1.0, 0.82, 1.0),
        smooth=False,
    )
    ellipse_curve(
        f"{prefix} / lens rim",
        p(0.0, -1.64, 0.14),
        1.03 * scale,
        1.03 * scale,
        0.105 * scale,
        mats["cyan"],
        target,
        segments=96,
    )
    cylinder(
        f"{prefix} / iris",
        p(0.0, -1.68, 0.14),
        0.7 * scale,
        0.1 * scale,
        mats["violet"],
        target,
        vertices=64,
        bevel=0.035 * scale,
    )
    ico_sphere(
        f"{prefix} / vertical aperture",
        p(0.0, -1.82, 0.14),
        0.37 * scale,
        mats["starlight"],
        target,
        subdivisions=2,
        scale=(0.42, 0.25, 1.68),
        smooth=False,
    )
    ico_sphere(
        f"{prefix} / aperture heart",
        p(0.0, -1.94, 0.14),
        0.12 * scale,
        mats["cyan"],
        target,
        subdivisions=2,
        smooth=False,
    )

    # Four sturdy crystal nodes reinforce the favicon silhouette. The top node
    # doubles as a restrained starlight beacon.
    for label, coordinate, size, material in (
        ("north", (0.0, -0.36, 2.93), 0.28, mats["starlight"]),
        ("east", (2.63, -0.34, 0.12), 0.24, mats["cyan"]),
        ("south", (0.0, -0.34, -2.83), 0.24, mats["violet"]),
        ("west", (-2.63, -0.34, 0.12), 0.24, mats["soft_violet"]),
    ):
        ico_sphere(
            f"{prefix} / {label} crystal node",
            p(*coordinate),
            size * scale,
            material,
            target,
            subdivisions=2,
            scale=(0.75, 0.72, 1.2 if label in {"north", "south"} else 0.88),
            smooth=False,
        )

    star_mesh(
        f"{prefix} / north beacon",
        p(0.0, -0.65, 3.35),
        0.35 * scale,
        0.075 * scale,
        mats["starlight"],
        target,
    )


def build_constellation(
    prefix: str,
    center: tuple[float, float],
    offsets: list[tuple[float, float]],
    edges: list[tuple[int, int]],
    mats: dict[str, bpy.types.Material],
    target: bpy.types.Collection,
    *,
    y: float = 1.15,
) -> tuple[float, float, float]:
    cx, cz = center
    points = [(cx + dx, y, cz + dz) for dx, dz in offsets]
    for index, (start, end) in enumerate(edges):
        poly_curve(
            f"{prefix} / link {index + 1}",
            [points[start], points[end]],
            0.018,
            mats["slate"],
            target,
            bevel_resolution=1,
        )
    node_materials = [mats["starlight"], mats["cyan"], mats["soft_violet"]]
    for index, point in enumerate(points):
        radius = 0.075 + (0.025 if index == 0 else 0.0)
        ico_sphere(
            f"{prefix} / chain node {index + 1}",
            point,
            radius,
            node_materials[index % len(node_materials)],
            target,
            subdivisions=2,
            smooth=False,
        )
    return points[0]


def build_hero(
    mats: dict[str, bpy.types.Material],
    target: bpy.types.Collection,
) -> None:
    plane_xz(
        "Hero / midnight observatory field",
        (0.0, 4.2, 0.0),
        24.5,
        10.0,
        mats["background"],
        target,
    )

    # Subtle horizon and meridian references keep the environment observational,
    # not ornamental. They remain low-energy so data overlays can retain priority.
    for z in (-2.95, -2.25, 3.05):
        poly_curve(
            f"Hero / observatory latitude {z}",
            [(-11.8, 3.0, z), (11.8, 3.0, z)],
            0.009,
            mats["slate"],
            target,
            bevel_resolution=1,
        )

    build_mark("Hero mark", (-3.9, 0.0, 0.0), 1.02, mats, target)

    # Guard rings surround the core. Broken line segments suggest active scans
    # while preserving the original diamond silhouette.
    ellipse_curve(
        "Hero / inner protection orbit",
        (-3.9, 0.85, 0.08),
        3.45,
        3.15,
        0.024,
        mats["slate"],
        target,
        segments=160,
    )
    arc_specs = [
        (3.88, 3.52, -0.62, 0.38),
        (3.88, 3.52, 0.48, 1.34),
        (4.25, 3.82, 2.45, 3.72),
        (4.25, 3.82, 4.08, 5.28),
    ]
    for index, (rx, rz, start, end) in enumerate(arc_specs):
        points = []
        for step in range(42):
            angle = start + (end - start) * step / 41.0
            points.append((-3.9 + math.cos(angle) * rx, 0.92, 0.08 + math.sin(angle) * rz))
        poly_curve(
            f"Hero / scanning arc {index + 1}",
            points,
            0.032 if index < 2 else 0.02,
            mats["cyan"] if index == 0 else mats["soft_violet"],
            target,
            bevel_resolution=1,
        )

    clusters = [
        (
            "Asterism one",
            (0.5, 2.35),
            [(0.0, 0.0), (0.72, 0.38), (1.45, 0.02), (0.95, -0.54)],
            [(0, 1), (1, 2), (1, 3), (3, 2)],
        ),
        (
            "Asterism two",
            (3.18, 1.15),
            [(0.0, 0.0), (0.62, 0.68), (1.3, 0.45), (1.05, -0.34), (1.82, -0.58)],
            [(0, 1), (1, 2), (2, 3), (3, 0), (3, 4)],
        ),
        (
            "Asterism three",
            (7.0, 2.25),
            [(0.0, 0.0), (0.85, 0.32), (1.45, -0.18), (0.62, -0.62)],
            [(0, 1), (1, 2), (0, 3), (3, 2)],
        ),
        (
            "Asterism four",
            (0.35, -1.72),
            [(0.0, 0.0), (0.8, 0.22), (1.45, -0.28), (0.65, -0.72)],
            [(0, 1), (1, 2), (1, 3), (3, 2)],
        ),
        (
            "Asterism five",
            (3.85, -2.12),
            [(0.0, 0.0), (0.68, 0.72), (1.28, 0.18), (1.88, 0.54), (2.18, -0.25)],
            [(0, 1), (1, 2), (2, 3), (2, 4)],
        ),
        (
            "Asterism six",
            (7.35, -0.92),
            [(0.0, 0.0), (0.76, 0.54), (1.45, 0.12), (1.15, -0.72), (2.0, -0.42)],
            [(0, 1), (1, 2), (2, 3), (3, 0), (3, 4)],
        ),
    ]
    anchors = []
    for name, center, offsets, edges in clusters:
        anchors.append(build_constellation(name, center, offsets, edges, mats, target))

    # Protected monitor paths converge on the validator core without implying
    # chain-to-chain protocol links or reproducing any ecosystem logo.
    core = (-2.68, 1.05, 0.12)
    for index, anchor in enumerate(anchors):
        midpoint = (
            core[0] + (anchor[0] - core[0]) * 0.48,
            1.7,
            core[2] + (anchor[2] - core[2]) * 0.48 + (0.18 if index % 2 else -0.12),
        )
        poly_curve(
            f"Hero / monitored path {index + 1}",
            [core, midpoint, anchor],
            0.011,
            mats["slate"],
            target,
            bevel_resolution=1,
        )

    # Deterministic, sparse starlight. Sizes are bounded so the hero remains a
    # dashboard background rather than a noisy space illustration.
    random.seed(SEED)
    for index in range(68):
        x = random.uniform(-11.0, 11.0)
        z = random.uniform(-3.8, 3.8)
        if ((x + 3.9) / 3.5) ** 2 + (z / 3.2) ** 2 < 1.0:
            continue
        radius = random.choice((0.012, 0.016, 0.02, 0.027))
        material = mats["slate"] if index % 4 else mats["soft_violet"]
        ico_sphere(
            f"Hero / field star {index + 1:02d}",
            (x, 2.6, z),
            radius,
            material,
            target,
            subdivisions=1,
            smooth=False,
        )


def configure_scene() -> None:
    scene = bpy.context.scene
    scene.render.engine = "BLENDER_EEVEE"
    scene.eevee.taa_render_samples = 64
    scene.eevee.taa_samples = 32
    scene.eevee.use_gtao = True
    scene.eevee.gtao_distance = 3.0
    scene.eevee.gtao_factor = 1.2
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

    scene.view_settings.view_transform = "AgX"
    scene.view_settings.look = "None"
    scene.view_settings.exposure = 0.25
    scene.view_settings.gamma = 1.0
    scene.view_settings.use_curve_mapping = False

    world = bpy.data.worlds.new("NosNode Seer world") if not scene.world else scene.world
    scene.world = world
    world.use_nodes = True
    background = world.node_tree.nodes.get("Background")
    background.inputs["Color"].default_value = rgba(PALETTE["void"])
    background.inputs["Strength"].default_value = 0.12

    scene.render.filepath = ""
    scene.frame_start = 1
    scene.frame_end = 1
    scene.frame_set(1)


def set_collection_render_state(mark: bpy.types.Collection, hero: bpy.types.Collection, *, show_mark: bool) -> None:
    mark.hide_render = not show_mark
    hero.hide_render = show_mark
    mark.hide_viewport = not show_mark
    hero.hide_viewport = show_mark


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
    # Coordinates are the orthographic front projection of the same four shield
    # rails, orb, vertical aperture, and observatory sill used in build_mark().
    svg = f'''<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 512 512" role="img" aria-labelledby="title desc">
  <title id="title">NosNode Seer mark</title>
  <desc id="desc">A faceted validator orb and vertical seer aperture protected by a diamond watchtower frame.</desc>
  <g fill="none" stroke-linecap="square" stroke-linejoin="bevel">
    <path d="M256 42 L458 246 L256 474 L54 246 Z" stroke="{PALETTE['midnight']}" stroke-width="48"/>
    <path d="M256 48 L450 246" stroke="{PALETTE['cyan']}" stroke-width="17"/>
    <path d="M450 246 L256 466 L62 246" stroke="{PALETTE['violet']}" stroke-width="17"/>
    <path d="M62 246 L256 48" stroke="{PALETTE['soft_violet']}" stroke-width="17"/>
  </g>
  <path d="M206 366 H306 L327 431 H185 Z" fill="{PALETTE['midnight']}"/>
  <circle cx="256" cy="246" r="102" fill="#11183D" stroke="{PALETTE['cyan']}" stroke-width="17"/>
  <circle cx="256" cy="246" r="61" fill="{PALETTE['violet']}"/>
  <path d="M256 165 L281 246 L256 327 L231 246 Z" fill="{PALETTE['starlight']}"/>
  <circle cx="256" cy="246" r="12" fill="{PALETTE['cyan']}"/>
  <path d="M256 17 L265 34 L282 43 L265 52 L256 69 L247 52 L230 43 L247 34 Z" fill="{PALETTE['starlight']}"/>
</svg>
'''
    path.write_text(svg, encoding="utf-8", newline="\n")


def main() -> None:
    args = parse_args()
    if tuple(bpy.app.version) != BLENDER_TARGET:
        raise RuntimeError(
            f"This source receipt targets Blender {'.'.join(map(str, BLENDER_TARGET))}; "
            f"running {bpy.app.version_string}."
        )

    output_dir = args.output_dir.resolve()
    blend_output = args.blend_output.resolve()
    output_dir.mkdir(parents=True, exist_ok=True)
    blend_output.parent.mkdir(parents=True, exist_ok=True)

    clear_scene()
    configure_scene()
    mats = build_materials()
    mark_col = collection("MARK — transparent square master")
    hero_col = collection("HERO — midnight observatory banner")
    rig_col = collection("CAMERAS + LIGHTS")

    build_mark("Square mark", (0.0, 0.0, -0.03), 1.0, mats, mark_col)
    build_hero(mats, hero_col)

    square_camera = create_camera(
        "Camera / square mark 1:1",
        (0.0, -16.5, 0.12),
        (0.0, 0.0, 0.12),
        7.8,
        rig_col,
    )
    hero_camera = create_camera(
        "Camera / observatory hero 8:3",
        (0.0, -23.0, 0.2),
        (0.0, 0.0, 0.2),
        22.8,
        rig_col,
    )
    create_area_light(
        "Key / starlight",
        (-4.5, -7.0, 6.0),
        (-1.8, 0.0, 0.0),
        PALETTE["starlight"],
        820.0,
        5.0,
        rig_col,
    )
    create_area_light(
        "Rim / cyan watch",
        (6.5, -2.0, 3.8),
        (0.0, 0.0, 0.2),
        PALETTE["cyan"],
        680.0,
        4.0,
        rig_col,
    )
    create_area_light(
        "Fill / violet observatory",
        (-6.0, 1.0, -3.5),
        (-2.0, 0.0, -0.3),
        PALETTE["violet"],
        520.0,
        5.5,
        rig_col,
    )

    # Save a useful default view: hero visible, square source retained but hidden.
    set_collection_render_state(mark_col, hero_col, show_mark=False)
    bpy.context.scene.camera = hero_camera
    bpy.ops.wm.save_as_mainfile(filepath=str(blend_output))

    write_svg(output_dir / "nosnode-seer-mark.svg")
    if not args.skip_render:
        set_collection_render_state(mark_col, hero_col, show_mark=True)
        render_master(
            output_dir / "nosnode-seer-mark-master.png",
            square_camera,
            1024,
            1024,
            transparent=True,
        )
        set_collection_render_state(mark_col, hero_col, show_mark=False)
        render_master(
            output_dir / "nosnode-seer-hero-master.png",
            hero_camera,
            1920,
            720,
            transparent=False,
        )
        bpy.context.scene.camera = hero_camera
        bpy.ops.wm.save_as_mainfile(filepath=str(blend_output))

    print(f"NOSNODE_SEER_SEED={SEED}")
    print(f"NOSNODE_SEER_BLEND={blend_output}")
    print(f"NOSNODE_SEER_OUTPUT_DIR={output_dir}")


if __name__ == "__main__":
    main()
