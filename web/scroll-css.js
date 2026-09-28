/**
 * scroll-css.js — the thin themed scrollbars (D123) as one shared lit
 * CSSResult, so every shadow root adopts the same sheet:
 * `static styles = [scrollCss, css`…`]`. Importing it also installs the
 * focused-scroll tracker in this document (/vendor/bx-scroll.js, which holds
 * the CSS text and stays lit-free for tile documents).
 */
import { unsafeCSS } from 'lit';
import { scrollCssText } from '/vendor/bx-scroll.js';

export const scrollCss = unsafeCSS(scrollCssText);
