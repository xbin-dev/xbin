#!/usr/bin/env python3
# hack/demo/readme-shots.py — the demo set's stills as the repo's pictures:
#
#   python3 hack/demo/readme-shots.py readme STILLS_DIR
#       the README's four (website/shots/readme-*.webp) from the demoStills
#       pass's PNGs (hack/demo/stills.js: 1440 × 900 at 2×, dark): each
#       scaled to 1600 × 1000 and written as WebP
#
#   python3 hack/demo/readme-shots.py site ID PNG THEME
#       a website shot's master (website/art/shots/<ID>-<THEME>.webp, 3200 ×
#       2000 at quality 90, hack/website-images.py's input) from one still
#       (hack/demo/cam/shot.js --size 1600x1000 --dpr 2, or a 2880 × 1800
#       desk still, scaled up to the master's size); then `make
#       website-images` writes the web sizes
#
# Every file it writes stays under the repo's 1 MiB limit
# (hack/check-large-files.sh); it refuses to write one over it.
import os
import sys

from PIL import Image

ROOT = os.path.normpath(os.path.join(os.path.dirname(os.path.abspath(__file__)), '..', '..'))
LIMIT = 1 << 20

# the README's pictures: the still each is made from (hack/demo/stills.js names)
README = {
    'readme-agent-and-calendar.webp': '01-person-today-agent.png',
    'readme-app-history.webp': '08-admin-agent-built-app.png',
    'readme-tiled.webp': '07b-admin-ops-tiled.png',
    'readme-per-person.webp': '05-person-expenses.png',
}
README_SIZE = (1600, 1000)
README_WEBP = dict(quality=82, method=6)
MASTER_SIZE = (3200, 2000)
MASTER_WEBP = dict(quality=90, method=6)


def write(im, path, opts):
    im.save(path, 'WEBP', **opts)
    size = os.path.getsize(path)
    if size > LIMIT:
        os.remove(path)
        sys.exit(f'readme-shots: {os.path.relpath(path, ROOT)} would be {size} bytes, over the 1 MiB limit')
    print(f'  {os.path.relpath(path, ROOT)}  {im.size[0]} × {im.size[1]}  {size / 1024:.0f} KiB')


def scaled(src, size):
    im = Image.open(src).convert('RGB')
    if im.size[0] * size[1] != im.size[1] * size[0]:
        sys.exit(f'readme-shots: {src} is {im.size[0]} × {im.size[1]}, not the {size[0]}:{size[1]} frame')
    return im if im.size == size else im.resize(size, Image.LANCZOS)


def readme(stills):
    out = os.path.join(ROOT, 'website', 'shots')
    for name, still in README.items():
        src = os.path.join(stills, still)
        if not os.path.exists(src):
            sys.exit(f'readme-shots: no {still} in {stills} (run the demoStills pass: hack/demo/README.md)')
        write(scaled(src, README_SIZE), os.path.join(out, name), README_WEBP)


def site(shot_id, png, theme):
    if theme not in ('light', 'dark'):
        sys.exit('readme-shots: THEME is light or dark')
    out = os.path.join(ROOT, 'website', 'art', 'shots', f'{shot_id}-{theme}.webp')
    write(scaled(png, MASTER_SIZE), out, MASTER_WEBP)


def main(argv):
    if len(argv) == 2 and argv[0] == 'readme':
        readme(argv[1])
    elif len(argv) == 4 and argv[0] == 'site':
        site(argv[1], argv[2], argv[3])
    else:
        sys.exit(__doc__ if __doc__ else 'usage: readme-shots.py readme STILLS | site ID PNG THEME')


if __name__ == '__main__':
    main(sys.argv[1:])
