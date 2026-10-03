# The xbin mark: M3, b to the power x

The owner's choice (D183): **bˣ**, a white lowercase b on a cobalt tile with a yellow x
raised as its exponent. Read the name as a formula: x is the variable, bin is base two.
The monogram also spells `bx`, the command-line tool. Corners are square everywhere;
only iOS rounds anything, with its own mask. Brand rules: `plans/brand.md` §13.

These are the master files. The site uses copies: `website/img/mark.svg` (= `mark.svg`),
`website/img/wordmark.svg` (= `wordmark-a.svg`), `website/favicon.svg` (= `favicon.svg`)
and `website/apple-touch-icon.png` (180 px, from `icon-1024.svg`). Swapping in B takes
more than the file: B is 3.49:1 against A's 2.54:1, so the wordmark's sizes on every
page and in the share card change with it (`website/README.md` → "Assets" has B's, from
`lockup-b.svg`'s unit), and the site's guard fails until they do.

## Files

| File | What it is |
| --- | --- |
| `mark.svg` | The master colour mark, 20 px and up. |
| `mark-mono.svg` | One colour, `currentColor` (black when loaded as an image). |
| `icon-1024.svg` | The iOS app icon: full bleed, opaque, square; iOS applies its mask. |
| `favicon.svg` | Both hinted drawings in one file; a size query shows the 16 px one at 20 px and below. |
| `favicon-16.svg`, `favicon-32.svg` | The hinted 16 px and 32 px drawings, every edge on a whole pixel. Export PNG fallbacks at those sizes from these, not from the master. |
| `wordmark-a.svg` | Wordmark A: Bricolage Grotesque 800, exact outlines (below). The recommended wordmark. |
| `wordmark-b.svg` | Wordmark B: the extended 1970s redraw on the mark's unit. |
| `lockup-a.svg`, `lockup-b.svg` | The mark with each wordmark, clear space built into the viewBox. |

Open: the owner signs off A or B; A ships on the site until then.

## The construction

An 8-unit grid, u = 128 at 1024: margin 1u, the exponent x 2u, the b's x-height 4u, the
tile 8u, so the grid measures 1 : 2 : 4 : 8 like the brand's fields.

| Measure | Units | At 1024 | At 32 px | At 16 px |
| --- | --- | --- | --- | --- |
| Margin (b to the left and bottom edges, x to the top and right) | 1u | 128 | 4 | 2 (x: 1) |
| Exponent x, yellow | 2u | 256 | 8 | 5 (hinted up) |
| b counter | 2u | 256 | 8 | 4 |
| b x-height | 4u | 512 | 16 | 8 |
| b ascender | 6u | 768 | 24 | 12 |
| Tile, cobalt | 8u | 1024 | 32 | 16 |
| b stem / bars / wall | 1.25u / 1u / 1u | 160 / 128 / 128 | 5 / 4 / 4 | 3 / 2 / 2 |

- **The b** is white. The shoulder leaves the stem 0.5u below the x-height; the bowl's right
  edge falls at 5.25u; a small spur sits at the baseline.
- **The x** is yellow and set as an exponent is set, at half its base: 2u against the b's
  4u x-height, its top on the b's ascender line, its bottom on the b's x-height line, 1u
  from the right edge. Its notches are near-equal on all four sides so it never reads as
  an I-beam or an hourglass, and it is never boxed: a yellow cell read as a notification
  badge and suggested other brands in the cold reads.
- **Colours** are the brand fields, the same in both themes: cobalt `#1F3DFF`, white,
  yellow `#FFD000`. White on cobalt is 6.63:1, yellow on cobalt 4.50:1.

## Sizes, clear space, grounds

- Mark: at least 16 px, the 16 px drawing below 20 px. Clear space 2u on every side.
- Wordmark A: at least 16 px from ascender to baseline; clear space half the x-height.
- Lockups: the tile at least 20 px; 2u gap between tile and word; 2u clear space.
- The tile never changes between themes; only the wordmark's ink does (`#0B0C12` on
  light, `#F5F6FA` on dark, by the files' own prefers-color-scheme query). The tile
  sits on white, concrete and ink grounds as it is, with no keyline. Never put the colour
  tile on a cobalt ground: use the mono mark in white there. On photographs, only on
  quiet areas.
- Mono: the tile is solid, the b and the x are cut out of it, the counter stays solid.
  Never draw the b and x loose without the tile (a bare "bx" is the CLI's name in text),
  and never put the x in a box or on a patch of its own colour.

## Wordmark A's outlines

The mark's designers had no font tooling, so A first shipped with hand-drawn proxy
outlines. `wordmark-a.svg` and `lockup-a.svg` now carry the real Bricolage Grotesque 800
(96 pt optical size) glyphs, exported with fontTools from
`website/fonts/bricolage-grotesque-800.woff2` (decompressed with `woff2_decompress`),
with the designed ink gaps: x to b 2.7 % of the x-height, b to i 7.3 %, i to n 12 %. In
the lockup the word is scaled so its x-height (528 font units) matches the b's 600 and
its baseline sits on the b's.

## Motion

On the site, in the product and on the home screen the mark never moves. In the film:
the era stripe's fields snap in (yellow 1, green 2, magenta 4, cobalt 8), the cobalt
widens to fill the frame, a white b stands up in it (bowl, then stem), the yellow x cuts
in at the top right, hold. End card: the wordmark wipes in beside the tile, then holds.
One move per element, exponential-out easing `cubic-bezier(.16,1,.3,1)`, hard cuts; no
fade, bounce, spin, glow or loop.

## Not yet verified

The unboxed x has been rendered and checked at 16 to 48 px on white, concrete and ink,
but the cold-reader test ("a b with a raised x") has not been run again on it.
`favicon.svg`'s size switch depends on the browser evaluating media queries inside SVG
favicons.
