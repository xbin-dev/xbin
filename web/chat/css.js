/**
 * chat/css.js — the chat kit's one stylesheet, as text (/vendor/chat/css.js).
 *
 * Everything the kit's views draw (/vendor/chat/views.js, cards.js, asks.js,
 * composer.js) is styled here, under class names that all start with `bxc-`
 * so the text can sit in a host's shadow root (lit: unsafeCSS(chatCssText))
 * or in a document's <style> beside a page's own rules without meeting
 * them. Colours, type, radii and sizes are the theme's tokens
 * (/vendor/theme.css, D184) with Night's values as fallbacks, so a document
 * without theme.css still reads.
 *
 * What a host adds itself: the scroller the rows sit in (the kit's rows are
 * block elements with a bottom margin; the scroller needs
 * `overflow-anchor: none` — /vendor/chat/window.js sets it), and its own
 * chrome around them. What a host may tune, as custom properties on any
 * ancestor: --bxc-gap (the space under a row, default 12px).
 *
 * Classes inside a rendered diff (fh, h, d, a, ctx, mk, hljs-*) are
 * /vendor/bx-code.js diffHTML's and /vendor/chat/tools.js patchLines';
 * classes inside rendered markdown are /vendor/chat/md.js's — both are
 * styled here under .bxc-diff and .bxc-md. Pure text: no lit, no DOM.
 */
import { mdCssText } from './md.js';

const F = {
  body: 'var(--bx-font-body, 400 14px/20px "Instrument Sans", system-ui, sans-serif)',
  ui: 'var(--bx-font, 13px/18px "Instrument Sans", system-ui, sans-serif)',
  meta: 'var(--bx-font-meta, 400 12px/16px "Instrument Sans", system-ui, sans-serif)',
  micro: 'var(--bx-font-micro, 600 11px/14px "Instrument Sans", system-ui, sans-serif)',
  code: 'var(--bx-font-code, 400 12px/18px "JetBrains Mono", ui-monospace, monospace)',
};

