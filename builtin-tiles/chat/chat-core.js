// chat-core.js — the chat tile's engine, shared by its web page (index.html)
// and its native view (native.js). No DOM: models from every bound "llm"
// endpoint, the MCP client (Streamable HTTP) and tool routing, the streamed
// Chat Completions round with <think> splitting, and the up-to-8-round agent
// loop. The views draw from its state and events:
//
//   const chat = new Chat({ llm: xbin.iface('llm'), mcp: xbin.iface('mcp') });
//   chat.addEventListener('change', paint);      // after every event below
//   chat.loadModels(); chat.loadTools();
//   chat.send(text[, model]);  chat.abort();  chat.reset();
//
// Events (CustomEvent; detail in braces), each followed by a plain 'change':
//   note {text, error}    the status line (no LLM bound, failed providers)
//   models {models}       the model list is (re)loaded
//   tools {tools, errors} the MCP tools are (re)loaded
//   user {turn}           a user message was sent
//   busy {busy}           a turn started / ended
//   model {model}         setModel() chose a model
//   round {turn}          an assistant round began (a new bubble)
//   delta {turn}          streamed text/thinking changed (every SSE line)
//   round-end {turn}      the round's stream finished
//   tool {turn}           a tool call started;  tool-result {turn}: it ended
//   fail {turn, text}     a failure line on a turn (truncated, stopped, error)
//   reset                 the conversation was discarded
//
// history holds the turns the views draw, in order:
//   {id, role: 'user', text}
//   {id, role: 'assistant', text, think, thinking, thinkSecs, streaming, error, errorKind}
//   {id, role: 'tool', label, name, args, state: 'running'|'ok'|'error', result}
// messages is the OpenAI conversation sent to the model (memory only).

// ── interface bindings (docs/overview/11-interfaces.md, both multi:true) ─────
// "llm": OpenAI-compatible providers — models + completions.
// "mcp": Model Context Protocol servers — tools the model may call. Each
//        provider serves the MCP Streamable-HTTP endpoint at "/mcp".
export const asEndpoints = (iface) => iface?.endpoints ?? (iface?.url ? [{ url: iface.url, provider: iface.service }] : []);
export const epLabel = (e) => (e.instance ? `${e.provider}#${e.instance}` : e.provider);

export const MCP_PROTO = '2025-06-18';
export const MAX_ROUNDS = 8;   // cap the tool-use loop
const mcpUrl = (ep) => ep.url.replace(/\/+$/, '') + '/mcp';
// A model's value is '<endpoint index><SEP><model id>'. The page's inline
// script held a literal NUL here, which HTML parsing turns into U+FFFD — so
// U+FFFD is the separator the page has always used (and no model id has).
export const SEP = '\uFFFD';

// splitThink: reasoning that arrives as a leading <think>…</think> vs
// delta.reasoning_* — returns {thinking, content}.
export function splitThink(raw, apiThinking) {
  let thinking = apiThinking, content = raw;
  const lead = raw.trimStart();
  if (lead.startsWith('<think>')) {
    const end = lead.indexOf('</think>');
    if (end === -1) { thinking += lead.slice(7); content = ''; }
    else { thinking += lead.slice(7, end); content = lead.slice(end + 8).replace(/^\s+/, ''); }
  } else if (lead && '<think>'.startsWith(lead)) { content = ''; }
  return { thinking, content };
}

// readRpc: a JSON-RPC response — application/json, or the matching message
// on a text/event-stream.
async function readRpc(r, id) {
  const ct = r.headers.get('Content-Type') || '';
  if (!ct.includes('text/event-stream')) return r.json();
  const reader = r.body.getReader(), dec = new TextDecoder();
  let buf = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    buf += dec.decode(value, { stream: true });
    const events = buf.split(/\n\n/); buf = events.pop();
    for (const ev of events)
      for (const line of ev.split('\n')) {
        if (!line.startsWith('data:')) continue;
        try { const j = JSON.parse(line.slice(5).trim()); if (j.id === id) { reader.cancel(); return j; } } catch { /* keep reading */ }
      }
  }
  throw new Error('no matching response on MCP stream');
}

