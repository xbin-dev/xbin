// A canned Chat for the design's §18.8 example (plans/native.md): the sample
// conversation of the design's tree behind the shape of the real engine,
// builtin-tiles/chat/chat-core.js (whose own native.js runs in the
// native/fixtures/tile-chat fixture).
export class Chat extends EventTarget {
  constructor() {
    super();
    this.model = ''; this.models = []; this.tools = []; this.history = []; this.busy = false; this.note = '';
  }
  async loadModels() { this.models = [{ value: '0 qwen3-32b', label: 'qwen3-32b' }]; this.model = '0 qwen3-32b'; }
  async loadTools() {
    this.tools = ['search_issues', 'get_issue', 'list_labels'];
    this.history = [
      { id: 1, role: 'user', text: 'How many open issues are labelled bug?' },
      { id: 2, role: 'tool', label: 'apps/github-mcp · search_issues', state: 'ok', args: '{"q":"is:open label:bug"}', result: '{"total_count":7}' },
      { id: 3, role: 'assistant', think: 'The tool says 7.', thinking: false, thinkSecs: 2, text: 'There are **7** open issues labelled `bug`.', streaming: false },
    ];
  }
  setModel(v) { this.model = v; this.dispatchEvent(new Event('change')); }
  reset() { this.history = []; this.dispatchEvent(new Event('change')); }
  send(text) { this.history.push({ id: this.history.length + 1, role: 'user', text }); this.dispatchEvent(new Event('change')); }
  abort() {}
}
