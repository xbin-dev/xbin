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
  harness: 'Coding agents — a conversation with Claude Code, Codex, Gemini or opencode in a coding sandbox',
  proj: 'Projects — a sandbox, its repos and task conversations, each with a git worktree per repo',
  ci: 'CI — what a conversation pushed, as the platform\'s CI reports it',
};

export const FEATURES = {
  // Home
  'home.greeting': 'the greeting and tagline (HOME, model/home.js)',
  'home.examples': 'example asks; picking one puts it into the composer',
  'home.needs': '"Needs you" (GET /needs): questions, approvals, a coding agent waiting for you to sign in (named when the item says which, D147 §4.3.9) and failed automations; picking one opens it',
  'home.mcpHint': 'a hint when no MCP server is bound yet',

  // Conversations
  'conv.new': 'New chat (home, the composer focused)',
  'conv.newOptions': 'new chat with options: first message, who answers (the built-in agent or a coding agent, and the sandbox it starts in — its class resolves, instructions are the built-in agent\'s), class, title, instructions',
  'conv.search': 'search conversations (?q=)',
  'conv.search.snippets': 'search results show the matching line',
  'conv.search.join': 'pasting a #join= link into search joins that conversation',
  'conv.groups': 'date groups: Today, Yesterday, Previous 7 days, Previous 30 days, Older (model/conv-groups.js)',
  'conv.pinned': 'your pins first, as their own group',
  'conv.unread': 'unread conversations stand out; looking at one marks it read',
  'conv.shared': 'a shared row says how, as chips: from whom (someone else\'s), the team (to read or to write), how many people',
  'conv.status': 'status glyphs: ? waiting for you (the conversation, or a run below it — D147 §4.3.8), ! failed, a spinner while it works; ⧉ N: the coding agents at work below it',
  'conv.kind': 'a conversation a coding agent answers says which (D147): its monogram (CC, CX, GM, OC) — its name on the app',
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
  'chat.harnessChild': 'a coding agent the agent started (D147 §8 U6) as its own card where the spawn is: its monogram and name, the task, its state and what it does now, where it works (▣ sandbox:cwd), its counters (tool calls, files +a −d, cost, time), its plan, its last 3 blocks — read once the card is open and on screen, then kept current — its answer once done, and Open ↗ to its own chat',
  'chat.harnessChild.ask': 'a coding agent\'s permission request, plan approval, question or sign-in drawn on its card in the parent\'s chat (the harness.* cards) and answered on the child\'s run',
  'chat.harnessChild.steer': 'from its card: Stop (interrupts its turn), Cancel (confirmed; for good) and Message — a person\'s message straight to it (Enter queues or steers, ⌘/Ctrl+Enter interrupts first); the agent that started it is told, and its chat shows that notice',
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
  'composer.placeholder': 'its prompt by state: a new ask, view only, steer, answer, follow up — a coding agent\'s own words (it steers or queues, what interrupts, a reply to its park)',
  'composer.disabled': 'disabled in a conversation you may only read',
  'composer.class': 'the class for new chats (D116): icon and name, each one\'s description in its menu, only the classes you may use (GET /classes); your last pick is your default',
  'composer.model': 'the model: any bound provider\'s, grouped by provider — the open conversation\'s from its next turn, or the next new chat\'s; your last pick is your default',
  'composer.sandbox': 'the coding sandbox (D115), beside the model — only where the class (the conversation\'s, or the new chat\'s) has the sandbox toolset: grouped This conversation · Yours · Shared · Team, ones you may not use or the class does not allow disabled with the reason, ＋ New and Manage…; a pick binds it from the next turn (at home: the new chat starts in it)',
  'composer.agent': 'who answers new chats (D147): the built-in agent or a coding agent (GET /harnesses) with its monogram — one not available there, disabled with the reason; picking a coding agent hides the class (it resolves to one you may use that allows it) and the built-in model, and keeps the sandbox picker to sandboxes whose image has it with internet; your last pick is your default (prefs/agent)',
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
  'top.task.delegated': 'the unfolded task\'s Delegated section (D147 §8 U7): each coding agent it started below it — its state, its task, a way to its chat — read-only',
  'top.board': 'the Coding agents chip (D147 §8 U7): "⌨ 3 coding agents · 1 needs you" in a conversation with coding agents below it — at home, yours that run or need you — opening the board',
  'top.sandbox': '▣ its sandbox and working directory, and why a binding no longer resolves (gone, its manager unbound or down, its class no longer allows it); opens the working directory, switching among the attached ones, Detach, Manage…',
  'top.harness': 'a coding agent\'s conversation: which one, its state, and — its sandbox being shared — that the people who may use it can read what it does',
  'top.status': 'its status',
  'top.viewOnly': 'view only, when shared with you to read',
  'top.retry': 'Retry, when the run failed or was cancelled (a coding agent\'s: cut off or couldn\'t start — it resumes its session)',
  'top.compact': 'Compact (a coding agent\'s: only when it advertises /compact, which it is sent as)',
  'top.learn': 'Learn skill (the built-in agent\'s)',
  'top.memory': 'Memory (n) — opens its memory blocks (the built-in agent\'s)',
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
  'tools.board': 'the Coding agents board (D147 §8 U7): every coding agent in the conversation\'s tree (GET /runs/{root}/tree, kept current by the stream) — at home every one of yours that runs or needs you — in the order they started, never re-sorted as they change; each with its state, what it does now, where it works and its counters, its permission, question or sign-in answered in place (on its own run), Stop, Message (the agent is told) and Cancel task; only those that need you (the web: a filter; the app: sections Needs you, Running, Done)',
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
  'tools.sandboxes.terminal': 'a terminal in a sandbox whose manager offers one (tty): Open terminal in the ▣ popover (the active sandbox, at its working directory) and Terminal on a Sandboxes row — the page dials the manager as you (its per-person rules apply), the app through the tile\'s relay (which checks you may use it, D147 §4.2.8); closing it ends the shell',
  'tools.sandboxes.create': 'create a sandbox: manager, name, image, size, network (what the class allows), private or team, a working directory; made in a conversation it is bound there (a team conversation\'s is a team one), at home the next new chat starts in it',
  'tools.sandboxes.shareTerminal': 'share a sandbox of yours (this agent its home) with a terminal tile — the builtin sandbox-terminal (D121): its path (apps/sandbox-terminal by default), for you, or everyone who may use it when it is a team one; the shares it has now, each stopped (confirmed)',
  'tools.terminal': 'a terminal in a coding agent\'s conversation: a shell in its sandbox at its working directory, as you (D147 §2.1: terminals are part of the sandbox interface)',
  'tools.terminal.tabs': 'several terminals at once, as tabs of one dock: another shell here, ✕ ends one (and its shell), Hide keeps them running behind a pill ("2 terminals"); they stay open across conversations',

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
  'manage.classes': 'the classes: list, add, edit (name, icon, description, toolsets — coding agents too, and which of them —, MCP servers, sandbox managers and egress, model, system addendum, who), delete — a built-in resets to its default; the default for new chats; saving one that can move internal data out is confirmed; a refusal says why',
  'manage.harnesses': 'the Coding agents catalog (D147): each coding agent — whether you could start it and why not, the managers and images that have it, the sandboxes it was found or signed in on, the classes that allow it, its default and auto modes, its sign-in command; checking a running sandbox now',
  'manage.halt': 'halt every run (while runs are active), and resume',

  // States
  'state.halted': 'halted: the switch says so',
  'state.errors': 'a failed action tells the person why',
  'state.partition.share': 'in a person\'s own partition (a partitioned instance) the Shared view and a shared conversation\'s Share are the shared space\'s (the global instance); one of their own conversations has no Share there — only a copy shares it (state.partition.publish)',
  'state.partition.homes': 'in a person\'s partition a conversation has one of two homes, told by its id (from 2^40: their own; below: the shared space): the list merges both, an address (#c=) opens each at its home, and the stream follows it there (model/homes.js)',
  'state.partition.publish': 'in a person\'s partition, Share a copy… of one of their own conversations: who can see the copy, its session files or not, the original kept or deleted; the copy opens (POST /runs/{id}/publish)',
  'state.partition.copy': 'in a person\'s partition, a shared conversation\'s share dialog makes a private copy in their own space (POST /copy)',
  'state.partition.newShared': 'in a person\'s partition, New chat with options asks who can see it: only you (your own space), or the team or people you name (made in the shared space)',
  'state.partition.global': 'at a partitioned instance\'s global instance (the owner token), a note to sign in as a person for private conversations',
  'state.partition.sandboxes': 'in a person\'s partition, a notice naming a bound sandbox manager that can\'t keep people apart, and how to update it',
  'state.partition.hidden': 'in a partitioned instance the live stream closes while the page is hidden and resumes from its cursor when it shows (model/stream.js)',
  'state.partition.mcp': 'in a partitioned instance the settings\' MCP list shows the config\'s static servers, marking one with headers as working in shared (global) conversations only — bind it as a tile or a personal bind for your own (model/partition.js staticMcp)',
  'state.partition.hosted': 'in a person\'s partition, a non-secure (hosted) conversation: a ⚠ not private chip (header, row); the warning — whose private resources it uses, who can read it — every time it is opened into a page session, and the composer locked until it is started; locked for everyone while a wider audience waits for its host, who confirms or declines; once hosting ended, continue it without the host (model/hosted.js)',
  'state.partition.host': 'in a person\'s partition, a shared conversation\'s share dialog lets it use their private resources (the warning first; POST /hosting), and its host takes them back',
  'state.partition.copyIn': 'in a person\'s partition, a shared conversation\'s share dialog adds copies of their own session files (POST /copyin), saying who can read the copies; the originals stay private',
  'state.partition.harness': 'in a partitioned instance a coding agent works only in a person\'s own conversations (model/harness-homes.js): the global instance\'s page offers none ("Who answers" isn\'t shown); in a person\'s partition its sandbox is one of their own space (the team\'s and ones shared with them disabled, saying why; Create offered), New chat with options offers their own sandboxes whatever conversation is open, a new chat shared with others is the built-in agent\'s and takes no sandbox of theirs along, its sign-in is offered only for their own conversations (elsewhere a read-only card says why), its conversation offers no Share a copy, Copy to my own space or hosting and one in the shared space isn\'t left shared with no one (that would move it), its calls and the app\'s run terminal go to the run\'s home, and one in the shared space (from before this rule) is read, not driven — no message, Retry, mode or options',

  // Deep links
  'link.conv': 'an address opens a conversation (#c=<id>)',
  'link.auto': 'an address opens the Automations page or one automation (#auto[=kind:id])',
  'link.join': 'an invite link joins a conversation (#join=<token>)',

  // Needs you, beyond the tile
  'needs.push': 'a question, an approval or a failed automation reaches your phone (the backend pushes it; tapping it opens the conversation)',

  // Coding agents (D147)
  'harness.start': 'starting one: the sandbox it starts in — the one you last used with it (prefs/harness-sandbox), else one it fits — and a setup card when none fits (Create, filled in for it) or it isn\'t signed in there; a running sandbox it wasn\'t looked for in is checked; the ask carries harness {provider, options} and the sandbox — the mode is the person\'s setting',
  // Coding agents — the transcript (D147 §8 U3)
  'harness.tool': 'a coding agent\'s call as a card of its ACP kind (execute, edit, read, search, fetch, delete, move, think, switch_mode, other): what it did in words, what it came to (exit code, +a −d, lines, matches) and its status (pending, running, needs approval, failed, cancelled); a failed one opens by itself',
  'harness.tool.output': 'a command\'s card: the command (to copy), its output without colour codes — the end of it, all of it on asking — streamed while it runs, and the exit code',
  'harness.tool.diff': 'an edit\'s card: each file it changed (added, modified, deleted; +a −d), unfolding to its patch; a patch past 64 KiB says it stops there',
  'harness.subagent': 'a coding agent\'s own subagent (a Claude Task): its steps and text inside its card, which says how many; one whose card is further back shows where it is, marked ↳',
  'harness.plan': 'the coding agent\'s live plan, pinned: its progress and the entry in progress, unfolding to every entry with its status',
  'harness.usage': 'the context in use (as a share of the window, the tokens on asking) and the cost so far, when the coding agent reports them',
  'harness.files': 'what the conversation changed: its tool calls, the files edited and the lines added and deleted (across restarts of the coding agent); each edit\'s patch is on its card',
  // Coding agents — asking and controls (D147 §8 U4)
  'harness.permission': 'a coding agent\'s permission request: its own options as buttons (reject first when it defaults to no; one that raises it to a bypass mode only for the owner, marked ⚠ and confirmed), the call — title, command, a diff preview — what "always" would remember, and an optional word sent with a rejection',
  'harness.planApproval': 'its plan approval (leaving plan mode): the plan, its options, and a "keep planning" box sent with the rejection',
  'harness.question': 'its question: a form from its schema (choices, "Other", yes/no, numbers, text) with Submit and Skip; a page to open (url mode), then Done',
  'harness.mode': 'the conversation\'s live mode, from the agent\'s own modes (PATCH /runs/{id}/harness); a bypass mode is marked ⚠, the owner\'s only, and confirmed',
  'harness.options': 'its config options (model, effort…), switched live — the built-in model picker hides in its conversation',
  'harness.slash': 'the slash commands it advertises, offered while "/" is typed',
  'harness.steer': 'while its turn runs a message steers it or waits for it (the queued chip says which; a steered one is said); ⌘/Ctrl+Enter — Send now on the app — interrupts the turn and sends; Stop interrupts',
  'harness.autonomy': 'your Auto / Always approve per coding agent (/prefs/harness-mode): how its new conversations, and the ones the agent starts for you, begin',
  // Coding agents — terminals and sign-in (D147 §8 U5)
  'harness.login': 'a coding agent waiting for a sign-in (pendingState "login"): its methods — a login terminal running its sign-in command in the sandbox, then "Signed in? Retry"; an API key, sent once and never stored or shown; a device code (the page to open, the code) — the warning that credentials land in the sandbox\'s shared home, a confirm on a sandbox others may use, and whom to ask when you may not use it',
  // Coding agents — guided and saved sign-ins (D179, model/harness-signins.js)
  'harness.login.guided': 'the guided sign-in on the sign-in card: the provider\'s own CLI runs in the sandbox (Claude Code\'s `claude auth login`) — Open sign-in page ↗, Copy link, the code it shows and Finish, a status line in the CLI\'s words, and Use a terminal instead',
  'harness.login.remember': '"Remember for my other sandboxes" on the guided sign-in, with a name: `claude setup-token` instead, its token kept by the backend as a saved sign-in (never shown) — a person\'s own partition and a sandbox of theirs no one else uses only, else why not',
  'harness.signins': 'Coding-agent sign-ins (the Coding agents settings): your saved sign-ins per coding agent — what each is, its state (expiring in 14 days, expired, refused), the default; rename, make the default, paste a key or token, Forget; unpartitioned or in the shared space, why there are none',
  'harness.account': 'a coding agent\'s conversation names the account it uses ("using Work", or the sandbox\'s own sign-in) and switches it: the default, another saved sign-in, or the sandbox\'s own — the session resumes with it at the next message',

  // Projects (API.md §Projects)
  'proj.entry': 'the Projects entry (the sidebar; the drawer), with how many tasks need you',
  'proj.list': 'the projects you may see — yours and team ones — with their state',
  'proj.new': 'a new project: the scm provider, its repos (a picker of what you can reach), the sandbox (one of yours or a new one) and the policy basics',
  'proj.upgrade': '"Make this a project…": a conversation with a sandbox and git repos becomes a project, the conversation its first task',
  'proj.board': 'a project\'s board: its tasks by column (queued, working, needs you, PR, done), each with its number, title, state, branch, PR and CI',
  'proj.task.new': 'a new task: what to do, and a title',
  'proj.task.issues': 'tasks from issues: pick several of the project\'s issues, a task each (their text is shown as untrusted)',
  'proj.task.size': 'a task\'s size: small (a worktree in the project\'s sandbox) or big (its own sandbox, forked)',
  'proj.task.agent': 'who works on a task: the built-in agent or a coding agent',
  'proj.repos': 'a project\'s repos: add, remove, and each one\'s setup script',
  'proj.policy': 'a project\'s policy: every key, grouped (tasks, branches and PRs, CI and reviews, ports and setup, big tasks, cleanup, the coordinator)',
  'proj.members': 'a project\'s members and what team visibility grants (where sharing is possible)',
  'proj.status': 'a project\'s status: its sandbox, repos (fetched, protected), credentials (whose, until when — never the token), jobs and warnings',
  'proj.signin': 'signing in to the scm provider (its device code shown only to you), and Forget',
  'proj.coordinator': 'the project\'s coordinator: open it, write to it',
  'proj.events': 'the project\'s event feed: tasks, PRs, CI and reviews as they happen',
  'proj.team': 'a team project: the shared board (others\' tasks without their transcripts), "Work on this" in your own space, reviewing the team\'s changes before they apply, members who left',
  'proj.delete': 'archive or delete a project, keeping or deleting its sandbox, confirmed',
  'top.task.chips': 'a task conversation\'s branch and PR chips (each a link to the platform)',
  'top.task.pr': 'Open PR from a task conversation',
  'chat.task.prep': 'a task\'s workspace being prepared, step by step, with Retry when it failed; the sign-in card when it needs you to sign in',
  'link.project': 'links to the Projects page and to a project (#proj, #proj=<id>), and a task\'s way back to its project',

  // CI in the conversation (API.md §Projects "CI in the conversation")
  'ci.chip': 'the CI chip beside the coding agents chip: running jobs and time, passed, or what failed',
  'ci.dock': 'CI beside the coding agents board: each watched branch or PR with its state',
  'ci.jobs': 'CI runs, their jobs with live progress (steps done, the current step, time) and each job\'s steps',
  'ci.logs': 'a job\'s log: its end, more on asking, search, following it while it runs (where the platform allows), else a link to the live log',
  'ci.annotations': 'a check\'s annotations as file:line with level and message',
  'ci.links': 'links to the run, job, check, PR and branch on the platform',
  'ci.rerun': 'Re-run failed jobs — a person only, confirmed',
  'ci.watch': '"Watch CI for…" a branch or PR, and stop watching one',
  'ci.cards': 'a card in the conversation when CI passes or fails, with Open logs',
  'ci.board': 'CI on a project board\'s tasks and on the cards of coding agents that pushed',
};