export class Chat extends EventTarget {
  // llm, mcp: the xbin.iface() values; fetch: defaults to xbin.fetch (the
  // tile's identity — binding the interfaces is the grant).
  constructor({ llm = null, mcp = null, fetch = null } = {}) {
    super();
    this.endpoints = asEndpoints(llm);
    this.mcpEndpoints = asEndpoints(mcp);
    this.fetch = fetch || ((url, opts) => globalThis.xbin.fetch(url, opts));
    this.models = [];        // [{value: '<endpoint index><SEP><model id>', label}]
    this.model = '';         // the selected value ('' until models load)
    this.note = '';
    this.noteError = false;
    this.tools = [];         // OpenAI tool defs sent to the model
    this.toolErrors = [];    // '<server>: <error>' from the last loadTools
    this.history = [];
    this.messages = [];
    this.controller = null;  // AbortController while a turn runs
    this.running = null;     // the running turn's promise
    this.route = {};         // exposed tool name -> { i, name }
    this.mcp = this.mcpEndpoints.map(() => ({ sid: null, ver: MCP_PROTO, ready: false }));
    this.rpcId = 1;
    this.seq = 0;
  }

  get busy() { return !!this.controller; }

  emit(type, detail = {}) {
    this.dispatchEvent(new CustomEvent(type, { detail }));
    this.dispatchEvent(new Event('change'));
  }
  setNote(text, error = false) { this.note = text || ''; this.noteError = !!error; this.emit('note', { text: this.note, error: this.noteError }); }
  turn(t) { const x = { id: ++this.seq, ...t }; this.history.push(x); return x; }
  fail(turn, text, kind) { turn.error = text; turn.errorKind = kind; this.emit('fail', { turn, text }); }
  setModel(value) { this.model = String(value ?? ''); this.emit('model', { model: this.model }); }
  abort() { if (this.controller) this.controller.abort(); }
  reset() {
    this.abort();
    this.messages = [];
    this.history = [];
    this.emit('reset');
  }

  // ── models (aggregated across every bound llm endpoint) ────────────────────
  async loadModels() {
    if (!this.endpoints.length) {
      this.setNote('No LLM bound yet — bind this tile\'s "llm" interface to one or ' +
        'more OpenAI-compatible providers (e.g. apps/llm-gw) in the ' +
        'admin Interfaces tab on the root page.', true);
      return;
    }
    const lists = await Promise.all(this.endpoints.map(async (ep, i) => {
      try {
        const r = await this.fetch(`${ep.url}/v1/models`);
        if (!r.ok) throw new Error(`${r.status}`);
        const { data = [] } = await r.json();
        return { i, ep, data };
      } catch (e) { return { i, ep, data: [], err: String(e.message ?? e) }; }
    }));
    const failed = lists.filter((l) => l.err);
    const opts = [];
    const isAlias = (m) => !!m.alias_of || m.owned_by === 'alias';
    for (const { i, ep, data } of lists) {
      data.sort((a, b) => (isAlias(b) - isAlias(a)) || a.id.localeCompare(b.id));
      for (const m of data) {
        const name = m.alias_of ? `${m.id} → ${m.alias_of}` : m.id;
        opts.push({ value: `${i}${SEP}${m.id}`, label: this.endpoints.length > 1 ? `${name} — ${epLabel(ep)}` : name });
      }
    }
    if (!opts.length) {
      this.setNote(failed.length
        ? `No models — ${failed.map((l) => `${epLabel(l.ep)}: ${l.err}`).join(' · ')}`
        : 'Bound providers report no models — configure them on their tiles.', true);
      return;
    }
    this.models = opts;
    if (!opts.some((o) => o.value === this.model)) this.model = opts[0].value;
    this.emit('models', { models: opts });
    this.setNote(failed.length ? `Some providers failed: ${failed.map((l) => epLabel(l.ep)).join(', ')}` : '');
  }

