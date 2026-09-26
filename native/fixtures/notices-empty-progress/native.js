// notices-empty-progress — a photo-backup tile: notices in every tone,
// determinate and indeterminate progress, badges in every tone (one
// pulsing), an upload queue list mixing a progress, a notice, a section and
// rows (with "load more"), and empty states.
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let st = null;
let queue = [];
let cursor = null;

const n = (x) => x.toLocaleString();
const mb = (b) => `${(b / 1e6).toFixed(1)} MB`;

async function load() {
  st = await (await xbin.fetch(`/api/${xbin.self}/status`)).json();
  const q = await (await xbin.fetch(`/api/${xbin.self}/queue`)).json();
  queue = q.items; cursor = q.next;
  paint();
}
async function more() {
  if (!cursor) return;
  const q = await (await xbin.fetch(`/api/${xbin.self}/queue?after=${cursor}`)).json();
  queue = [...queue, ...q.items]; cursor = q.next;
  paint();
}

const paint = () => render(!st ? html`<screen title="Photo backup" style="scroll"><progress label="Connecting…"/></screen>` : html`
  <screen title="Photo backup" subtitle=${st.device} style="scroll">
    <toolbar>
      <badge tone="ok" pulse>syncing</badge>
      <button icon="pause" @tap=${() => {}}>Pause</button>
    </toolbar>
    <stack gap="l">
      <stack gap="s">
        <progress value=${st.done / st.total} label=${`Uploading ${n(st.done)} of ${n(st.total)} · ${Math.round((100 * st.done) / st.total)}%`}/>
        <progress label="Scanning the library for new photos…"/>
        <stack axis="h" gap="xs" wrap>
          <badge tone="muted">${`${st.queued} queued`}</badge>
          <badge tone="accent">HEIC → JPEG</badge>
          <badge tone="warn">${`${st.skipped} skipped`}</badge>
          <badge tone="danger">${`${st.failed.length} failed`}</badge>
          <badge>originals</badge>
        </stack>
      </stack>
      <notice tone="danger" title=${`${st.failed.length} uploads failed`} text="The server refused them: the album quota is full. Free space or raise the quota, then retry."/>
      <notice tone="warn" title="Storage almost full" text=${`${st.storage.used} of ${st.storage.size} used on the backup volume.`}/>
      <notice tone="info" text="Uploads run on Wi-Fi only; cellular is paused to save data."/>
      <notice tone="accent" title="New: shared albums" text="Albums shared with you can be backed up too."/>
      <notice tone="ok" title="Up to date until Sep 19" text=${`${n(st.backedUp)} photos and videos are safe.`}/>
      <notice tone="muted" text="Last full scan 3 hours ago."/>
      <list style="inset" @more=${more}>
        <progress value=${st.current.sent / st.current.size} label=${`${st.current.name} · ${mb(st.current.sent)} of ${mb(st.current.size)}`}/>
        <notice tone="warn" text="Videos over 2 GB upload last."/>
        ${repeat(queue, (q) => q.name, (q) => html`<row title=${q.name} subtitle=${q.album} detail=${mb(q.size)} icon=${q.video ? 'play' : 'photo'} mono="title"/>`)}
        <section title="Failed" badge=${String(st.failed.length)}>
          ${repeat(st.failed, (f) => f.name, (f) => html`<row title=${f.name} subtitle=${f.error} icon="error" tone="danger" mono="title"/>`)}
        </section>
      </list>
      <list style="grouped">
        <empty icon="archive" title="No skipped duplicates" text="Photos that are already on the server show up here."/>
      </list>
      <empty icon="cloud" title="No other devices" text="Install the app on another device to back it up here too."/>
    </stack>
  </screen>`);

load();
