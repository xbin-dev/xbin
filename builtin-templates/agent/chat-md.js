// chat-md.js — model text as markdown, sanitized: xbind's renderer
// (/vendor/chat/md.js, D224), the one the terminal window's Agent tab draws
// with. This file keeps its name and its exports for the modules here that
// import it.
//
// What the renderer holds to: raw HTML tokens are shown escaped (model output
// is untrusted — an injected <script>/<img> must never execute with this
// tile's frame token), links get safe schemes and a new tab, and images
// render as their source text RATHER THAN LOADING. That last one is
// load-bearing: the platform CSP on /c/ documents has no img-src, so a
// model-authored <img src="https://…/?leak=…"> would be a live exfiltration
// beacon. Streaming-tolerant: a parse error falls back to escaped text.
//
// Fenced code with a language is highlighted (#26) by xbind's
// /vendor/highlight.min.js, which the renderer imports on the first such
// fence and draws with its classes on the theme's --bx-syn-* tokens
// (index.html); until it is there code is plain. Only a CLOSED fence is
// highlighted: the one being streamed stays plain until its closing fence
// arrives (mdInto parses that block again then). When the highlighter
// arrives, mdEpoch moves on: HTML parsed before it (chat-cards.js memos,
// mdInto's blocks) is parsed again at the next paint, which onHighlighter
// asks for.
//
// The renderer came with the chat kit's revision 1, so this page runs on an
// xbind that serves it: boot.js checks before agent.js (and so this file) is
// loaded, and says which xbind the page needs when it is older.
export { md, mdInto, closedFence, mdEpoch, onHighlighter } from '/vendor/chat/md.js';
