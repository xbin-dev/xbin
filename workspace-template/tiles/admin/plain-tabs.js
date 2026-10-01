/**
 * The admin console's tabs that take no inputs: each loads its own data and
 * follows its own events, so the router (admin.js) only needs its element.
 * render() draws any tab id listed in PLAIN_TABS. Adding a tab of this kind:
 * the element under tabs/, its import and one line here, and its GROUPS
 * entry in admin.js (docs/maintenance.md → "The admin console's tabs").
 */
import { html } from 'lit';
import './tabs/sandboxes.js';
import './tabs/deployments.js';
import './tabs/branding.js';
import './tabs/nativeapp.js';
import './tabs/terminals.js';
import './tabs/policies.js';
import './tabs/partitions.js';

export const PLAIN_TABS = {
  sandboxes: () => html`<bx-admin-sandboxes></bx-admin-sandboxes>`,
  deployments: () => html`<bx-admin-deployments></bx-admin-deployments>`,
  branding: () => html`<bx-admin-branding></bx-admin-branding>`,
  nativeapp: () => html`<bx-admin-nativeapp></bx-admin-nativeapp>`,
  terminals: () => html`<bx-admin-terminals></bx-admin-terminals>`,
  policies: () => html`<bx-admin-policies></bx-admin-policies>`,
  partitions: () => html`<bx-admin-partitions></bx-admin-partitions>`,
};