  // ── MCP client (Streamable HTTP) ──────────────────────────────────────────
  // JSON-RPC 2.0 over a single POST endpoint. Responses are application/json
  // OR text/event-stream; a stateful server assigns Mcp-Session-Id on init and
  // we echo it (plus the negotiated MCP-Protocol-Version) on later calls.
  async rpc(i, method, params, { notify = false } = {}) {
    const st = this.mcp[i];
    const msg = { jsonrpc: '2.0', method };
    if (params !== undefined) msg.params = params;
    if (!notify) msg.id = this.rpcId++;
    const headers = { 'Content-Type': 'application/json', 'Accept': 'application/json, text/event-stream' };
    if (st.sid) headers['Mcp-Session-Id'] = st.sid;
    if (st.ready) headers['MCP-Protocol-Version'] = st.ver;
    const r = await this.fetch(mcpUrl(this.mcpEndpoints[i]), { method: 'POST', headers, body: JSON.stringify(msg) });
    const sid = r.headers.get('Mcp-Session-Id'); if (sid) st.sid = sid;
    if (notify) return;                                  // 202 Accepted, no body
    if (!r.ok) throw new Error(`${method} → ${r.status} ${(await r.text()).slice(0, 200)}`);
    const resp = await readRpc(r, msg.id);
    if (resp.error) throw new Error(resp.error.message || JSON.stringify(resp.error));
    return resp.result;
  }
  async mcpInit(i) {
    if (this.mcp[i].ready) return;
    const res = await this.rpc(i, 'initialize', {
      protocolVersion: MCP_PROTO, capabilities: {},
      clientInfo: { name: 'xbin-chat', version: '1' },
    });
    if (res?.protocolVersion) this.mcp[i].ver = res.protocolVersion;
    this.mcp[i].ready = true;
    await this.rpc(i, 'notifications/initialized', undefined, { notify: true });
  }
  async mcpTools(i) {
    await this.mcpInit(i);
    const out = []; let cursor;
    do {
      const res = await this.rpc(i, 'tools/list', cursor ? { cursor } : undefined);
      out.push(...(res.tools ?? []));
      cursor = res.nextCursor;
    } while (cursor);
    return out;
  }

  // Tool aggregation: MCP tools → OpenAI function tools, with a route back to
  // the owning server. Exposed name = "m<server>_<tool>", clamped to OpenAI's
  // ^[A-Za-z0-9_-]{1,64}$ (the server index prefix keeps cross-server names
  // distinct; per-server names are already unique).
  async loadTools() {
    this.tools = []; this.route = {}; this.toolErrors = [];
    if (!this.mcpEndpoints.length) { this.emit('tools', { tools: this.tools, errors: [] }); return; }
    const errs = [];
    await Promise.all(this.mcpEndpoints.map(async (ep, i) => {
      try {
        for (const t of await this.mcpTools(i)) {
          let name = `m${i}_${t.name}`.replace(/[^A-Za-z0-9_-]/g, '_').slice(0, 64);
          while (this.route[name]) name = (name.slice(0, 60) + '_' + this.rpcId++).slice(0, 64);
          this.route[name] = { i, name: t.name };
          this.tools.push({ type: 'function', function: {
            name,
            description: (this.mcpEndpoints.length > 1 ? `[${epLabel(ep)}] ` : '') + (t.description || t.name),
            parameters: t.inputSchema || { type: 'object', properties: {} },
          } });
        }
      } catch (e) { errs.push(`${epLabel(ep)}: ${String(e.message ?? e)}`); }
    }));
    this.toolErrors = errs;
    this.emit('tools', { tools: this.tools, errors: errs });
  }

  // Execute one model-requested tool call against its MCP server; returns the
  // string the model sees back (MCP content flattened to text).
  async callTool(exposed, argsJson) {
    const r = this.route[exposed];
    if (!r) return { text: `unknown tool: ${exposed}`, isError: true };
    let args = {};
    try { args = argsJson ? JSON.parse(argsJson) : {}; }
    catch (e) { return { text: `invalid tool arguments: ${e.message}`, isError: true }; }
    try {
      const res = await this.rpc(r.i, 'tools/call', { name: r.name, arguments: args });
      const text = (res.content ?? []).map((c) =>
        c.type === 'text' ? c.text
        : c.type === 'image' ? '[image]'
        : c.type === 'audio' ? '[audio]'
        : c.type === 'resource' ? `[resource ${c.resource?.uri ?? ''}]\n${c.resource?.text ?? ''}`
        : JSON.stringify(c)).join('\n');
      return { text: text || '(no output)', isError: !!res.isError };
    } catch (e) { return { text: `tool call failed: ${String(e.message ?? e)}`, isError: true }; }
  }

