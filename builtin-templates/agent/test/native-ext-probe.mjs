// native-ext-probe.mjs — native-stub.mjs (as data.setup) plus a feature
// module that hooks every native seam (native/ext.js): what
// hack/agent-template-harness.test.mjs checks the seams draw where they say.
// Each hook answers only for what it recognises, as a real module would.
import stub from './native-stub.mjs';
import { html } from '/vendor/xb-native.js';
import { ext } from '../native/ext.js';
import { ui, ctx } from '../native/ui.js';

export { push, route, result } from './native-stub.mjs';

export default async function setup(arg) {
  await stub(arg);
  ext.register({
    block: (b) => (b.k === 'tool' && b.name === 'acp:execute' ? html`<text>${'probe block: ' + b.headline}</text>` : null),
    end: (v) => (v.run.engine === 'harness' ? html`<notice tone="info" text=${'probe end: ' + v.run.harness.state}/>` : null),
    toolbar: (v) => html`<button icon="star" @tap=${() => {}}>${v ? 'probe toolbar' : 'probe home toolbar'}</button>`,
    menu: (v) => html`<button icon="star" @tap=${() => {}}>${'probe menu #' + v.run.id}</button>`,
    subtitle: (v) => (v.run.engine === 'harness' ? 'probe subtitle' : null),
    main: () => html`<button icon="star" @tap=${() => {}}>probe main</button>`,
    composer: (v) => ({ placeholder: v ? 'probe placeholder' : '', slash: [{ name: 'probe', description: 'a probe command' }] }),
    newChat: (f) => ({ tpl: () => html`<section title="probe section"><field label="Probe" value=${f.probe || ''}/></section>`, body: () => ({ probe: 'yes' }) }),
    screen: (s) => (s.kind === 'probe' ? html`<screen title="probe screen"/>` : null),
  });
}

// {call} steps: push the probe's screen; open the new-chat sheet with a first message.
export function probeScreen() { ui.stack.push({ kind: 'probe' }); ctx.paint(); }
export function newChat(text) { ui.newChat = { text, title: '', system: '', class: '' }; ctx.paint(); }
