"""Generate VoltDrive PWA icons: orange gradient rounded square + white bolt."""
from PIL import Image, ImageDraw
import os

OUT = os.path.dirname(os.path.abspath(__file__))


def gradient(size, c1, c2):
    """Vertical-ish diagonal gradient."""
    img = Image.new("RGB", (size, size), c1)
    top = Image.new("RGB", (size, size), c2)
    mask = Image.new("L", (size, size))
    md = mask.load()
    for y in range(size):
        for x in range(size):
            md[x, y] = int(255 * ((x + y) / (2 * size)))
    return Image.composite(top, img, mask)


def rounded_mask(size, radius):
    m = Image.new("L", (size, size), 0)
    d = ImageDraw.Draw(m)
    d.rounded_rectangle([0, 0, size, size], radius=radius, fill=255)
    return m


def bolt(draw, size, color):
    s = size
    pts = [
        (0.56, 0.16), (0.34, 0.55), (0.48, 0.55),
        (0.44, 0.84), (0.66, 0.45), (0.52, 0.45),
    ]
    draw.polygon([(x * s, y * s) for x, y in pts], fill=color)


def make(size, radius_ratio=0.22, maskable=False):
    base = gradient(size, (255, 77, 0), (255, 138, 43))  # #FF4D00 -> #FF8A2B
    if maskable:
        # Full-bleed background for maskable (safe zone), no rounding.
        img = base.convert("RGBA")
    else:
        mask = rounded_mask(size, int(size * radius_ratio))
        img = Image.new("RGBA", (size, size), (0, 0, 0, 0))
        img.paste(base, (0, 0), mask)
    d = ImageDraw.Draw(img)
    scale = 0.78 if maskable else 1.0  # shrink bolt inside maskable safe zone
    if maskable:
        off = size * (1 - scale) / 2
        tmp = Image.new("RGBA", (size, size), (0, 0, 0, 0))
        td = ImageDraw.Draw(tmp)
        bolt(td, size, (255, 255, 255, 255))
        tmp = tmp.resize((int(size * scale), int(size * scale)))
        img.alpha_composite(tmp, (int(off), int(off)))
    else:
        bolt(d, size, (255, 255, 255, 255))
    return img


make(192).save(os.path.join(OUT, "icon-192.png"))
make(512).save(os.path.join(OUT, "icon-512.png"))
make(512, maskable=True).save(os.path.join(OUT, "icon-maskable.png"))
make(180).save(os.path.join(OUT, "apple-touch-icon.png"))
# Favicon
make(64).save(os.path.join(OUT, "favicon.png"))
print("icons yaratildi:", os.listdir(OUT))
