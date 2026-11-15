// old-app-agent — the workspace agent's view written WITHOUT asking
// xbin.native.supports(), opened by an app released before D233 (the caps in
// data.json: transcript 2, question 1, plan 1, composer 1, and no card,
// connection, request or session). The runtime builds the tree and reports
// every addition it meets as `unsupported`; that app shows the web page
// instead (tree.md §8). The view that ships branches on supports() and
// draws the older way there (docs/native.md §The workspace agent's nodes):
// a notice for a request, the tile's name for a session, menus for a
// question, an `empty` with buttons for the empty slot.
import { html, render, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let v = null;
async function load() { v = await selfApi('/ws/view'); paint(); }

const has = (name, rev) => xbin.native.supports(name, rev);

const paint = () => render(!v ? nothing : html`
  <screen title="Workspace agent" subtitle=${has('request') ? 'this app' : 'an app from before D233'} style="scroll">
    <connection connected=${v.connected} model=${v.model} @tap=${() => {}}/>
    <transcript follow empty=${v.empty} @suggestion=${() => {}}>
      <plan title="Plan" numbered entries=${v.plan} tags=${v.tags} note=${v.note} meta=${v.meta}/>
      <card title=${v.card.title} note=${v.card.note}><text>${v.card.text}</text><button @tap=${() => {}}>Open scope</button></card>
      <request id=${v.request} kind="call"/>
      <session id=${v.session} tile="apps/orders"/>
      <question title="Which one do you mean?" schema=${v.schema} style="rows" other=${{ which: 'other' }} paged
                submitLabel="Answer" skipLabel="Skip" @submit=${() => {}} @skip=${() => {}}/>
    </transcript>
    <composer value="" context=${v.context} replies=${v.replies} hint=${v.hint}
              @context=${() => {}} @remove-context=${() => {}} @reply=${() => {}}/>
  </screen>`);

load();