// STAGED: Projects and CI land in stages, each key leaving these lists in
// the change that implements it in that view (grouped by stage, so the
// stages' changes don't touch each other's lines).
const STAGED = 'not built yet: Projects and CI in the conversation land in stages, this one with a later one';
const stagedWeb = [
  // projects: the page, the board, new projects and tasks, settings, a task's chips
  // projects: the coordinator, events, upgrades, team projects
  'proj.coordinator', 'proj.events', 'proj.upgrade', 'proj.team',
  // CI
];
const stagedNative = [
  // projects
  'proj.entry', 'proj.list', 'proj.new', 'proj.board', 'proj.task.new', 'proj.task.issues', 'proj.task.size', 'proj.task.agent',
  'proj.repos', 'proj.policy', 'proj.members', 'proj.status', 'proj.signin', 'proj.delete',
  'top.task.chips', 'top.task.pr', 'chat.task.prep', 'link.project',
  'proj.coordinator', 'proj.events', 'proj.upgrade', 'proj.team',
  // CI
];
const staged = (keys) => Object.fromEntries(keys.map((k) => [k, STAGED]));

// DIFFERENCES: keys a view does not implement ON PURPOSE, with the reason.
// Anything else missing from a view fails the features test.
export const DIFFERENCES = {
  web: {
    ...staged(stagedWeb),
    'needs.push': 'a web page does not receive pushes: the backend sends Needs-you to the person\'s xbin app (POST /api/xbin/notify), which opens the conversation in the native view',
    'composer.dictation': 'the browser and the OS dictate into any text box; the tile adds no control of its own',
    'composer.attach.camera': 'the browser\'s file picker offers the camera and the photo library itself',
  },
  native: {
    ...staged(stagedNative),
    'composer.keys': 'on a phone Return is a new line and Send is the button; the app\'s composer handles a hardware keyboard and IME composition itself',
    'composer.attach.paste': 'the app\'s composer owns the pasteboard: an image pasted there is uploaded like a picked one — nothing for the tile to draw',
    'composer.attach.drop': 'dropping files on the composer (iPad) is the app\'s: they upload like picked ones — nothing for the tile to draw',
    'chat.jumpLatest': 'the native view never lets the live end go: the app\'s transcript keeps a row still only at its bottom, so letting go below the reader would move what they read — until the renderer anchors a row across a trim (D130 E3/E4), and the app scrolls to the end itself',
    'tools.live.ports': 'the ▣ popover is the web\'s; on the app a live preview\'s screen has its own Check (tools.live.check), which probes what the Ports section would',
    'state.partition.publish': 'the native view shows a person\'s shared conversations and shares them, but publishing a copy of one of their own is the web\'s for now: the app\'s share sheet has no form for its choices yet',
    'state.partition.copy': 'as state.partition.publish: the app\'s share sheet shares a shared conversation; a private copy of one is made on the web for now',
    'state.partition.newShared': 'the app\'s new chat sheet makes a chat in the person\'s own space; a shared one is started on the web for now (or shared by a copy there)',
    'state.partition.host': 'as state.partition.publish: letting a shared conversation use one\'s private resources needs the warning\'s form, which the app\'s share sheet hasn\'t yet — it is done on the web; a hosted one is shown, warned about and locked in the app (state.partition.hosted)',
    'state.partition.copyIn': 'as state.partition.publish: the app\'s share sheet has no picker of one\'s own files yet — copies are added on the web',
    'tools.terminal.tabs': 'the app\'s terminal primitive closes its socket when its screen goes and names no session to attach again, so a native terminal is one pushed screen at a time (going back ends its shell: the relay ends a terminal it started once its client goes, D147 §4.2.8); the web\'s dock keeps several running',
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
