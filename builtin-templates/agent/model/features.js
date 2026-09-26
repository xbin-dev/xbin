// model/features.js — every feature of the agent tile's UI, by key: the
// contract that keeps its views level. Each view declares the keys it
// implements (IMPLEMENTS: web-features.js for the web; the native view its
// own), and a node test (hack/agent-template-features.test.mjs) fails when a
// view misses a key that is not listed below as an intended difference, with
// the reason. A UX change lands here, in the model and in BOTH views in the
// same change; a view-only change says why it is view-specific (DIFFERENCES).
//
// Keys are <area>.<feature>[.<detail>]; the areas follow the tile's surfaces.
export const AREAS = {
  home: 'Home — no conversation open',
  conv: 'Conversations — the list',
  chat: 'Transcript — the open conversation',
  ask: 'Asking — the agent waits for you',
  composer: 'Composer — writing to the agent',
  top: 'Top bar — the open conversation\'s header',
  tools: 'Run tools — memory, files, skills, the workflow tree, the render preview',
  share: 'Sharing a conversation',
  auto: 'Automations — schedules, watchers, channels, triggers',
  manage: 'Managers — settings and the brake',
  state: 'States the whole tile can be in',
  link: 'Deep links',
  needs: 'Needs you — beyond the tile',
};

export const FEATURES = {
  // Home
  'home.greeting': 'the greeting and tagline (HOME, model/home.js)',
  'home.examples': 'example asks; picking one puts it into the composer',
  'home.needs': '"Needs you" (GET /needs): questions, approvals and failed automations; picking one opens it',
  'home.mcpHint': 'a hint when no MCP server is bound yet',

  // Conversations
  'conv.new': 'New chat (home, the composer focused)',
  'conv.newOptions': 'new chat with options: first message, tool mode, title, instructions',
  'conv.search': 'search conversations (?q=)',
  'conv.search.snippets': 'search results show the matching line',
  'conv.search.join': 'pasting a #join= link into search joins that conversation',
  'conv.groups': 'date groups: Today, Yesterday, Previous 7 days, Previous 30 days, Older (model/conv-groups.js)',
  'conv.pinned': 'your pins first, as their own group',
  'conv.unread': 'unread conversations stand out; looking at one marks it read',
  'conv.shared': '⇆ on shared conversations, with who shared it',
  'conv.status': 'status glyphs: ? waiting for you, ! failed, a spinner while it works',
  'conv.more': 'paging: "more" at the end of the list',
  'conv.live': 'the list stays current from the stream: new rows, status, pins, revocations, deletions',
  'conv.rename': 'rename (owner)',
  'conv.pin': 'pin / unpin',
  'conv.share': 'share from the row (owner)',
  'conv.archive': 'archive / unarchive',
  'conv.delete': 'delete, confirmed (owner)',
  'conv.leave': 'leave a conversation shared with you, confirmed',
  'conv.scope.mine': 'scope: your conversations',
  'conv.scope.team': 'scope: shared with the team',
  'conv.scope.archived': 'scope: your archive',

  // Transcript
  'chat.user': 'your messages',
  'chat.user.sender': 'who wrote a message, in a shared conversation',
  'chat.user.files': 'a message\'s attachments: image thumbnails, deleted ones marked, opening one in Files',
  'chat.assistant': 'the assistant\'s answers as sanitized markdown (no remote images, safe links)',
  'chat.assistant.streaming': 'the answer streams in while it is written',
  'chat.thinking': 'thinking: open while live, folded after with its duration',
  'chat.tool': 'a tool call as one card: headline, family icon, state (tool-heads.js)',
  'chat.tool.detail': 'a card opens to its arguments and result',
  'chat.tool.resultCut': 'a long result is cut at 1200 characters, with "show all"',
  'chat.agent': 'a subagent as a card: open while running, its own work folded inside',
  'chat.agent.open': 'open a subagent\'s full session',
  'chat.agent.approval': 'approve a subagent\'s tool call from its card',
  'chat.agent.answer': 'a finished subagent\'s answer on its card',
  'chat.step': 'step lines: notes, errors, sleeps, finishes, renders, cancels',
  'chat.notice': 'engine notices (subagent results, messages from a parent), folded',
  'chat.notice.automation': 'what an automation delivered, labelled (Scheduled, Watcher check, …)',
  'chat.compaction': 'a banner where earlier turns were compacted',
  'chat.breadcrumbs': 'a subagent\'s chain of parents, each one a link',
  'chat.activity': 'the one line saying what the run is doing',
  'chat.reconnecting': 'live updates lost — reconnecting',
  'chat.follow': 'the chat sticks to its end while it grows',
  'chat.queue': 'messages sent while the agent works, queued above the composer',
  'chat.queue.takeBack': 'take a queued message back',

  // Asking
  'ask.approval': 'an approval card: the calls it wants to run, approve or deny',
  'ask.question': 'the agent\'s question, answered by your next message',

  // Composer
  'composer.text': 'a text box that grows with its text',
  'composer.keys': 'Enter sends, Shift+Enter is a new line, an IME\'s Enter is the IME\'s',
  'composer.placeholder': 'its prompt by state: a new ask, view only, steer, answer, follow up',
  'composer.disabled': 'disabled in a conversation you may only read',
  'composer.toolMode': 'the tool mode for new asks (🔒 internal / 🌐 web), remembered per person',
  'composer.attach': 'attach files (a picker)',
  'composer.attach.paste': 'paste an image to attach it',
  'composer.attach.drop': 'drop files on the chat to attach them',
  'composer.attach.limit': 'the 16 MiB per-file cap, marked on the chip',
  'composer.attach.camera': 'attach a photo from the camera or the photo library',
  'composer.dictation': 'dictate a message',
  'composer.uploadChips': 'attachment chips: size, uploading, failed, remove',
  'composer.heldAsk': 'a new ask with attachments: created held, uploaded into, then sent',
  'composer.send': 'Send',
  'composer.stop': 'Stop the run',
  'composer.stop.returns': 'Stop gives the still-queued text back to the composer',

  // Top bar
  'top.crumb': 'an automation\'s run links back to it (Automations ›)',
  'top.title': 'the conversation\'s title',
  'top.toolMode': 'its tool mode (immutable per run)',
  'top.status': 'its status',
  'top.viewOnly': 'view only, when shared with you to read',
  'top.retry': 'Retry, when the run failed or was cancelled',
  'top.compact': 'Compact',
  'top.learn': 'Learn skill',
  'top.memory': 'Memory (n) — opens its memory blocks',
  'top.files': 'Files (n) — opens its session files',
  'top.tree': '⑂ tree — opens the workflow tree',
  'top.share': 'Share / Shared',
  'top.delete': 'Delete, confirmed (owner)',

  // Run tools
  'tools.memory': 'a run\'s memory blocks: edit, add, delete',
  'tools.files': 'a run\'s session files: list, delete',
  'tools.files.editor': 'edit a text file (the version you loaded is sent back: a conflicting write is a visible 409)',
  'tools.files.attachments': 'an attachment: image preview, download',
  'tools.skills': 'the skill library: list, edit, add, delete',
  'tools.tree': 'the workflow tree: nodes by parent, their state and what blocks them',
  'tools.tree.cost': 'cost per node and in total, the running/limit count',
  'tools.tree.stop': 'stop the whole workflow, confirmed',
  'tools.render': 'the render preview of an HTML file: sandboxed, no scripts, nothing external loads',
  'tools.render.follow': 'a new render opens the preview; one you closed stays closed',
  'tools.render.blocked': 'says how many external resources it blocked, and when it shows a newer version',
  'tools.render.maximize': 'maximize the preview',
  'tools.render.source': 'open the rendered file in Files',

  // Sharing
  'share.visibility': 'who can see it: only invited people, the team to read, the team to write',
  'share.members': 'the people it is shared with and their roles: add, change, remove',
  'share.links': 'invite links: create (shown once), with role and expiry',
  'share.links.revoke': 'revoke an invite link',
  'share.readOnly': 'someone it was shared with sees the same, read-only',
  'share.leave': 'leave it from the dialog',

  // Automations
  'auto.entry': 'the Automations entry, with what is new and what failed',
  'auto.page': 'the page: a section per kind, a card per automation',
  'auto.card': 'a card\'s badges: new runs, needs attention, failed, off; whose it is',
  'auto.detail': 'one automation: what it does and its runs, paged; opening it marks them read',
  'auto.schedule.form': 'new / edit schedule: name, cadence presets or a custom cron, what to do, where runs go, tool mode, visibility',
  'auto.watcher.form': 'new / edit watcher',
  'auto.schedule.runNow': 'run now',
  'auto.schedule.toggle': 'switch on / off',
  'auto.schedule.reset': 'start afresh (a thread)',
  'auto.schedule.delete': 'delete, confirmed',
  'auto.schedule.target': 'a schedule that reports into a conversation links to it',
  'auto.channel.claim': 'claim an announced chat channel, with its rules',
  'auto.channel.pairing': 'the pairing queue: approve a code, allow or block who asked',
  'auto.channel.people': 'the people it knows: allow, block, trust, unlink, forget, add',
  'auto.channel.sessions': 'its sessions: open one, start one afresh',
  'auto.channel.undelivered': 'replies it could not deliver: retry',
  'auto.channel.rules': 'its rules: DMs, groups, mentions, threads, lanes, reset, rate, instructions',
  'auto.channel.manage': 'switch it off, remove it (confirmed)',
  'auto.trigger.form': 'new / edit trigger: source, topics, what to do, where events go, cap, lane, data class, announce, visibility',
  'auto.trigger.firewall': 'the form refuses a lane/data-class clash',
  'auto.trigger.testFire': 'fire a test event',
  'auto.trigger.events': 'its recent events, and why one did not run',
  'auto.trigger.wiring': 'what it still needs: a grant, or a binding',
  'auto.trigger.unmatched': 'pushes nothing took, with "create a trigger"',
  'auto.trigger.manage': 'switch on / off, start afresh, delete (confirmed)',

  // Managers
  'manage.settings': 'settings, for managers only',
  'manage.config': 'the config: model per tier, system prompt, limits, behaviour',
  'manage.features': 'feature switches',
  'manage.mcp': 'the MCP servers bound',
  'manage.halt': 'halt every run (while runs are active), and resume',

  // States
  'state.halted': 'halted: the switch says so',
  'state.errors': 'a failed action tells the person why',

  // Deep links
  'link.conv': 'an address opens a conversation (#c=<id>)',
  'link.auto': 'an address opens the Automations page or one automation (#auto[=kind:id])',
  'link.join': 'an invite link joins a conversation (#join=<token>)',

  // Needs you, beyond the tile
  'needs.push': 'a question, an approval or a failed automation reaches your phone (the backend pushes it; tapping it opens the conversation)',
};

