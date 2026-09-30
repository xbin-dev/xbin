/**
 * logs-partition.js — the partition switcher of a partitioned tile's log
 * view (<bx-logs>; docs/partitions.md §Operating people's partitions).
 * Pure and DOM-free, so hack/logs-partition.test.mjs runs it in
 * node.
 *
 * On a partitioned tile, GET /api/xbin/logs answers the caller's own
 * partition's log (the credential decides) and names the partition it
 * serves in X-XBin-Partition; ?xbin-partition=global asks for the global
 * instance's log, ?user=<id> for a log its person shares with the caller (an
 * admin or a tile manager). A tile that isn't partitioned, and an xbind older
 * than partitioned tiles, never send the header: the view shows no switcher
 * there, and asks for nothing more.
 *
 * A choice is '' (the default: what the credential reaches — the viewer's
 * own partition, or the global instance for the root token), 'global', or
 * 'user:<id>'.
 */

// logsQuery(choice) → the query GET /logs gains for a choice ('' for none).
export function logsQuery(choice) {
  if (choice === 'global') return '&xbin-partition=global';
  if (typeof choice === 'string' && choice.startsWith('user:') && choice.length > 5) return `&user=${encodeURIComponent(choice.slice(5))}`;
  return '';
}

// echoOK(choice, header) → whether an answer's X-XBin-Partition is the one
// asked for: the default takes whatever the credential reaches; a named
// choice must come back named (an xbind that ignored the parameter answered
// another log — never shown as the one asked for).
export const echoOK = (choice, header) => !choice || header === choice;

// defaultLabel(header) → the default choice's words, from what the default
// answered: "yours" (the viewer's own partition), or "global" when the
// credential reaches no person's partition (the root token).
export const defaultLabel = (header) => (header === 'global' ? 'global' : 'yours');

const day = (t) => {
  const d = new Date(t);
  return Number.isNaN(d.getTime()) ? '' : d.toISOString().slice(0, 10);
};

// logChoices(listing, defaultHeader) → the switcher's entries
// [{value, label, title}], or [] where there is nothing to switch: the tile
// isn't partitioned (the listing says so, or there is none — a 404 from an
// older xbind). listing is GET /api/xbin/partitions?tile=<t> as the viewer
// reads it; defaultHeader what the default answered (X-XBin-Partition, ''
// before or without an answer). Entries: the default; the global instance's
// log when the tile runs one (and the default isn't it); and, for an admin,
// each person who shares their log now (the admins' rows carry logShare).
export function logChoices(listing, defaultHeader = '') {
  const spec = listing && typeof listing === 'object' ? listing.spec : null;
  if (!spec?.user && !defaultHeader) return [];
  const own = defaultLabel(defaultHeader);
  const out = [{ value: '', label: own, title: own === 'yours' ? 'your own partition\'s log' : 'the global instance\'s log' }];
  if (spec?.global && own !== 'global') out.push({ value: 'global', label: 'global', title: 'the global instance\'s log (one instance for what isn\'t a person\'s)' });
  const rows = Array.isArray(listing?.partitions) ? listing.partitions : [];
  for (const r of rows) {
    if (!r || typeof r.user !== 'string' || !r.logShare?.until || r.partition === defaultHeader) continue;
    const until = day(r.logShare.until);
    out.push({ value: `user:${r.user}`, label: `${r.user}'s (shared)`, title: `${r.user} shares their partition's log${until ? ` until ${until}` : ''}` });
  }
  return out;
}

// badgeText(choices, dep) → the corner badge: what the view shows.
// With a switcher beside it (two choices or more) the switcher names the
// log; a lone choice is named in the badge.
export function badgeText(choices, dep = '') {
  if (dep) return `read-only logs · ${dep}`;
  if (choices.length !== 1) return 'read-only logs';
  return choices[0].label === 'yours' ? 'read-only logs · your partition' : 'read-only logs · global';
}
