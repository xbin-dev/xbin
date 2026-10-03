#!/usr/bin/env python3
# hack/website-images.py — the site's photographs at web sizes, made from the masters in
# website/art/ (`make website-images`; website/README.md → "Assets"). Run it again after
# a master changes, and commit what it writes.
#
#   python3 hack/website-images.py
#
# It writes website/img/. Each photograph comes as AVIF (what browsers pick) plus JPEG
# (the fallback every browser reads), at the 1× and 2× widths the layout needs:
#
# - hall-{day,night}-{880,1536}: the home hero's photo column on desktop. The master's
#   full height is cut square at 70 % across, so the column's `object-position: 70% 62%`
#   keeps the art direction's framing. The column is at most 880 px tall, so 880 is the
#   1× file. 2× would be 1760, past the masters' 1536 px, so 1536 is the 2× file.
# - hall-{day,night}-strip-{900,1800}: the full frame for the phone strip, which is
#   100vw wide up to 900 px and 112 px tall, framed by object-position in site.css.
# - film/F-1-{day,night}-poster.webp: the 12 s film's poster frames. These are
#   placeholders until the film is shot (website/shots.todo.md): 1600 × 1000, flat, in
#   the product theme's shell background (plans/brand.md §15). They never ship, because
#   the film's sections ship without the film until it exists.
# - shots/<ID>-<light|dark>-{800,1600}.{avif,webp}: every product shot whose master is in
#   art/shots/ (<ID>-light.webp and <ID>-dark.webp, 3200 × 2000), at a half and a
#   quarter of the capture, for a shot beside copy (about 740 px wide at most). AVIF
#   keeps full colour resolution (4:4:4) so coloured terminal text stays sharp; WebP is
#   the fallback.
#
# hack/check-website.mjs holds every page's first-screen images to 250 KB, counting a
# <picture> by its largest candidate. This script refuses to write a hero file over
# HERO_MAX, which leaves room for the header's and footer's marks.
#
# Needs Pillow with AVIF (11.3 or later). It encodes single-threaded, so the bytes do not
# depend on the machine's core count.
import os
import sys

from PIL import Image, features

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), '..'))
ART = os.path.join(ROOT, 'website', 'art')
IMG = os.path.join(ROOT, 'website', 'img')

HERO_MAX = 240_000            # bytes, for any one file the hero's <picture> can load
CROP_ACROSS = 0.70            # where the hero's square is cut, as object-position
SQUARE = (880, 1536)          # the hero column: 1× and 2× (2× capped by the masters)
STRIP = (900, 1800)           # the phone strip: 1× and 2×
AVIF = dict(quality=62, speed=4, max_threads=1)
JPEG = dict(quality=78, optimize=True, progressive=True)
SHOT_WIDTHS = (800, 1600)      # a shot beside copy: 1× and 2× of about 740 px
SHOT_AVIF = dict(quality=80, speed=4, max_threads=1, subsampling='4:4:4')
SHOT_WEBP = dict(quality=90, method=6)

# The film's poster placeholders: the product theme's shell background (plans/brand.md
# §15, Concrete Day and Concrete Night).
POSTERS = {'F-1-day-poster.webp': (0xE8, 0xE9, 0xEE), 'F-1-night-poster.webp': (0x0B, 0x0C, 0x12)}
FILM = (1600, 1000)


def square(im, side):
    w, h = im.size
    left = round((w - h) * CROP_ACROSS)
    cut = im.crop((left, 0, left + h, h))
    return cut if side == h else cut.resize((side, side), Image.LANCZOS)


def frame(im, width):
    w, h = im.size
    return im.resize((width, round(h * width / w)), Image.LANCZOS)


def save(im, name):
    rows = []
    for ext, fmt, opts in (('avif', 'AVIF', AVIF), ('jpg', 'JPEG', JPEG)):
        path = os.path.join(IMG, f'{name}.{ext}')
        im.save(path, fmt, **opts)
        size = os.path.getsize(path)
        if size > HERO_MAX:
            sys.exit(f'website-images: {os.path.relpath(path, ROOT)} is {size} bytes, over {HERO_MAX} (the first-screen budget)')
        rows.append(f'{ext} {size / 1000:.1f} KB')
    print(f'  img/{name}  {im.size[0]} × {im.size[1]}  ' + ', '.join(rows))


def main():
    if not features.check('avif'):
        sys.exit('website-images: this Pillow has no AVIF support (Pillow 11.3 or later)')
    os.makedirs(os.path.join(IMG, 'film'), exist_ok=True)
    for light in ('day', 'night'):
        master = os.path.join(ART, f'hall-{light}.jpg')
        im = Image.open(master).convert('RGB')
        if im.size[1] < SQUARE[1]:
            sys.exit(f'website-images: {os.path.relpath(master, ROOT)} is {im.size[0]} × {im.size[1]}, shorter than the 2× square ({SQUARE[1]})')
        print(f'{os.path.relpath(master, ROOT)} ({im.size[0]} × {im.size[1]})')
        for side in SQUARE:
            save(square(im, side), f'hall-{light}-{side}')
        for width in STRIP:
            save(frame(im, width), f'hall-{light}-strip-{width}')
    for name, rgb in POSTERS.items():
        path = os.path.join(IMG, 'film', name)
        Image.new('RGB', FILM, rgb).save(path, 'WEBP', lossless=True)
        print(f'  img/film/{name}  {FILM[0]} × {FILM[1]}  placeholder, {os.path.getsize(path)} bytes')
    shots = os.path.join(ART, 'shots')
    os.makedirs(os.path.join(IMG, 'shots'), exist_ok=True)
    for master in sorted(os.listdir(shots)) if os.path.isdir(shots) else []:
        if not master.endswith('.webp'):
            continue
        im = Image.open(os.path.join(shots, master)).convert('RGB')
        print(f'website/art/shots/{master} ({im.size[0]} × {im.size[1]})')
        for width in SHOT_WIDTHS:
            out = frame(im, width)
            rows = []
            for ext, fmt, opts in (('avif', 'AVIF', SHOT_AVIF), ('webp', 'WEBP', SHOT_WEBP)):
                path = os.path.join(IMG, 'shots', f'{master[:-5]}-{width}.{ext}')
                out.save(path, fmt, **opts)
                rows.append(f'{ext} {os.path.getsize(path) / 1000:.1f} KB')
            print(f'  img/shots/{master[:-5]}-{width}  {out.size[0]} × {out.size[1]}  ' + ', '.join(rows))


if __name__ == '__main__':
    main()
