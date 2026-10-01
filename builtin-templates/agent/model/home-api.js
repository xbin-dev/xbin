// model/home-api.js — calls at a conversation's home (model/homes.js): the
// kit's selfApi and xbin.fetch, sent to the global instance ({partition:
// 'global'}) for a shared conversation in a person's partition, and exactly
// as ever everywhere else.
import { selfApi } from '/vendor/bx-kit.js';
import { at, homeOf, runOfPath } from './homes.js';

/** runApi(path, opts): the kit's selfApi, at the home of the conversation the path names. */
export const runApi = (path, opts) => selfApi(path, at(homeOf(runOfPath(path)), opts));

/** homeApi(home, path, opts): the kit's selfApi at a home ('' = this page's own backend). */
export const homeApi = (home, path, opts) => selfApi(path, at(home, opts));

/** homeFetch(url, opts): xbin.fetch of a URL under this backend's prefix, at
 * the home of the conversation it names (raw bytes: uploads, previews). */
export function homeFetch(url, opts = {}) {
  const self = `/api/${globalThis.xbin?.self ?? ''}`;
  const u = String(url);
  return globalThis.xbin.fetch(url, at(homeOf(runOfPath(u.startsWith(self) ? u.slice(self.length) : '')), opts));
}
