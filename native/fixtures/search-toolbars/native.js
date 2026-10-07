// search-toolbars — an issue tracker's list on vocabulary rev 2: search with
// scopes (open / closed / all), recent searches as suggestions and a submit
// that runs the query; toolbars at three places (a sort menu with a "Group
// by" submenu at the leading end, New at the trailing end, a count and "Back
// to top" in a bottom toolbar); pull to refresh that stays up while the
// reload runs (`refreshing`); rows with a leading full-swipe action (mark
// read) and trailing ones (archive with a full swipe, delete); the list keeps
// the row the user was reading in place (`anchor`), reports its end (`edge`)
// and jumps back to the start (`scrollTo`).
import { html, render, repeat, nothing } from '/vendor/xb-native.js';
import { selfApi } from '/vendor/bx-kit.js';

let issues = null;
let total = 0;
let query = '';
let scope = 'open';
let sort = 'updated';
let group = 'none';
let refreshing = false;
let atEnd = false;
let tops = 0;
const recent = [
  { value: 'deadlock', label: 'deadlock' },
  { value: 'label:billing', label: 'label:billing', icon: 'tag' },
  { value: 'author:ana', label: 'author:ana', icon: 'person' },
];

async function load() {
  const q = new URLSearchParams({ state: scope, sort });
  if (query) q.set('q', query);
  const r = await selfApi(`/issues?${q}`);
  issues = r.issues;
  total = r.total;
  paint();
}

async function refresh() {
  refreshing = true;
  paint();
  await load();
  refreshing = false;
  paint();
}

const choose = (k, v) => () => { if (k === 'sort') sort = v; else group = v; load(); };
const check = (on) => (on ? 'check' : nothing);

const row = (i) => html`
  <row title=${i.title} subtitle=${`#${i.number} · ${i.author}`} detail=${i.updated}
       badge=${i.comments ? String(i.comments) : nothing} tone=${i.unread ? 'accent' : nothing} @tap=${() => {}}>
    <actions edge="leading" full>
      <button icon=${i.unread ? 'eye' : 'eye-slash'} @tap=${() => { i.unread = !i.unread; paint(); }}>${i.unread ? 'Mark read' : 'Mark unread'}</button>
    </actions>
    <actions edge="trailing" full>
      <button icon="archive" @tap=${() => { issues = issues.filter((x) => x !== i); total--; paint(); }}>Archive</button>
      <button icon="trash" role="destructive" confirm=${{ title: `Delete #${i.number}?`, label: 'Delete', destructive: true }}
              @tap=${() => { issues = issues.filter((x) => x !== i); total--; paint(); }}>Delete</button>
    </actions>
  </row>`;

const paint = () => render(!issues ? nothing : html`
  <screen title="Issues" style="list" search=${query} refreshable ?refreshing=${refreshing}
      scopes=${[{ value: 'open', label: 'Open' }, { value: 'closed', label: 'Closed' }, { value: 'all', label: 'All' }]}
      scope=${scope} suggestions=${recent}
      @search=${(e) => { query = e.value; paint(); }}
      @submit=${(e) => { query = e.value; load(); }}
      @scope=${(e) => { scope = e.value; load(); }}
      @refresh=${refresh}>
    <toolbar place="leading">
      <menu icon="filter" label="Sort">
        <button icon=${check(sort === 'updated')} @tap=${choose('sort', 'updated')}>Recently updated</button>
        <button icon=${check(sort === 'created')} @tap=${choose('sort', 'created')}>Newest</button>
        <divider/>
        <menu label="Group by" icon="list">
          <button icon=${check(group === 'none')} @tap=${choose('group', 'none')}>No grouping</button>
          <button icon=${check(group === 'label')} @tap=${choose('group', 'label')}>Label</button>
          <button icon=${check(group === 'assignee')} @tap=${choose('group', 'assignee')}>Assignee</button>
        </menu>
      </menu>
    </toolbar>
    <toolbar place="trailing">
      <button icon="plus" @tap=${() => {}}>New issue</button>
    </toolbar>
    <toolbar place="bottom">
      <badge>${`${total} ${scope === 'all' ? '' : `${scope} `}issues`}</badge>
      ${atEnd ? html`<button icon="collapse" @tap=${() => { tops++; paint(); }}>Back to top</button>` : nothing}
    </toolbar>
    <list anchor=${issues[1]?.id ?? nothing} scrollTo=${`start#${tops}`}
        @edge=${(e) => { if (e.edge === 'end') { atEnd = e.at; paint(); } }}>
      ${repeat(issues, (i) => i.id, row)}
    </list>
  </screen>`);

load();
