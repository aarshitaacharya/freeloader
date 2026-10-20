#!/usr/bin/env python3
"""Turns captured frames into a captioned GIF.

    assemble.py FRAMES_JSON OUT.gif [--width 960]

FRAMES_JSON is a list of {"file": png | "files": [png, ...] (stacked), "caption": str, "ms": int}, or a terminal frame
{"terminal": [lines], "caption": str, "ms": int} that is drawn here from real captured output.
Needs Pillow. See README.md in this directory for the capture scripts.
"""
import argparse
import json
import pathlib

from PIL import Image, ImageDraw, ImageFont

SANS = ["/System/Library/Fonts/Helvetica.ttc", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"]
MONO = ["/System/Library/Fonts/Menlo.ttc", "/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf"]


def font(paths, size):
    for p in paths:
        if pathlib.Path(p).exists():
            return ImageFont.truetype(p, size)
    return ImageFont.load_default(size)


def terminal(lines, size):
    img = Image.new("RGB", size, "#1a1a19")
    d = ImageDraw.Draw(img)
    f = font(MONO, 17)
    y = 70
    d.text((40, 30), "terminal", font=font(SANS, 14), fill="#898781")
    for line in lines:
        color = "#0ca30c" if line.startswith("HTTP 200") else "#f0efec"
        if line.startswith("$"):
            color = "#6da7ec"
        # wrap long lines at ~78 chars
        while len(line) > 78:
            d.text((40, y), line[:78], font=f, fill=color)
            line, y = "  " + line[78:], y + 28
        d.text((40, y), line, font=f, fill=color)
        y += 34
    return img


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("frames")
    ap.add_argument("out")
    ap.add_argument("--width", type=int, default=960)
    a = ap.parse_args()
    spec = json.loads(pathlib.Path(a.frames).read_text())
    cap_font = font(SANS, 21)

    def resolve(name):
        path = pathlib.Path(name)
        if not path.exists():  # paths in the manifest are relative to where it was captured
            path = pathlib.Path(a.frames).resolve().parent / path.name
        return path

    images, durations = [], []
    base = None
    for fr in spec:
        if "terminal" in fr:
            img = terminal(fr["terminal"], base.size if base else (1100, 700))
        else:
            parts = [Image.open(resolve(f)).convert("RGB") for f in (fr.get("files") or [fr["file"]])]
            img = parts[0]
            if len(parts) > 1:  # stacked vertically, e.g. two regions of a tall dashboard
                img = Image.new("RGB", (parts[0].width, sum(q.height for q in parts)))
                y = 0
                for q in parts:
                    img.paste(q, (0, y))
                    y += q.height
            base = base or img
        w, h = img.size
        bar = 46
        canvas = Image.new("RGB", (w, h + bar), "#0d0d0d")
        canvas.paste(img, (0, 0))
        d = ImageDraw.Draw(canvas)
        d.text((20, h + 11), fr["caption"], font=cap_font, fill="#f0efec")
        scale = a.width / w
        canvas = canvas.resize((a.width, round((h + bar) * scale)), Image.LANCZOS)
        images.append(canvas.convert("P", palette=Image.ADAPTIVE, colors=128, dither=Image.NONE))
        durations.append(fr["ms"])

    images[0].save(a.out, save_all=True, append_images=images[1:], duration=durations, loop=0, optimize=True, disposal=2)
    print(a.out, f"{pathlib.Path(a.out).stat().st_size / 1e6:.2f} MB", len(images), "frames")


if __name__ == "__main__":
    main()
