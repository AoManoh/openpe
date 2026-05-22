#!/usr/bin/env python3
from pathlib import Path

try:
    from PIL import Image, ImageDraw, ImageFilter
except ImportError as exc:
    raise SystemExit("缺少 Pillow。请先安装 pillow，或直接保留已生成的 media/openpe-icon.png。") from exc


ROOT = Path(__file__).resolve().parents[1]
OUTPUT = ROOT / "media" / "openpe-icon.png"
SIZE = 128
SCALE = 4


def rgba(hex_color: str, alpha: int = 255) -> tuple[int, int, int, int]:
    hex_color = hex_color.lstrip("#")
    return (
        int(hex_color[0:2], 16),
        int(hex_color[2:4], 16),
        int(hex_color[4:6], 16),
        alpha,
    )


def scaled(points):
    return [(round(x * SCALE), round(y * SCALE)) for x, y in points]


def rounded(draw: ImageDraw.ImageDraw, xy, radius, fill, outline=None, width=1):
    xy = tuple(round(v * SCALE) for v in xy)
    draw.rounded_rectangle(
        xy,
        radius=round(radius * SCALE),
        fill=fill,
        outline=outline,
        width=round(width * SCALE),
    )


def line(draw: ImageDraw.ImageDraw, xy, fill, width):
    draw.line(scaled(xy), fill=fill, width=round(width * SCALE), joint="curve")


def main():
    output = Image.new("RGBA", (SIZE * SCALE, SIZE * SCALE), rgba("#061317"))
    draw = ImageDraw.Draw(output)

    # 深色圆角底板使用竖向渐变，避免插件列表里显得扁平。
    for y in range(SIZE * SCALE):
        t = y / (SIZE * SCALE - 1)
        r = round(0x12 * (1 - t) + 0x06 * t)
        g = round(0x34 * (1 - t) + 0x13 * t)
        b = round(0x3B * (1 - t) + 0x17 * t)
        draw.line([(0, y), (SIZE * SCALE, y)], fill=(r, g, b, 255))

    mask = Image.new("L", output.size, 0)
    mask_draw = ImageDraw.Draw(mask)
    rounded(mask_draw, (0, 0, SIZE, SIZE), 28, 255)
    transparent = Image.new("RGBA", output.size, (0, 0, 0, 0))
    output = Image.composite(output, transparent, mask)
    draw = ImageDraw.Draw(output)

    shadow = Image.new("RGBA", output.size, (0, 0, 0, 0))
    shadow_draw = ImageDraw.Draw(shadow)
    rounded(shadow_draw, (23, 32, 105, 95), 16, rgba("#000000", 110))
    shadow = shadow.filter(ImageFilter.GaussianBlur(7 * SCALE))
    output.alpha_composite(shadow, (0, 6 * SCALE))
    draw = ImageDraw.Draw(output)

    rounded(draw, (24, 27, 105, 94), 16, rgba("#0B2228"))
    bubble = scaled([(32, 84), (32, 43), (32, 38), (37, 34), (88, 34), (97, 43), (97, 74), (88, 84), (56, 84), (38, 100), (38, 84)])
    draw.polygon(bubble, fill=rgba("#D7FFF3"))
    rounded(draw, (32, 34, 97, 84), 10, rgba("#D7FFF3"))

    dark = rgba("#0F2D33")
    line(draw, [(45, 52), (70, 52)], dark, 7)
    line(draw, [(45, 67), (62, 67)], dark, 7)
    line(draw, [(59, 83), (73, 83)], dark, 7)
    line(draw, [(75, 64), (84, 73), (75, 83)], dark, 7)

    draw.polygon(
        scaled([(95, 22), (100, 34), (111, 39), (100, 44), (95, 56), (90, 44), (79, 39), (90, 34)]),
        fill=rgba("#2DD4BF"),
    )
    draw.polygon(
        scaled([(105, 72), (108, 79), (115, 82), (108, 85), (105, 92), (102, 85), (95, 82), (102, 79)]),
        fill=rgba("#F59E0B"),
    )

    resample = getattr(getattr(Image, "Resampling", Image), "LANCZOS")
    output = output.resize((SIZE, SIZE), resample)
    OUTPUT.parent.mkdir(parents=True, exist_ok=True)
    output.save(OUTPUT)
    print(f"Rendered {OUTPUT}")


if __name__ == "__main__":
    main()
