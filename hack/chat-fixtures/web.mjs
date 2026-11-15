// hack/chat-fixtures/web.mjs — the Agent tab's fold as the chat kit's block
// model. Until the kit's views (F3) this was the mapping itself, a test-only
// adapter; it is shipped code now (web/agent-fold.js transcriptOf, what
// <bx-agent> draws from), and this file only names it for the tests.
export { transcriptOf as webTranscript, blockOf } from '../../web/agent-fold.js';
