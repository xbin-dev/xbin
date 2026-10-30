// hack/ui-harness/passes-more.js — the passes registered past shots.js's
// size budget (hack/size-budget.txt): one line each, so parallel packs'
// registrations merge. shots.js calls register(PASSES) after its own.
'use strict';

function register(P) {
  P.demoStills = require('../demo/stills').demoStills; // the demo film set's stills (HARNESS_SEED=demo; hack/demo/README.md)
  P.appearance = require('./passes/appearance').appearance; // D184: no flash, the relay, a second tab, the hint cookie, density, fonts
  P.themeCanary = require('./passes/themecanary').themeCanary; // D184: every colour, font and radius on screen from a token, in both themes
  P.docMode = require('./passes/docmode').docMode; // D187: Document mode — rows, growing tiles, the top bar, a phone, back to Canvas
  P.fixedGrid = require('./passes/fixedgrid').fixedGrid; // D193: Fixed width — 48 stretched columns, the right edge, back to Canvas
  P.termTheme = require('./passes/termtheme').termTheme; // D184: an open terminal follows the person's theme; a picked palette stays
  P.themeCompat = require('./passes/themecompat').themeCompat; // D184: a tile from before the theme — its own palette carries, its own buttons keep their colours
  P.buildState = require('./passes/buildstate').buildState; P.tilePrompts = require('./passes/tileprompts').tilePrompts; // D196 build phases; D195 prompts on the tile
  P.grantDismiss = require('./passes/grantdismiss').grantDismiss; // D188: dismiss grant requests and interfaces to bind; no "N grants active"
  P.deviceTheme = require('./passes/devicetheme').deviceTheme; // D188: this browser's Light or Dark over the person's theme
  P.focusAnchor = require('./passes/focusanchor').focusAnchor; // D191: the focused tile holds its place — Document mode, the last tile, a phone's canvas
  P.blocks = require('./passes/blocks').blocks; // D192: headings and text on a screen, no tile — canvas, Document mode, an org screen's draft
  P.richCopy = require('./passes/richcopy').richCopy; // D199/#26: copy with formatting — a right-click, xbin.copy, the agent's Copy; a coloured fence
  P.tiled = require('./passes/tiled').tiled; // D193: Tiled — one tile fills the screen, splits, dividers, swaps, a phone, an org screen's draft
  P.ideMove = require('./passes/idemove').ideMove; // D202: a session's element survives moves between panes and in the DOM; the dock's logs mount once; narrow folds
  P.agentDocs = require('./passes/agentdocs').agentDocs; // D201: the agent reads a PDF, writes a workbook, a deck and a document — previews, downloads, no sandbox
  P.codePanels = require('./passes/codepanels').codePanels; // D202: the tile window's code and proposal panels, standalone (code-tree, code-view, pr-panels)
  P.tileIde = require('./passes/tileide').tileIde; // D202: the tile window as an IDE — tools, panes, drags, dividers, the ✕, a reload, an older pref, the phone sheet
  P.limitsCaps = require('./passes/limitscaps').limitsCaps; // D203 end to end: a tile's limit asks and cap hits — the popover, an org's ceiling, the banner's raises, the admin tab, ⚙ → Limits, /xbin/limits
  P.apptunnel = require('./passes/apptunnel').apptunnel; // D204: the admin console's iroh tunneling — off, n0, a custom relay's tokens, refuse outside the tunnel, failed, a person's device limit (mocked API)
  P.pairing = require('./passes/pairing').pairing; // D204: adding a phone over the tunnel — the big QR code, the confirmation code, They don't, replacing at the limit, /xbin/devices (mocked API)
  P.agentReview = require('./passes/agentreview').agentReview; // D210: a turn's changes a file at a time — folded, the list, inline or the full-diff route, 400 lines then Show all
}

module.exports = { register };