  // ── one streamed completion round ──────────────────────────────────────────
  // Streams into the assistant turn; returns {content, toolCalls, finish}.
  // tool_calls are accumulated by index (OpenAI streams name once, arguments
  // in fragments).
  async streamRound(ep, model, turn, signal) {
    const req = { model, messages: this.messages, stream: true };
    if (this.tools.length) { req.tools = this.tools; req.tool_choice = 'auto'; }
    const started = Date.now();
    const r = await this.fetch(`${ep.url}/v1/chat/completions`, {
      method: 'POST', headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(req), signal,
    });
    if (!r.ok) throw new Error(`${r.status}: ${(await r.text()).slice(0, 400)}`);

    let raw = '', apiThinking = '', finish = null;
    const toolCalls = [];
    const update = (final) => {
      const { thinking, content } = splitThink(raw, apiThinking);
      turn.think = thinking; turn.text = content;
      const live = !final && !!thinking && !content;
      if (live || turn.thinking) turn.thinkSecs = Math.round((Date.now() - started) / 1000);
      turn.thinking = live;
    };

    const reader = r.body.getReader(), dec = new TextDecoder();
    let buf = '';
    for (;;) {
      const { done, value } = await reader.read();
      if (done) break;
      buf += dec.decode(value, { stream: true });
      const lines = buf.split('\n'); buf = lines.pop();
      for (const line of lines) {
        if (!line.startsWith('data:')) continue;
        const payload = line.slice(5).trim();
        if (payload === '[DONE]') continue;
        let j; try { j = JSON.parse(payload); } catch { continue; }
        if (j.error) throw new Error(j.error.message || JSON.stringify(j.error));
        const choice = j.choices?.[0] || {};
        const d = choice.delta || {};
        apiThinking += d.reasoning_content ?? d.reasoning ?? '';
        raw += d.content ?? '';
        for (const tc of d.tool_calls ?? []) {
          const k = tc.index ?? 0;
          const slot = toolCalls[k] ?? (toolCalls[k] = { id: '', type: 'function', function: { name: '', arguments: '' } });
          if (tc.id) slot.id = tc.id;
          if (tc.function?.name) slot.function.name += tc.function.name;
          if (tc.function?.arguments) slot.function.arguments += tc.function.arguments;
        }
        if (choice.finish_reason) finish = choice.finish_reason;
        update(false);
        this.emit('delta', { turn });
      }
    }
    update(true);
    turn.streaming = false;
    this.emit('round-end', { turn });
    return { content: turn.text, toolCalls: toolCalls.filter(Boolean), finish };
  }

  // send(text[, model]): start a turn. False (and nothing happens) when the
  // text is empty, a turn is running or no model is chosen; otherwise the
  // turn runs (this.running) and every step is an event.
  send(text, model = this.model) {
    text = String(text ?? '').trim();
    if (!text || this.controller || !model) return false;
    this.messages.push({ role: 'user', content: text });
    this.emit('user', { turn: this.turn({ role: 'user', text }) });
    this.controller = new AbortController();
    this.emit('busy', { busy: true });
    this.running = this.agentLoop(model, this.controller);
    return true;
  }

  async agentLoop(model, controller) {
    const [epIdx, id] = model.split(SEP);
    const ep = this.endpoints[Number(epIdx)] ?? this.endpoints[0];
    const assistant = (streaming) => this.turn({ role: 'assistant', text: '', think: '', thinking: false, thinkSecs: 0, streaming, error: '', errorKind: '' });
    let live = null;
    try {
      for (let round = 0; round < MAX_ROUNDS; round++) {
        live = assistant(true);
        this.emit('round', { turn: live });
        const { content, toolCalls, finish } = await this.streamRound(ep, id ?? model, live, controller.signal);

        if (toolCalls.length) {
          // Record the assistant's tool request, run each call, feed results back.
          this.messages.push({ role: 'assistant', content: content || null, tool_calls: toolCalls });
          for (const tc of toolCalls) {
            const r = this.route[tc.function.name];
            const label = r ? `${epLabel(this.mcpEndpoints[r.i])} · ${r.name}` : tc.function.name;
            const t = this.turn({ role: 'tool', label, name: tc.function.name, args: tc.function.arguments || '{}', state: 'running', result: null });
            this.emit('tool', { turn: t });
            const { text: out, isError } = await this.callTool(tc.function.name, tc.function.arguments);
            t.state = isError ? 'error' : 'ok'; t.result = out;
            this.emit('tool-result', { turn: t });
            this.messages.push({ role: 'tool', tool_call_id: tc.id, content: out.slice(0, 100_000) });
            if (controller.signal.aborted) throw new DOMException('aborted', 'AbortError');
          }
          continue;               // loop for the model's next turn
        }

        this.messages.push({ role: 'assistant', content });
        if (finish === 'length') this.fail(live, '— response truncated (token limit) —', 'truncated');
        break;                    // no tools requested → turn done
      }
    } catch (e) {
      if (live) { live.streaming = false; live.thinking = false; }
      const b = assistant(false);
      this.emit('round', { turn: b });
      if (e.name === 'AbortError') this.fail(b, '— stopped —', 'stopped');
      else this.fail(b, `Error: ${e.message}`, 'error');
    } finally {
      this.controller = null;
      this.emit('busy', { busy: false });
    }
  }
}