// DIFFERENCES: keys a view does not implement ON PURPOSE, with the reason.
// Anything else missing from a view fails the features test.
export const DIFFERENCES = {
  web: {
    'needs.push': 'a web page does not receive pushes: the backend sends Needs-you to the person\'s xbin app (POST /api/xbin/notify), which opens the conversation in the native view',
    'composer.dictation': 'the browser and the OS dictate into any text box; the tile adds no control of its own',
    'composer.attach.camera': 'the browser\'s file picker offers the camera and the photo library itself',
  },
  native: {
    'composer.keys': 'on a phone Return is a new line and Send is the button; the app\'s composer handles a hardware keyboard and IME composition itself',
    'composer.attach.paste': 'the app\'s composer owns the pasteboard: an image pasted there is uploaded like a picked one — nothing for the tile to draw',
    'composer.attach.drop': 'dropping files on the composer (iPad) is the app\'s: they upload like picked ones — nothing for the tile to draw',
  },
};

// gaps checks a view's declaration against the registry:
//   missing  keys it neither implements nor lists as a difference
//   unknown  keys it implements that the registry does not have
//   stale    differences listed for it that it implements, or that no longer exist
export function gaps(view, implemented) {
  const has = new Set(Object.keys(implemented || {}));
  const diff = DIFFERENCES[view] || {};
  return {
    missing: Object.keys(FEATURES).filter((k) => !has.has(k) && !diff[k]),
    unknown: [...has].filter((k) => !(k in FEATURES)),
    stale: Object.keys(diff).filter((k) => has.has(k) || !(k in FEATURES)),
  };
}
