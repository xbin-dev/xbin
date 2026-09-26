// tabs-bar — a tile whose whole UI is a tab bar (style "bar"): each tab
// holds its own navigation stack. The app restored the last tab
// ("settings", from xbin.native.state); the script selects "Activity".
import { html, render, repeat, nothing } from '/vendor/xb-native.js';

let feed = null;
let tab = xbin.native.state?.tab ?? 'home'; // the app kept it across runtime restarts

async function load() {
  feed = await (await xbin.fetch(`/api/${xbin.self}/feed`)).json();
  paint();
}

const paint = () => render(!feed ? nothing : html`
  <tabs style="bar" selected=${tab} @change=${(e) => { tab = e.key; xbin.native.saveState({ tab }); paint(); }}>
    <tab key="home" title="Home" icon="home">
      <nav><screen title="Home" style="list" large>
        <section title="Pinned">${repeat(feed.pinned, (p) => p.id, (p) => html`<row title=${p.title} icon="pin" nav/>`)}</section>
      </screen></nav>
    </tab>
    <tab key="activity" title="Activity" icon="bell" badge=${String(feed.activity.filter((a) => a.unread).length)}>
      <nav><screen title="Activity" style="list" large>
        <section>
          ${repeat(feed.activity, (a) => a.id, (a) => html`
            <row title=${a.title} subtitle=${a.who} detail=${a.ago} icon=${a.icon} badge=${a.unread ? 'new' : nothing} tone=${a.unread ? 'accent' : nothing}/>`)}
        </section>
      </screen></nav>
    </tab>
    <tab key="settings" title="Settings" icon="gear">
      <nav><screen title="Settings" style="form"><section><toggle label="Notifications" value=${true}/></section></screen></nav>
    </tab>
  </tabs>`);

load();
