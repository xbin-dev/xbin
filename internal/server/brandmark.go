package server

// brandmark.go — xbin's own mark and wordmark on xbind's pages (D183): the
// bˣ tile, a white b with a yellow x raised as its exponent on a square
// cobalt tile, and wordmark A, "xbin" in Bricolage Grotesque 800 (its real
// outlines). The pages draw them inline, as the lockup /vendor/logo.svg
// draws them (TestBrandMarkIsTheMasters keeps the paths equal), for two
// reasons: some of these pages load no image at all (their CSP), and the
// word must take the page's text colour — the file's own switch follows the
// system's light or dark, the page follows the person (pagetheme.go). The
// tile keeps its colours in both themes: "the tile never changes between
// themes; only the wordmark's ink does". A workspace's own branding (D76)
// replaces all of it (brandLogo).

// The mark's units: the tile is 1024 (u = 128; margin 1u, the exponent 2u,
// the b's x-height 4u, the tile 8u).
const (
	// the b, its counter cut out (even-odd)
	markBPath = "M128 128H288V448C304 404 356 376 440 376C572 376 672 482 672 640C672 798 572 904 440 904C356 904 308 884 288 856V896H128ZM288 576C288 536 336 512 416 512C500 512 544 556 544 640C544 724 500 768 416 768C336 768 288 744 288 704Z"
	// the exponent x
	markXPath = "M640 128H728L768 189L808 128H896L812 256L896 384H808L768 323L728 384H640L724 256Z"
	// wordmark A in Bricolage's font units (baseline 0, x-height 528),
	// glyph by glyph, with the designed ink gaps
	wordPath = "M0 0 174 -264 1 -528H186L273 -334H292L379 -528H564L390 -264L565 0H381L292 -196H273L186 0Z" + " " + // x
		"M913.3 14Q863.3 14 825.8 -5Q788.3 -24 764.8 -62Q741.3 -100 733.3 -156H714.3L711.3 0H579.3V-259V-720H740.3V-545Q740.3 -521 736.8 -494.5Q733.3 -468 727.8 -439Q722.3 -410 716.3 -377H739.3Q753.3 -431 776.3 -467Q799.3 -503 833.8 -521.5Q868.3 -540 915.3 -540Q981.3 -540 1029.8 -506Q1078.3 -472 1105.3 -409.5Q1132.3 -347 1132.3 -260Q1132.3 -176 1105.8 -114.5Q1079.3 -53 1030.3 -19.5Q981.3 14 913.3 14ZM853.3 -116Q886.3 -116 911.3 -134.5Q936.3 -153 949.8 -186.5Q963.3 -220 963.3 -265Q963.3 -311 950.3 -343.5Q937.3 -376 913.8 -394Q890.3 -412 857.3 -412Q835.3 -412 816.8 -404Q798.3 -396 784.3 -382Q770.3 -368 760.3 -350.5Q750.3 -333 745.3 -313.5Q740.3 -294 740.3 -275V-254Q740.3 -233 746.3 -209Q752.3 -185 765.8 -164Q779.3 -143 800.8 -129.5Q822.3 -116 853.3 -116Z" + " " + // b
		"M1184.8 0V-528H1345.8V0ZM1265.8 -602Q1219.8 -602 1195.3 -621.5Q1170.8 -641 1170.8 -677Q1170.8 -715 1195.3 -734.5Q1219.8 -754 1265.8 -754Q1312.8 -754 1337.3 -734.5Q1361.8 -715 1361.8 -678Q1361.8 -641 1337.3 -621.5Q1312.8 -602 1265.8 -602Z" + " " + // i
		"M1425.2 0V-318V-528H1555.2L1557.2 -373H1577.2Q1590.2 -429 1614.2 -467Q1638.2 -505 1675.2 -523.5Q1712.2 -542 1762.2 -542Q1855.2 -542 1903.7 -477Q1952.2 -412 1952.2 -270V0H1790.2V-252Q1790.2 -334 1766.7 -371.5Q1743.2 -409 1697.2 -409Q1659.2 -409 1634.7 -386Q1610.2 -363 1598.2 -325Q1586.2 -287 1586.2 -240V0Z" // n
)

// theme-ok: the mark's own colours (D183), brand fields that stay the same in both themes: never a token
const markCobalt, markWhite, markYellow = "#1F3DFF", "#FFFFFF", "#FFD000"

// markTile is the tile's three shapes, each with a class the UI harness's
// theme canary knows as the mark's (its colours are no token's).
const markTile = `<rect class="m-tile" width="1024" height="1024" fill="` + markCobalt + `"/>` +
	`<path class="m-b" fill="` + markWhite + `" fill-rule="evenodd" d="` + markBPath + `"/>` +
	`<path class="m-x" fill="` + markYellow + `" d="` + markXPath + `"/>`