export const chatCssText = `
  .bxc-muted { color: var(--bx-muted, #A3A6B6); font: ${F.meta}; }
  .bxc-mono { font-family: var(--bx-mono, "JetBrains Mono", ui-monospace, monospace); }
  .bxc-md > :first-child { margin-top: 0; } .bxc-md > :last-child { margin-bottom: 0; }
  .bxc-md .md-b { display: contents; }
  .bxc-md > .md-b:first-child > :first-child { margin-top: 0; } .bxc-md > .md-b:last-child > :last-child { margin-bottom: 0; }
  ${mdCssText('.bxc-md')}

  /* ---- messages ---- */
  .bxc-msg { margin: 0 0 var(--bxc-gap, 12px); word-break: break-word; }
  .bxc-who { font: ${F.meta}; font-weight: 600; color: var(--bx-muted, #A3A6B6); margin-bottom: 2px; }
  .bxc-bubble { font: ${F.body}; }
  /* the reader's own words sit on their side; another person's on the other, under their name */
  .bxc-user { display: flex; flex-direction: column; align-items: flex-end; }
  .bxc-user.bxc-other { align-items: flex-start; }
  .bxc-user .bxc-bubble { background: var(--bx-panel-2, #262730); border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
    padding: 8px 12px; max-width: 88%; box-sizing: border-box; }
  .bxc-user .bxc-text { white-space: pre-wrap; }
  .bxc-att { display: flex; flex-wrap: wrap; gap: 4px; }
  .bxc-att.bxc-below { margin-top: 4px; }
  .bxc-att-file { display: inline-flex; align-items: center; gap: 6px; max-width: 100%; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
    padding: 0 6px; font: ${F.meta}; background: var(--bx-panel, #1F2028); }
  .bxc-att-file img { width: 20px; height: 20px; object-fit: cover; }
  .bxc-att-file .bxc-mono { overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bxc-att-file .bxc-sz { color: var(--bx-muted, #A3A6B6); flex: none; }
  button.bxc-att-file { color: inherit; cursor: pointer; }
  button.bxc-att-file:hover { border-color: var(--bx-border-strong, #666A7E); }
  .bxc-att-file.bxc-gone { color: var(--bx-muted, #A3A6B6); text-decoration: line-through; }
  .bxc-steer { display: block; margin-top: 4px; color: var(--bx-muted, #A3A6B6); font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); }
  /* an answer's own actions (Copy) show with the pointer, or the keyboard */
  .bxc-msg.bxc-agent { position: relative; }
  .bxc-acts { position: absolute; top: -4px; right: 0; display: none; }
  .bxc-msg.bxc-agent:hover > .bxc-acts, .bxc-acts:focus-within { display: flex; }
  .bxc-act { display: inline-flex; padding: 4px; cursor: pointer; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-muted, #A3A6B6); }
  .bxc-act:hover { color: var(--bx-text, #E9EAF0); }
  /* the answer being written: a still block at its end, no blink */
  .bxc-cursor::after { content: ''; display: inline-block; width: 6px; height: 13px; background: var(--bx-accent, #8C9BFF); vertical-align: text-bottom; }
  .bxc-thought { color: var(--bx-muted, #A3A6B6); }
  .bxc-thought > summary { list-style: none; cursor: pointer; user-select: none; display: inline-flex; align-items: center; gap: 4px; }
  .bxc-thought > summary::-webkit-details-marker { display: none; }
  .bxc-thought:not([open]) > summary .bxc-cd, .bxc-thought[open] > summary .bxc-cr { display: none; }
  .bxc-thought .bxc-md { font-style: italic; border-left: 2px solid var(--bx-border, #33353F); padding: 2px 0 2px 12px; margin-top: 4px; max-height: 320px; overflow: auto; }
  /* progress is a static label (nothing loops) */
  .bxc-live { color: var(--bx-muted, #A3A6B6); }
  .bxc-activity { margin: 2px 0 8px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bxc-plan { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 0 0 8px; }
  .bxc-plan .bxc-h { font: ${F.micro}; letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; color: var(--bx-muted, #A3A6B6); margin-bottom: 4px; }
  .bxc-plan ul { margin: 0; padding: 0; }
  .bxc-plan li { list-style: none; margin: 2px 0; display: flex; gap: 6px; align-items: baseline; }
  .bxc-plan li bx-icon, .bxc-plan li .bxc-todo { flex: none; align-self: center; }
  .bxc-plan li .bxc-todo { width: 8px; height: 8px; margin: 0 4px; box-sizing: border-box; border: 1px solid var(--bx-border-strong, #666A7E); }
  .bxc-plan .bxc-done { color: var(--bx-ok, #A3CF5E); }
  .bxc-plan .bxc-done > :not(.bxc-ptag) { text-decoration: line-through; }
  .bxc-plan .bxc-h { display: flex; gap: 8px; align-items: baseline; }
  .bxc-plan .bxc-h > span:first-child { flex: 1; }
  .bxc-plan .bxc-pmeta { font: ${F.meta}; letter-spacing: normal; text-transform: none; }
  .bxc-plan ol { margin: 0; padding: 0; }
  .bxc-plan .bxc-pn { flex: none; min-width: 16px; color: var(--bx-muted, #A3A6B6); font: ${F.meta}; font-variant-numeric: tabular-nums; }
  .bxc-plan .bxc-pt { flex: 1; min-width: 0; }
  .bxc-plan .bxc-ptag { flex: none; display: inline-flex; align-items: center; gap: 4px; border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px);
    padding: 0 6px; font: ${F.meta}; color: var(--bx-muted, #A3A6B6); white-space: nowrap; }
  .bxc-plan .bxc-ptag.bxc-ok { color: var(--bx-ok, #A3CF5E); }
  .bxc-plan .bxc-ptag.bxc-warn { color: var(--bx-warn, #F2994A); }
  .bxc-plan .bxc-ptag.bxc-bad { color: var(--bx-danger, #FF7A7A); }
  .bxc-plan .bxc-pnote { color: var(--bx-muted, #A3A6B6); font: ${F.meta}; margin-top: 6px; }
  .bxc-gap { color: var(--bx-muted, #A3A6B6); font: ${F.meta}; text-align: center; margin: 4px 0; }
  .bxc-notice { color: var(--bx-muted, #A3A6B6); font: ${F.meta}; margin: 4px 0; white-space: pre-wrap; overflow-wrap: anywhere; }
  .bxc-notice b { display: block; font-weight: 600; }
  /* a line with a glyph (a journal line): the glyph in its own column */
  .bxc-notice.bxc-line { display: flex; align-items: baseline; gap: 4px; }
  .bxc-notice .bxc-ng { flex: none; width: 16px; align-self: center; display: inline-flex; justify-content: center; }
  .bxc-notice.bxc-bad { color: var(--bx-danger, #FF7A7A); }
  .bxc-notice.bxc-nmd { white-space: normal; }
  .bxc-notice.bxc-nmd .bxc-md { flex: 1; min-width: 0; color: var(--bx-text, #E9EAF0); font: ${F.ui}; }
  .bxc-notice button.bxc-more { font: inherit; text-align: left; overflow-wrap: anywhere; }
  .bxc-nfold > summary { list-style: none; cursor: pointer; display: inline-flex; align-items: center; gap: 4px; }
  .bxc-nfold > summary::-webkit-details-marker { display: none; }
  .bxc-nfold:not([open]) > summary .bxc-cd, .bxc-nfold[open] > summary .bxc-cr { display: none; }
  .bxc-nbody { border-left: 2px solid var(--bx-border, #33353F); padding-left: 12px; margin-top: 4px; }
  .bxc-turn { border-top: 1px solid var(--bx-border, #33353F); margin: 12px 0; padding-top: 4px; text-align: center;
    font: ${F.meta}; font-variant-numeric: tabular-nums; color: var(--bx-muted, #A3A6B6); }
  .bxc-err { color: var(--bx-danger, #FF7A7A); }
  /* a card the host draws (a block of its own kind): the kit's frame around it */
  .bxc-host { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); margin: 0 0 8px; padding: 8px 12px; background: var(--bx-panel, #1F2028); }

  /* ---- a tool call's card ---- */
  .bxc-tool { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); margin: 0 0 8px; overflow: hidden; background: var(--bx-panel, #1F2028); }
  .bxc-tool > summary { list-style: none; cursor: pointer; min-height: var(--bx-row, 28px); box-sizing: border-box; padding: 4px 8px;
    display: flex; align-items: center; gap: 8px; font: ${F.code}; }
  .bxc-tool > summary:hover { background: var(--bx-hover, #2A2B34); }
  .bxc-tool > summary::-webkit-details-marker { display: none; }
  .bxc-tool .bxc-ic { flex: none; width: 16px; color: var(--bx-muted, #A3A6B6); }
  .bxc-tool .bxc-title { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bxc-tool .bxc-title .bxc-sub { color: var(--bx-muted, #A3A6B6); margin-left: 8px; }
  .bxc-tool .bxc-under { color: var(--bx-muted, #A3A6B6); }
  /* a call's line ends with what it came to (an exit status in words, +a −d, matches), the still mark of a call at work,
     its state while that needs a look, and the fold's caret */
  .bxc-oc, .bxc-st { flex: none; display: inline-flex; align-items: center; gap: 4px; white-space: nowrap; color: var(--bx-muted, #A3A6B6);
    font-variant-numeric: tabular-nums; }
  .bxc-st { font: ${F.meta}; }
  .bxc-oc bx-icon, .bxc-st bx-icon { --bx-icon-size: 12px; }
  .bxc-oc.bxc-ok { color: var(--bx-ok, #A3CF5E); }
  .bxc-oc.bxc-bad, .bxc-st.bxc-bad { color: var(--bx-danger, #FF7A7A); }
  .bxc-oc.bxc-run, .bxc-st.bxc-run { color: var(--bx-text, #E9EAF0); }
  .bxc-st.bxc-warn { color: var(--bx-warn, #F2994A); }
  .bxc-spin { display: inline-block; flex: none; width: 8px; height: 8px; background: var(--bx-accent, #8C9BFF); }
  .bxc-tool > summary > .bxc-cr, .bxc-tool > summary > .bxc-cd { flex: none; width: 16px; color: var(--bx-muted, #A3A6B6); }
  .bxc-tool:not([open]) > summary > .bxc-cd, .bxc-tool[open] > summary > .bxc-cr { display: none; }
  .bxc-tool[data-state="error"] { border-color: var(--bx-danger, #FF7A7A); }
  .bxc-tool[data-state="approval"] { border-color: var(--bx-warn, #F2994A); }
  .bxc-tool[data-state="stopped"] > summary .bxc-title { color: var(--bx-muted, #A3A6B6); text-decoration: line-through; }
  .bxc-tool .bxc-body { padding: 8px; border-top: 1px solid var(--bx-border, #33353F); display: flex; flex-direction: column; gap: 8px; }
  .bxc-tool .bxc-body:empty { display: none; }
  .bxc-tool pre, .bxc-perm pre { margin: 0; font: ${F.code}; white-space: pre-wrap; overflow-x: auto; }
  .bxc-cmdwrap { position: relative; }
  pre.bxc-cmd { background: var(--bx-code-bg, #16171D); border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 8px 12px; max-height: 320px; overflow: auto; }
  .bxc-copy { position: absolute; top: 4px; right: 4px; display: inline-flex; align-items: center; gap: 4px; padding: 0 6px; cursor: pointer;
    font: ${F.meta}; border: 1px solid var(--bx-border, #33353F);
    background: var(--bx-panel, #1F2028); color: var(--bx-muted, #A3A6B6); border-radius: var(--bx-radius, 2px); }
  .bxc-copy:hover { color: var(--bx-text, #E9EAF0); border-color: var(--bx-border-strong, #666A7E); }
  .bxc-cmdx > summary { list-style: none; cursor: pointer; } .bxc-cmdx > summary::-webkit-details-marker { display: none; }
  .bxc-cmdx[open] > summary .bxc-preview { display: none; }
  .bxc-cmdx .bxc-more { font: ${F.meta}; color: var(--bx-link, #8C9BFF); }
  .bxc-cmdx[open] .bxc-more::after { content: ' (hide)'; }
  pre.bxc-out { color: var(--bx-text, #E9EAF0); border-left: 2px solid var(--bx-border, #33353F); padding-left: 8px; max-height: 360px; overflow: auto; }
  .bxc-raw > summary, .bxc-outx > summary { cursor: pointer; font: ${F.meta}; color: var(--bx-muted, #A3A6B6); }
  .bxc-places { display: flex; flex-wrap: wrap; gap: 2px 12px; font: ${F.code}; color: var(--bx-muted, #A3A6B6); }
  .bxc-places a { color: var(--bx-link, #8C9BFF); overflow-wrap: anywhere; }
  /* the call's own name; its arguments a row each; a plain result; a thought's or a subagent's task */
  .bxc-meta { font: ${F.code}; color: var(--bx-muted, #A3A6B6); overflow-wrap: anywhere; }
  .bxc-args { display: grid; gap: 2px; }
  .bxc-arg { display: grid; grid-template-columns: minmax(60px, auto) 1fr; gap: 8px; min-width: 0; }
  .bxc-ak { font: ${F.code}; color: var(--bx-muted, #A3A6B6); }
  .bxc-av { min-width: 0; overflow-wrap: anywhere; }
  .bxc-tool pre.bxc-av, .bxc-tool pre.bxc-res { word-break: break-word; background: var(--bx-code-bg, #16171D); border: 1px solid var(--bx-border, #33353F);
    border-radius: var(--bx-radius, 2px); padding: 4px 8px; max-height: 320px; overflow: auto; }
  .bxc-clip { display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
  .bxc-clip > pre { align-self: stretch; }
  .bxc-exit { font: ${F.code}; } .bxc-exit.bxc-ok { color: var(--bx-ok, #A3CF5E); } .bxc-exit.bxc-bad { color: var(--bx-danger, #FF7A7A); }
  .bxc-nopatch { font: ${F.code}; }
  .bxc-task { white-space: pre-wrap; color: var(--bx-muted, #A3A6B6); font: ${F.meta}; overflow-x: auto; }

  /* ---- diffs: a rendered patch (diffHTML's or patchLines' line classes) ---- */
  .bxc-diff { margin: 0; font: ${F.code}; background: var(--bx-code-bg, #16171D); font-variant-ligatures: none; font-feature-settings: "liga" 0, "calt" 0; }
  .bxc-diff .fh { display: block; color: var(--bx-text, #E9EAF0); font-weight: 600; }
  .bxc-diff .h { display: block; color: var(--bx-diff-hunk, #5CCBE3); background: var(--bx-diff-hunk-bg, #1C2830); }
  .bxc-diff .d { display: block; color: var(--bx-text, #E9EAF0); background: var(--bx-diff-add-bg, #1D302D); }
  .bxc-diff .a { display: block; color: var(--bx-text, #E9EAF0); background: var(--bx-diff-del-bg, #342429); }
  .bxc-diff .d > .mk { color: var(--bx-diff-add, #5EDBA5); }
  .bxc-diff .a > .mk { color: var(--bx-diff-del, #FF8F8F); }
  .bxc-diff .ctx { display: block; color: var(--bx-diff-context, #A3A6B6); background: var(--bx-diff-context-bg, transparent); }
  .bxc-diff .hljs-comment, .bxc-diff .hljs-quote { color: var(--bx-syn-comment, #9598A9); font-style: italic; }
  .bxc-diff .hljs-keyword, .bxc-diff .hljs-selector-tag, .bxc-diff .hljs-section, .bxc-diff .hljs-name, .bxc-diff .hljs-doctag { color: var(--bx-syn-keyword, #FF8CC8); }
  .bxc-diff .hljs-string, .bxc-diff .hljs-regexp { color: var(--bx-syn-string, #7BE0B0); }
  .bxc-diff .hljs-number, .bxc-diff .hljs-literal, .bxc-diff .hljs-symbol, .bxc-diff .hljs-bullet { color: var(--bx-syn-number, #FFD54A); }
  .bxc-diff .hljs-title { color: var(--bx-syn-function, #96A6FF); }
  .bxc-diff .hljs-type, .bxc-diff .hljs-title.class_ { color: var(--bx-syn-type, #5CCBE3); }
  .bxc-diff .hljs-attr, .bxc-diff .hljs-attribute, .bxc-diff .hljs-property { color: var(--bx-syn-attr, #F5B07A); }
  .bxc-diff .hljs-built_in, .bxc-diff .hljs-meta, .bxc-diff .hljs-variable, .bxc-diff .hljs-template-variable { color: var(--bx-syn-builtin, #C6A8FF); }

  /* ---- a change set: a row per file, its diff beneath it once opened ---- */
  .bxc-files > summary { cursor: pointer; font: ${F.code}; color: var(--bx-text, #E9EAF0); }
  .bxc-add { color: var(--bx-diff-add, #5EDBA5); } .bxc-del { color: var(--bx-diff-del, #FF8F8F); }
  .bxc-add, .bxc-del { font-variant-numeric: tabular-nums; }
  .bxc-flist { list-style: none; margin: 4px 0; padding: 0; font: ${F.code}; }
  .bxc-flist .bxc-frow > summary, .bxc-flist .bxc-frow-static { display: flex; gap: 6px; align-items: baseline; padding: 0 4px; border-radius: var(--bx-radius, 2px); }
  .bxc-flist .bxc-frow > summary { list-style: none; cursor: pointer; }
  .bxc-flist .bxc-frow > summary::-webkit-details-marker { display: none; }
  .bxc-flist .bxc-frow > summary:hover { background: var(--bx-hover, #2A2B34); }
  .bxc-flist .bxc-frow[open] > summary { background: var(--bx-panel-2, #262730); }
  .bxc-changes .bxc-frow[open] > summary { position: sticky; top: 0; z-index: 1; } /* which file a long diff is of */
  .bxc-flist .bxc-frow-static { padding-left: 22px; }
  .bxc-flist .bxc-cr { flex: none; align-self: center; --bx-icon-size: 12px; color: var(--bx-muted, #A3A6B6); transition: transform 0.1s; }
  .bxc-flist .bxc-frow[open] > summary .bxc-cr { transform: rotate(90deg); }
  .bxc-flist .bxc-fp { flex: 0 1 auto; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .bxc-fs { flex: none; width: 1.2em; text-align: center; font-weight: 700; color: var(--bx-muted, #A3A6B6); }
  .bxc-fs.bxc-added { color: var(--bx-diff-add, #5EDBA5); } .bxc-fs.bxc-deleted { color: var(--bx-diff-del, #FF8F8F); }
  .bxc-fs.bxc-modified, .bxc-fs.bxc-renamed { color: var(--bx-warn, #F2994A); }
  .bxc-fbody { margin: 2px 0 8px 22px; display: flex; flex-direction: column; align-items: flex-start; gap: 4px; }
  /* a diff flows in the transcript (no scroller in the scroller), its long lines wrapped */
  .bxc-fbody pre.bxc-diff { align-self: stretch; white-space: pre-wrap; overflow-wrap: anywhere; }
  button.bxc-more { padding: 0; border: 0; background: none; cursor: pointer; color: var(--bx-link, #8C9BFF); font: ${F.meta}; }
  button.bxc-more:hover { text-decoration: underline; }
  .bxc-changes { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); padding: 4px 8px; margin: 0 0 8px; }
  .bxc-tool.bxc-sub { border-color: var(--bx-border-strong, #666A7E); }
  /* a sub-conversation: a stripe says how it stands */
  .bxc-tool.bxc-agent { border-left-width: 2px; }
  .bxc-tool.bxc-agent > summary { font: ${F.ui}; }
  .bxc-tool.bxc-agent[data-state="done"] { border-left-color: var(--bx-ok, #A3CF5E); }
  .bxc-tool.bxc-agent[data-state="error"], .bxc-tool.bxc-agent[data-state="stopped"] { border-left-color: var(--bx-danger, #FF7A7A); }
  .bxc-ref { flex: none; font: ${F.code}; color: var(--bx-muted, #A3A6B6); }
  .bxc-tool > summary .bxc-open { flex: none; display: inline-flex; align-items: center; gap: 4px; }
  .bxc-k { font: ${F.micro}; letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; color: var(--bx-muted, #A3A6B6); margin-right: 4px; }
  /* a task: its first two lines until asked for */
  .bxc-clamp { cursor: pointer; display: -webkit-box; -webkit-line-clamp: 2; -webkit-box-orient: vertical; overflow: hidden; }
  .bxc-clamp.bxc-all { display: block; }
  .bxc-tool.bxc-sub .bxc-children { border-left: 2px solid var(--bx-border-strong, #666A7E); padding-left: 8px; }
  .bxc-tool.bxc-sub .bxc-children:empty { display: none; }
  .bxc-tool.bxc-sub .bxc-children .bxc-msg { margin-bottom: 6px; }
  .bxc-tool.bxc-sub .bxc-answer { border-top: 1px solid var(--bx-border, #33353F); padding-top: 8px; }

  /* ---- a request: attention, in the warn tint, its glyph and word ---- */
  .bxc-perm { border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px); padding: 8px 12px; margin: 0 0 12px;
    background: var(--bx-warn-bg, #382F2C); display: flex; flex-direction: column; gap: 8px; }
  .bxc-perm .bxc-q > b bx-icon { color: var(--bx-warn, #F2994A); margin-right: 6px; }
  .bxc-perm .bxc-desc { color: var(--bx-muted, #A3A6B6); margin-top: 2px; }
  .bxc-perm .bxc-ptitle { font: ${F.code}; display: block; margin-top: 2px; overflow-wrap: anywhere; } /* the call, its own element under the lead (D250: never "lead · call") */
  .bxc-perm .bxc-qmsg { font: ${F.body}; }
  .bxc-perm .bxc-calls { margin: 0; padding-left: 20px; }
  .bxc-perm .bxc-pbody:empty { display: none; }
  .bxc-perm .bxc-word { width: 100%; }
  .bxc-perm .bxc-rulenote, .bxc-perm .bxc-note { color: var(--bx-muted, #A3A6B6); font: ${F.meta}; }
  .bxc-perm .bxc-note.bxc-err { color: var(--bx-danger, #FF7A7A); }
  /* answered: nothing waits on it any more — a plain card */
  .bxc-perm.bxc-settled { border-color: var(--bx-border, #33353F); background: var(--bx-panel, #1F2028); }
  .bxc-perm .bxc-btns { display: flex; gap: 8px; flex-wrap: wrap; }
  .bxc-perm .bxc-btns.bxc-col { flex-direction: column; align-items: stretch; }
  .bxc-perm button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 4px 11px; cursor: pointer; text-align: left;
    border: 1px solid var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border-radius: var(--bx-radius, 2px); font: ${F.ui}; font-weight: 600; }
  .bxc-perm button:hover { background: var(--bx-hover, #2A2B34); }
  .bxc-perm button:disabled { opacity: .5; cursor: default; }
  .bxc-perm button.bxc-primary { background: var(--bx-accent, #8C9BFF); border-color: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); }
  .bxc-perm button.bxc-primary:hover { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  /* an option that raises what the agent may do without asking: the destructive outline, and it is confirmed */
  .bxc-perm button.bxc-explicit { background: var(--bx-panel, #1F2028); border-color: var(--bx-danger, #FF7A7A); color: var(--bx-danger, #FF7A7A); }
  .bxc-perm button.bxc-explicit:hover { background: var(--bx-danger-bg, #3A2B32); }
  .bxc-perm button.bxc-explicit bx-icon { margin-right: 6px; }
  .bxc-perm .bxc-settled-by { color: var(--bx-muted, #A3A6B6); font: ${F.meta}; }
  .bxc-ask .bxc-field { border-top: 1px solid var(--bx-border, #33353F); padding-top: 8px; display: flex; flex-direction: column; gap: 4px; }
  .bxc-ask .bxc-field a { color: var(--bx-link, #8C9BFF); overflow-wrap: anywhere; } /* a question that is a page to open */
  .bxc-ask .bxc-fh { font-weight: 600; }
  .bxc-ask .bxc-fd { color: var(--bx-text, #E9EAF0); }
  .bxc-ask .bxc-opts { display: flex; flex-direction: column; gap: 2px; }
  .bxc-ask .bxc-opt { display: flex; gap: 6px; align-items: baseline; cursor: pointer; }
  .bxc-ask .bxc-opt input { accent-color: var(--bx-accent, #8C9BFF); }
  .bxc-ask .bxc-opt .bxc-od { color: var(--bx-muted, #A3A6B6); }
  .bxc-ask .bxc-txt, .bxc-perm .bxc-txt { box-sizing: border-box; min-height: var(--bx-control-h, 28px); background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); padding: 4px 8px; font: ${F.ui}; }
  .bxc-ask .bxc-txt::placeholder, .bxc-perm .bxc-txt::placeholder, .bxc-reject textarea::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
  .bxc-ask .bxc-answers { margin: 0; padding-left: 16px; }
  bx-chat-form { display: flex; flex-direction: column; gap: 8px; }
  .bxc-plan-card { border-color: var(--bx-border-strong, #666A7E); background: var(--bx-panel, #1F2028); }
  .bxc-plan-md { max-height: 50vh; overflow: auto; background: var(--bx-panel-2, #262730); border: 1px solid var(--bx-border, #33353F);
    border-radius: var(--bx-radius, 2px); padding: 8px 12px; font: ${F.body}; }
  .bxc-reject { display: flex; gap: 8px; align-items: flex-end; }
  .bxc-reject textarea { flex: 1; resize: vertical; box-sizing: border-box; background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); padding: 4px 8px; font: ${F.ui}; }
  /* signed out: a warning (its glyph, its words, its tint) the host fills with its sign-in */
  .bxc-signin { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; margin-bottom: 8px; padding: 8px 12px;
    border: 1px solid var(--bx-warn, #F2994A); border-radius: var(--bx-radius, 2px); background: var(--bx-warn-bg, #382F2C); color: var(--bx-text, #E9EAF0); }
  .bxc-signin .bxc-q { flex: 1; }
  .bxc-signin .bxc-q bx-icon { color: var(--bx-warn, #F2994A); margin-right: 6px; }

  /* ---- usage: the context in use (and what it cost) ---- */
  .bxc-usage { font: ${F.meta}; font-variant-numeric: tabular-nums; color: var(--bx-muted, #A3A6B6); white-space: nowrap; }
  .bxc-usage.bxc-warn { color: var(--bx-warn, #F2994A); }
  .bxc-usage.bxc-bad { color: var(--bx-danger, #FF7A7A); }

  /* ---- the window's own lines ---- */
  .bxc-earlier { display: flex; gap: 8px; justify-content: center; align-items: baseline; }
  .bxc-earlier button { border: 0; background: none; padding: 0; cursor: pointer; color: var(--bx-link, #8C9BFF); font: inherit; }
  .bxc-pill { position: absolute; bottom: calc(100% + 8px); left: 50%; transform: translateX(-50%); z-index: 1; white-space: nowrap;
    display: inline-flex; align-items: center; gap: 6px; box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer;
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); font: ${F.ui};
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); font-weight: 600; box-shadow: var(--bx-shadow-pop, 0 12px 32px rgba(0, 0, 0, 0.6)); }
  .bxc-pill:hover { background: var(--bx-hover, #2A2B34); }

  /* ---- the composer ---- */
  bx-chat-composer { display: block; font: ${F.ui}; }
  bx-chat-composer button, bx-chat-composer select, bx-chat-composer textarea { font: inherit; }
  .bxc-compose { display: flex; gap: 8px; align-items: flex-end; }
  .bxc-compose textarea { flex: 1; resize: none; box-sizing: border-box; min-height: var(--bx-control-h, 28px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px); padding: 4px 8px; max-height: 40vh; }
  .bxc-compose textarea::placeholder { color: var(--bx-subtle, #8E91A2); opacity: 1; }
  .bxc-compose.bxc-drop textarea { border-color: var(--bx-accent, #8C9BFF); }
  /* Send (and Start) is the primary action; Stop the destructive outline; attach a quiet square */
  .bxc-compose button { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 11px; cursor: pointer; font-weight: 600;
    border: 1px solid var(--bx-accent, #8C9BFF); background: var(--bx-accent, #8C9BFF); color: var(--bx-accent-ink, #0B0C12); border-radius: var(--bx-radius, 2px); }
  .bxc-compose button:hover:not(:disabled) { background: var(--bx-accent-hover, #A9B4FF); border-color: var(--bx-accent-hover, #A9B4FF); }
  .bxc-compose button:disabled { opacity: .5; cursor: default; }
  .bxc-compose button.bxc-stop { background: var(--bx-panel, #1F2028); color: var(--bx-danger, #FF7A7A); border-color: var(--bx-danger, #FF7A7A); }
  .bxc-compose button.bxc-stop:hover { background: var(--bx-hover, #2A2B34); border-color: var(--bx-danger, #FF7A7A); }
  .bxc-compose button.bxc-attach { display: inline-flex; align-items: center; justify-content: center; width: var(--bx-control-h, 28px); padding: 0;
    background: var(--bx-panel, #1F2028); color: var(--bx-muted, #A3A6B6); border-color: var(--bx-border-strong, #666A7E); }
  .bxc-compose button.bxc-attach:hover:not(:disabled) { background: var(--bx-hover, #2A2B34); color: var(--bx-text, #E9EAF0); border-color: var(--bx-border-strong, #666A7E); }
  .bxc-compose input[type=file] { display: none; }
  .bxc-slash { border: 1px solid var(--bx-border, #33353F); border-radius: var(--bx-radius, 2px); background: var(--bx-panel-2, #262730);
    margin-bottom: 6px; max-height: 220px; overflow-y: auto; }
  .bxc-slash .bxc-sc { display: flex; gap: 8px; align-items: center; min-height: var(--bx-row, 28px); padding: 0 8px; cursor: pointer; }
  .bxc-slash .bxc-sc.bxc-on { background: var(--bx-selection, #262C5C); color: var(--bx-selection-text, #E9EAF0); box-shadow: inset 2px 0 0 var(--bx-accent, #8C9BFF); }
  .bxc-slash .bxc-sc b { font: ${F.code}; font-weight: 600; white-space: nowrap; }
  .bxc-slash .bxc-sc .bxc-d { color: var(--bx-muted, #A3A6B6); overflow: hidden; text-overflow: ellipsis; white-space: nowrap; flex: 1; }
  .bxc-slash .bxc-sc .bxc-hint { color: var(--bx-muted, #A3A6B6); font: ${F.code}; white-space: nowrap; }
  .bxc-slash-hint { font: ${F.code}; color: var(--bx-muted, #A3A6B6); margin-bottom: 4px; }
  /* messages held for the turn's end, and files waiting to go with the next one: dashed — not sent yet */
  .bxc-queued, .bxc-staged { display: flex; flex-wrap: wrap; align-items: center; gap: 6px; padding: 0 0 6px; }
  .bxc-qchip { display: inline-flex; align-items: center; gap: 4px; max-width: min(320px, 100%); min-width: 0;
    padding: 2px 2px 2px 8px; border: 1px dashed var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0); font: var(--bx-font-body, 400 13px/18px system-ui, sans-serif); }
  .bxc-qchip .bxc-qt { min-width: 0; overflow: hidden; white-space: nowrap; text-overflow: ellipsis; }
  .bxc-qchip .bxc-qf { flex: none; color: var(--bx-muted, #A3A6B6); }
  .bxc-qchip .bxc-qx { flex: none; display: inline-flex; align-items: center; justify-content: center; width: 22px; height: 22px; padding: 0;
    border: 0; background: none; color: var(--bx-muted, #A3A6B6); cursor: pointer; }
  .bxc-qchip .bxc-qx:hover, .bxc-qchip .bxc-qx:focus-visible { color: var(--bx-text, #E9EAF0); }
  .bxc-qhint { color: var(--bx-muted, #A3A6B6); font: var(--bx-font-micro, 600 11px/14px system-ui, sans-serif); }
  .bxc-qhint.bxc-err { color: var(--bx-danger, #FF7A7A); }
  /* the pickers over the box: the agent, its mode, its model */
  .bxc-pickers { display: flex; gap: 8px; margin-bottom: 8px; flex-wrap: wrap; align-items: center; }
  .bxc-pickers label { display: inline-flex; align-items: center; gap: 6px; color: var(--bx-muted, #A3A6B6);
    font: ${F.micro}; letter-spacing: var(--bx-tracking-micro, 0.06em); text-transform: uppercase; }
  .bxc-pickers select { box-sizing: border-box; min-height: var(--bx-control-h, 28px); padding: 0 6px;
    background: var(--bx-panel, #1F2028); color: var(--bx-text, #E9EAF0);
    border: 1px solid var(--bx-border-strong, #666A7E); border-radius: var(--bx-radius, 2px);
    font: ${F.ui}; letter-spacing: 0; text-transform: none; }
  .bxc-pickers .bxc-pnote { color: var(--bx-muted, #A3A6B6); font: ${F.ui}; }
  .bxc-unavailable { flex-basis: 100%; margin: 0; padding: 0; list-style: none; color: var(--bx-muted, #A3A6B6); font: ${F.meta}; }
  .bxc-unavailable b { font-weight: 600; color: var(--bx-text, #E9EAF0); }
`;
