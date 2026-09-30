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
  'conv.newOptions': 'new chat with options: first message, class, title, instructions',
  'conv.search': 'search conversations (?q=)',
  'conv.search.snippets': 'search results show the matching line',
  'conv.search.join': 'pasting a #join= link into search joins that conversation',
  'conv.groups': 'date groups: Today, Yesterday, Previous 7 days, Previous 30 days, Older (model/conv-groups.js)',
  'conv.pinned': 'your pins first, as their own group',
  'conv.unread': 'unread conversations stand out; looking at one marks it read',
  'conv.shared': 'a shared row says how, as chips: from whom (someone else\'s), the team (to read or to write), how many people',
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
  'conv.scope.shared': 'scope: shared — what you shared and what others shared with you, in two sections',
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
  'chat.step.finish': 'a finish line\'s result as sanitized markdown (a model often puts its answer there)',
  'chat.step.reopen': 'a render\'s and a live page\'s lines open them again once closed (a subagent\'s, from its own run)',
  'chat.notice': 'engine notices (subagent results, messages from a parent), folded',
  'chat.notice.automation': 'what an automation delivered, labelled (Scheduled, Watcher check, …)',
  'chat.compaction': 'a banner where earlier turns were compacted, and the step saying what was summarised and how many old tool outputs were hidden (rules.compactionWords)',
  'chat.breadcrumbs': 'a subagent\'s chain of parents, each one a link',
  'chat.activity': 'the one line saying what the run is doing',
  'chat.reconnecting': 'live updates lost — reconnecting',
  'chat.follow': 'the chat sticks to its end while it grows',
  'chat.older': 'a long conversation opens on its newest page; older ones load as you scroll up (API.md "Paging the view")',
  'chat.window': 'a long conversation stays quick: a window of it is drawn and what lies far from it let go, and what you read never moves',
  'chat.jumpLatest': 'reading far up, the live end is let go too and "↓ N new — jump to latest" brings it back',
  'chat.queue': 'messages sent while the agent works, queued above the composer',
  'chat.queue.takeBack': 'take a queued message back',

  // Asking
  'ask.approval': 'an approval card: the calls it wants to run, approve or deny; a verdict refused because the ask is gone (409) says so',
  'ask.grant': 'a grant card (D111): the agent asks to read your other conversations — its owner allows it once or here for an hour; others may only deny',
  'ask.question': 'the agent\'s question, answered by your next message',

  // Composer
  'composer.text': 'a text box that grows with its text',
  'composer.keys': 'Enter sends, Shift+Enter is a new line, an IME\'s Enter is the IME\'s',
  'composer.placeholder': 'its prompt by state: a new ask, view only, steer, answer, follow up',
  'composer.disabled': 'disabled in a conversation you may only read',
  'composer.class': 'the class for new chats (D116): icon and name, each one\'s description in its menu, only the classes you may use (GET /classes); your last pick is your default',
  'composer.model': 'the model: any bound provider\'s, grouped by provider — the open conversation\'s from its next turn, or the next new chat\'s; your last pick is your default',
  'composer.sandbox': 'the coding sandbox (D115), beside the model — only where the class (the conversation\'s, or the new chat\'s) has the sandbox toolset: grouped This conversation · Yours · Shared · Team, ones you may not use or the class does not allow disabled with the reason, ＋ New and Manage…; a pick binds it from the next turn (at home: the new chat starts in it)',
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
  'top.class': 'its class, fixed for its life (icon and name); a class that can move internal data out says so',
  'top.model': 'the model it was switched to, when one was picked',
  'top.task': 'its task, pinned (D133): the current request — the latest it was given — verbatim, and how many others there are — opening to every request it was given (GET /runs/{id}/asks), read-only',
  'top.sandbox': '▣ its sandbox and working directory, and why a binding no longer resolves (gone, its manager unbound or down, its class no longer allows it); opens the working directory, switching among the attached ones, Detach, Manage…',
  'top.status': 'its status',
  'top.viewOnly': 'view only, when shared with you to read',
  'top.retry': 'Retry, when the run failed or was cancelled',
  'top.compact': 'Compact',
  'top.learn': 'Learn skill',
  'top.memory': 'Memory (n) — opens its memory blocks',
  'top.files': 'Files (n) — opens its session files',
  'top.tree': '⑂ tree — opens the workflow tree',
  'top.grant': 'what the owner let the agent read here and until when, with a revoke (the owner)',
  'top.share': 'who can see it, said plainly (private · team can read/write · shared with N people · from its owner), opening the share dialog',
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
  'tools.live': 'a live preview (preview_port, D135): a page a server in the conversation\'s sandbox serves, its scripts running in an isolated frame (opaque origin: no cookies, no storage, no reach into the workspace), for the run\'s participants; labelled "live from the sandbox"',
  'tools.live.follow': 'a new live step opens the preview, as a render does',
  'tools.live.reload': 'reload the live page (a fresh link)',
  'tools.live.check': 'what the live page answers now — status, type, a refusal and what to do (nothing listening, the sandbox stopped, an agent from before ports, an expired or misplaced link) — and Check to ask again; a page that answers an error is said, never shown blank',
  'tools.live.ports': 'Ports, where the conversation\'s sandbox is described: its live previews, each probed now, with Open, and a probe of any port (GET /runs/{id}/ports)',
  'tools.sandboxes': 'the Sandboxes screen (D115): every sandbox you may see — state, manager, image, egress, owner, private/team, last active, where it is bound — with start, stop, archive, thaw, share with the team / make private and delete (confirmed) as your rights allow, and "Use here"',
  'tools.sandboxes.terminal': 'a terminal in a sandbox whose manager offers one (tty): Open terminal in the ▣ popover (the active sandbox, at its working directory) and Terminal on a Sandboxes row — the page dials the manager as you (its per-person rules apply); closing it ends the shell',
  'tools.sandboxes.create': 'create a sandbox: manager, name, image, size, network (what the class allows), private or team, a working directory; made in a conversation it is bound there (a team conversation\'s is a team one), at home the next new chat starts in it',
  'tools.sandboxes.shareTerminal': 'share a sandbox of yours (this agent its home) with a terminal tile — the builtin sandbox-terminal (D121): its path (apps/sandbox-terminal by default), for you, or everyone who may use it when it is a team one; the shares it has now, each stopped (confirmed)',

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
  'auto.schedule.form': 'new / edit schedule: name, cadence presets or a custom cron, what to do, where runs go, class, visibility',
  'auto.class': 'schedules, watchers and triggers run in a class (D116): the forms pick one of the classes you may use (a schedule\'s is fixed once made); cards and details say it, with the warning of one that can move internal data out',
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
  'auto.channel.classes': 'the classes its conversations run in (D116): everyone else\'s (only classes that reach outside with no internal reach) and, with the private lane, trusted people\'s',
  'auto.channel.manage': 'switch it off, remove it (confirmed)',
  'auto.trigger.form': 'new / edit trigger: source, topics, what to do, where events go, cap, class, data class, announce, visibility',
  'auto.trigger.firewall': 'the form refuses a class/data-class clash: private data into the web lane or a chat, public data into a class that can move internal data out',
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
  'manage.classes': 'the classes: list, add, edit (name, icon, description, toolsets, MCP servers, sandbox managers and egress, model, system addendum, who), delete — a built-in resets to its default; the default for new chats; saving one that can move internal data out is confirmed',
  'manage.halt': 'halt every run (while runs are active), and resume',

  // States
  'state.halted': 'halted: the switch says so',
  'state.errors': 'a failed action tells the person why',
  'state.partition.share': 'in a person\'s own partition (a partitioned instance) a conversation can\'t be shared: no Share in its row menu or header, no Shared view (model/partition.js)',
  'state.partition.global': 'at a partitioned instance\'s global instance (the owner token), a note to sign in as a person for private conversations',
  'state.partition.sandboxes': 'in a person\'s partition, a notice naming a bound sandbox manager that can\'t keep people apart, and how to update it',
  'state.partition.hidden': 'in a partitioned instance the live stream closes while the page is hidden and resumes from its cursor when it shows (model/stream.js)',

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
    'chat.jumpLatest': 'the native view never lets the live end go: the app\'s transcript keeps a row still only at its bottom, so letting go below the reader would move what they read — until the renderer anchors a row across a trim (D130 E3/E4), and the app scrolls to the end itself',
    'tools.live.ports': 'the ▣ popover is the web\'s; on the app a live preview\'s screen has its own Check (tools.live.check), which probes what the Ports section would',
    'tools.sandboxes.terminal':'the app\'s terminal primitive dials only the tile\'s own routes (TileTerminal refuses any other address), and a manager\'s tty is another tile\'s; relaying it through the agent\'s backend would make the person the manager checks an asserted one instead of the verified one. Until the app takes a bound interface\'s URL, terminals are on the web',
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