// brandLockup is xbin's logo on a page: the tile and the word as lockup A
// sets them, without the clear space the page's layout gives — the word 2u
// after the tile (x 1280) on the b's baseline (y 896) at the b's x-height
// (512 of the font's 528: 32/33). pageCSS sizes it by its height; the word
// is in currentColor.
const brandLockup = `<svg class="lockup" viewBox="0 0 3174 1024" role="img" aria-label="xbin"><title>xbin</title>` + markTile +
	`<path class="m-w" fill="currentColor" transform="translate(1280 896) scale(0.969697)" d="` + wordPath + `"/></svg>`

// brandWordmark is the word alone, for a workspace that set its own icon
// but no title (D76): xbin's tile never stands beside another brand's mark.
const brandWordmark = `<svg class="wordmark" viewBox="0 -754 1953 768" role="img" aria-label="xbin"><title>xbin</title>` +
	`<path fill="currentColor" d="` + wordPath + `"/></svg>`

// defaultIconURI is the favicon xbind's pages carry when the workspace set
// no icon: /vendor/favicon.svg (the hinted mark) as a data URI, since some
// of these pages allow only data: images (TestBrandMarkIsTheMasters keeps
// it the file).
const defaultIconURI = "data:image/svg+xml;base64,PHN2ZyB4bWxucz0iaHR0cDovL3d3dy53My5vcmcvMjAwMC9zdmciIHZpZXdCb3g9IjAgMCAzMiAzMiIgc2hhcGUtcmVuZGVyaW5nPSJjcmlzcEVkZ2VzIj4KICA8dGl0bGU+eGJpbjwvdGl0bGU+CiAgPCEtLSBGYXZpY29uLiBCb3RoIGhpbnRlZCBkcmF3aW5ncyBpbiBvbmUgZmlsZSwgb24gYSAzMi11bml0IGdyaWQ6CiAgICAgICAubCBpcyB0aGUgMzIgcHggZHJhd2luZzsgLnMgaXMgdGhlIDE2IHB4IGRyYXdpbmcgYXQgMnguCiAgICAgICBBdCAyMCBweCBhbmQgYmVsb3cgdGhlIDE2IHB4IGRyYXdpbmcgc2hvd3MuIFdoZXJlIHNpemUgbWVkaWEgcXVlcmllcyBhcmUgaWdub3JlZCwKICAgICAgIHRoZSAzMiBweCBkcmF3aW5nIHNob3dzIGF0IGV2ZXJ5IHNpemUuIC0tPgogIDxzdHlsZT4uc3tkaXNwbGF5Om5vbmV9QG1lZGlhIChtYXgtd2lkdGg6MjBweCl7Lmx7ZGlzcGxheTpub25lfS5ze2Rpc3BsYXk6aW5saW5lfX08L3N0eWxlPgogIDxyZWN0IHdpZHRoPSIzMiIgaGVpZ2h0PSIzMiIgZmlsbD0iIzFGM0RGRiIvPgogIDxnIGNsYXNzPSJsIj4KICAgIDxwYXRoIGZpbGw9IiNGRkZGRkYiIGZpbGwtcnVsZT0iZXZlbm9kZCIgZD0iTTQgNEg5VjEySDE3VjEzSDE4VjE0SDE5VjE1SDIwVjE3SDIxVjIzSDIwVjI1SDE5VjI2SDE4VjI3SDE3VjI4SDRaTTEwIDE2SDE1VjE3SDE2VjE4SDE3VjIySDE2VjIzSDE1VjI0SDEwVjIzSDlWMTdIMTBaIi8+CiAgICA8cGF0aCBmaWxsPSIjRkZEMDAwIiBkPSJNMjAgNEgyMlY1SDIzVjZIMjVWNUgyNlY0SDI4VjVIMjdWNkgyNlY3SDI1VjlIMjZWMTBIMjdWMTFIMjhWMTJIMjZWMTFIMjVWMTBIMjNWMTFIMjJWMTJIMjBWMTFIMjFWMTBIMjJWOUgyM1Y3SDIyVjZIMjFWNUgyMFoiLz4KICA8L2c+CiAgPGcgY2xhc3M9InMiPgogICAgPHBhdGggZmlsbD0iI0ZGRkZGRiIgZmlsbC1ydWxlPSJldmVub2RkIiBkPSJNNCA0SDEwVjEySDE4VjE0SDIwVjE2SDIyVjI0SDIwVjI2SDE4VjI4SDRaTTEwIDE2SDE4VjI0SDEwWiIvPgogICAgPHBhdGggZmlsbD0iI0ZGRDAwMCIgZD0iTTIwIDJIMjJWNEgyMFpNMjggMkgzMFY0SDI4Wk0yMiA0SDI0VjZIMjJaTTI2IDRIMjhWNkgyNlpNMjQgNkgyNlY4SDI0Wk0yMiA4SDI0VjEwSDIyWk0yNiA4SDI4VjEwSDI2Wk0yMCAxMEgyMlYxMkgyMFpNMjggMTBIMzBWMTJIMjhaIi8+CiAgPC9nPgo8L3N2Zz4K"
