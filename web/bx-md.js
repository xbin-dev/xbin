/**
 * bx-md.js — the hardened markdown-to-HTML renderer for UNTRUSTED text, with
 * its code highlighter loaded up front: what the Agent tab and a screen's
 * text blocks import, so a fenced block is coloured in the first HTML md()
 * returns (a caller here never has to draw again when a highlighter
 * arrives).
 *
 * The renderer itself is /vendor/chat/md.js (D224) — one implementation,
 * one marked instance of its own, one policy: raw HTML escaped, images as
 * their text, links on safe schemes in a new tab, a closed fence in a
 * language highlight.js knows coloured. That module loads the highlighter
 * only when a fence asks for it; this one hands it over before anything is
 * rendered. Both URLs are the same module state in a document.
 */
import hljs from '/vendor/highlight.min.js';
import { useHighlighter } from '/vendor/chat/md.js';

useHighlighter(hljs);

export { md, mdCssText, mdInto, closedFence, mdEpoch, onHighlighter } from '/vendor/chat/md.js';
